/**
 * Reference redaction scan/apply for the trajectory contract (v2.5.0 draft).
 *
 * Scope: deterministic detectors from docs/contracts/redaction-policy-1.0.json.
 * This is NOT a production DLP engine and makes no claim to catch contextual
 * re-identification. Class-free-text is manual_review by design.
 *
 * Evidence hygiene: scan results never include the matched substring, only the
 * class, action, and offset. A report can be attached to a gate without copying
 * the sensitive value into the report.
 */

function compileClass(entry) {
  if (entry.detector !== "regex" || !entry.pattern) return { ...entry, regex: null };
  return { ...entry, regex: new RegExp(entry.pattern, entry.flags ?? "g") };
}

export function compilePolicy(policy) {
  if (!policy || !Array.isArray(policy.classes)) throw new Error("redaction policy has no classes");
  return {
    policyVersion: policy.policyVersion,
    defaultAction: policy.defaultAction,
    classes: policy.classes.map(compileClass),
  };
}

/** Count regex hits without returning the matched text. */
export function scanText(text, compiled) {
  const hits = [];
  const manualReview = [];
  for (const entry of compiled.classes) {
    if (!entry.regex) {
      if (entry.action === "manual_review") manualReview.push(entry.id);
      continue;
    }
    entry.regex.lastIndex = 0;
    let match;
    while ((match = entry.regex.exec(text)) !== null) {
      hits.push({ classId: entry.id, action: entry.action, countKey: entry.countKey, index: match.index });
      if (match[0].length === 0) entry.regex.lastIndex += 1;
    }
  }
  return { hits, manualReview };
}

export function scanValue(value, compiled) {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  return scanText(text, compiled);
}

/**
 * Apply the policy to one string. Credential classes discard the record; other
 * classes replace the value with a typed placeholder. Pseudonymization requires
 * an explicit callback backed by the dataset key: without one, the record is
 * blocked instead of silently downgraded to a non-linkable placeholder.
 */
export function applyPolicy(text, compiled, { pseudonymize } = {}) {
  let output = text;
  const counts = {};
  let discard = false;
  let blocked = false;

  for (const entry of compiled.classes) {
    if (!entry.regex) continue;
    entry.regex.lastIndex = 0;
    if (entry.action === "discard_record") {
      if (entry.regex.test(output)) {
        discard = true;
        counts[entry.countKey] = (counts[entry.countKey] ?? 0) + 1;
      }
      continue;
    }
    if (entry.action === "pseudonymize") {
      entry.regex.lastIndex = 0;
      if (typeof pseudonymize !== "function") {
        if (entry.regex.test(output)) {
          blocked = true;
          if (entry.countKey) counts[entry.countKey] = (counts[entry.countKey] ?? 0) + 1;
        }
        continue;
      }
      entry.regex.lastIndex = 0;
      let replaced = 0;
      output = output.replace(entry.regex, (match) => {
        replaced += 1;
        return pseudonymize(entry.id, match);
      });
      if (replaced > 0 && entry.countKey) counts[entry.countKey] = (counts[entry.countKey] ?? 0) + replaced;
      continue;
    }
    if (entry.action !== "placeholder") continue;
    entry.regex.lastIndex = 0;
    let replaced = 0;
    output = output.replace(entry.regex, () => {
      replaced += 1;
      return entry.replacement ?? `<${entry.id.toUpperCase()}>`;
    });
    if (replaced > 0 && entry.countKey) counts[entry.countKey] = (counts[entry.countKey] ?? 0) + replaced;
  }

  return { text: output, counts, discard, blocked };
}

export function emptyCounts(policy) {
  const counts = {};
  for (const entry of policy.classes) {
    if (entry.countKey && counts[entry.countKey] === undefined) counts[entry.countKey] = 0;
  }
  return counts;
}
