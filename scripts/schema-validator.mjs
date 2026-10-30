/**
 * Minimal JSON Schema evaluator for P0.
 *
 * This intentionally supports only the keywords used by this repository's
 * schemas. CI must replace it with a standards-complete validator (ajv) before
 * claiming general JSON Schema conformance.
 */

export function validateSchema(schema, value, options = {}) {
  const root = options.rootSchema ?? schema;
  const errors = [];

  function resolveRef(ref) {
    if (!ref.startsWith("#/")) throw new Error(`unsupported $ref: ${ref}`);
    let node = root;
    for (const raw of ref.slice(2).split("/")) {
      const key = raw.replaceAll("~1", "/").replaceAll("~0", "~");
      node = node?.[key];
      if (!node) throw new Error(`unresolvable $ref: ${ref}`);
    }
    return node;
  }

  function equal(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }

  function matches(s, v, path) {
    const probe = [];
    walk(s, v, path, probe);
    return probe.length === 0;
  }

  function walk(s, v, path, out) {
    if (!s || typeof s !== "object") return;

    if (s.$ref) {
      walk(resolveRef(s.$ref), v, path, out);
      return;
    }

    if (s.allOf) for (const sub of s.allOf) walk(sub, v, path, out);

    if (s.anyOf) {
      if (!s.anyOf.some((sub) => matches(sub, v, path))) out.push(`${path}: no anyOf branch matched`);
    }

    if (s.oneOf) {
      const count = s.oneOf.filter((sub) => matches(sub, v, path)).length;
      if (count !== 1) out.push(`${path}: oneOf matched ${count} branches`);
    }

    if (s.if) {
      if (matches(s.if, v, path)) {
        if (s.then) walk(s.then, v, path, out);
      } else if (s.else) {
        walk(s.else, v, path, out);
      }
    }

    if (Object.hasOwn(s, "const") && !equal(v, s.const)) {
      out.push(`${path}: expected const ${JSON.stringify(s.const)}`);
    }

    if (s.enum && !s.enum.some((candidate) => equal(v, candidate))) {
      out.push(`${path}: not in enum`);
    }

    if (s.type) {
      const types = Array.isArray(s.type) ? s.type : [s.type];
      const actual = v === null ? "null" : Array.isArray(v) ? "array" : Number.isInteger(v) ? "integer" : typeof v;
      const ok = types.some((type) => {
        if (type === "number") return typeof v === "number";
        if (type === "integer") return Number.isInteger(v);
        return type === actual;
      });
      if (!ok) {
        out.push(`${path}: expected ${types.join("|")}, got ${actual}`);
        return;
      }
    }

    if (typeof v === "string") {
      if (s.minLength !== undefined && [...v].length < s.minLength) out.push(`${path}: shorter than minLength`);
      if (s.pattern && !new RegExp(s.pattern).test(v)) out.push(`${path}: pattern mismatch`);
      if (s.format === "uri") {
        try { new URL(v); } catch { out.push(`${path}: invalid uri`); }
      }
      if (s.format === "date-time" && Number.isNaN(Date.parse(v))) out.push(`${path}: invalid date-time`);
    }

    if (typeof v === "number" && s.minimum !== undefined && v < s.minimum) out.push(`${path}: below minimum`);
    if (typeof v === "number" && s.maximum !== undefined && v > s.maximum) out.push(`${path}: above maximum`);

    if (Array.isArray(v)) {
      if (s.minItems !== undefined && v.length < s.minItems) out.push(`${path}: fewer than minItems`);
      if (s.maxItems !== undefined && v.length > s.maxItems) out.push(`${path}: more than maxItems`);
      if (s.uniqueItems) {
        for (let i = 0; i < v.length; i++) {
          for (let j = i + 1; j < v.length; j++) {
            if (equal(v[i], v[j])) out.push(`${path}: duplicate item ${i}/${j}`);
          }
        }
      }
      if (s.items) v.forEach((item, index) => walk(s.items, item, `${path}[${index}]`, out));
    }

    if (v && typeof v === "object" && !Array.isArray(v)) {
      for (const key of s.required ?? []) {
        if (!Object.hasOwn(v, key)) out.push(`${path}: missing required ${key}`);
      }
      for (const [key, value] of Object.entries(v)) {
        if (s.properties?.[key]) {
          walk(s.properties[key], value, `${path}.${key}`, out);
        } else if (s.additionalProperties === false) {
          out.push(`${path}: unexpected property ${key}`);
        } else if (s.additionalProperties && typeof s.additionalProperties === "object") {
          walk(s.additionalProperties, value, `${path}.${key}`, out);
        }
      }
    }
  }

  walk(schema, value, "$", errors);
  return { ok: errors.length === 0, errors };
}

export function assertValid(schema, value, label) {
  const result = validateSchema(schema, value);
  if (!result.ok) throw new Error(`${label}: ${result.errors.join("; ")}`);
  return result;
}
