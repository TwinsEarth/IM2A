// IM2A kernel registry: one manifest, one bus, one gate.
// Phase 1 scope: topology, lifecycle, alias resolution, honest module status.
// No business handlers live here; modules forward to the existing bridge.

import { readFile, stat } from "node:fs/promises";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

export const MODULE_STATUS = new Set(["available", "partial", "planned", "unavailable"]);
export const TOOL_NAME_RE = /^im2a_[a-z][a-z0-9_]{2,40}$/;
// Wire names are not all `im2a_`-prefixed (the bridge still serves legacy names
// such as memory_search), so the runtime surface uses a general MCP-name rule.
export const RUNTIME_TOOL_RE = /^[a-z][a-z0-9_]{2,50}$/;
export const LOADABLE = new Set(["available", "partial"]);

const HERE = dirname(fileURLToPath(import.meta.url));
export const PLUGIN_ROOT = join(HERE, "..");

export function failure(errorCode, detail, extra = {}) {
  return {
    isError: true,
    errorCode,
    retryable: errorCode === "timeout" || errorCode === "upstream_unavailable",
    detail,
    ...extra,
  };
}

export async function loadPlugin(root = PLUGIN_ROOT) {
  const manifest = JSON.parse(await readFile(join(root, "plugin.json"), "utf8"));
  const aliases = JSON.parse(
    await readFile(join(root, manifest.aliases ?? "migrations/3.2.1-tool-aliases.json"), "utf8"),
  );
  return { manifest, aliases, root };
}

export function validateManifest(manifest) {
  const errors = [];
  if (manifest?.schemaVersion !== "3.0") errors.push("schemaVersion must be 3.0");
  if (manifest?.name !== "dsh-im2a-plugin") errors.push("name must be dsh-im2a-plugin");
  if (!/^\d+\.\d+\.\d+$/.test(manifest?.version ?? "")) errors.push("version must be semver");
  if (!manifest?.kernel?.module_version) errors.push("kernel.module_version is required");
  if (manifest?.kernel?.status !== "available") errors.push("kernel must be available");
  if (manifest?.premium?.license_implemented !== false) {
    errors.push("progress guard: premium.license_implemented must stay false until Relay ships");
  }

  const modules = manifest?.modules ?? {};
  for (const [id, mod] of Object.entries(modules)) {
    if (!MODULE_STATUS.has(mod.status)) errors.push(`${id}: unknown status ${mod.status}`);
    if (!Array.isArray(mod.deps)) errors.push(`${id}: deps must be an array`);
    if (!Array.isArray(mod.tools)) errors.push(`${id}: tools must be an array`);
    if (!LOADABLE.has(mod.status) && (mod.tools ?? []).length > 0) {
      errors.push(`${id}: ${mod.status} module must not declare tools`);
    }
    for (const dep of mod.deps ?? []) {
      if (!modules[dep]) errors.push(`${id}: unknown dependency ${dep}`);
    }
  }

  const premium = manifest?.premium?.modules ?? {};
  for (const [id, mod] of Object.entries(premium)) {
    if (mod.status !== "planned") errors.push(`pro/${id}: phase 1 must stay planned`);
  }

  // Kernel runtime modules are JS files the runtime actually loads, so the
  // manifest must describe them precisely: a path that escapes kernel/ would let
  // the manifest point anywhere.
  const runtimeModules = manifest?.kernel?.runtime_modules;
  if (!Array.isArray(runtimeModules) || runtimeModules.length === 0) {
    errors.push("kernel.runtime_modules must be a non-empty array");
  } else {
    const seen = new Set();
    for (const [index, entry] of runtimeModules.entries()) {
      const field = `kernel.runtime_modules[${index}]`;
      if (!entry || typeof entry.id !== "string" || entry.id.length === 0) errors.push(`${field}.id is required`);
      else if (seen.has(entry.id)) errors.push(`${field}: duplicate id ${entry.id}`);
      else seen.add(entry.id);
      if (typeof entry.path !== "string" || !entry.path.startsWith("kernel/") || entry.path.includes("..")) {
        errors.push(`${field}.path must stay inside kernel/`);
      }
      if (!MODULE_STATUS.has(entry.status)) errors.push(`${field}: unknown status ${entry.status}`);
    }

    // Every runtime module must be attached to a capability module, and no module
    // may reference a runtime module that was not declared. Without this the two
    // halves of the manifest can drift apart silently.
    const declaredRuntime = new Set(runtimeModules.map((entry) => entry.id));
    const claimed = new Map();
    for (const [id, mod] of Object.entries(modules)) {
      const refs = mod.runtime_modules ?? [];
      if (!Array.isArray(refs)) {
        errors.push(`${id}: runtime_modules must be an array`);
        continue;
      }
      if (new Set(refs).size !== refs.length) errors.push(`${id}: runtime_modules has duplicate entries`);
      for (const ref of refs) {
        if (!declaredRuntime.has(ref)) errors.push(`${id}: unknown runtime module ${ref}`);
        claimed.set(ref, (claimed.get(ref) ?? 0) + 1);
      }
    }
    for (const entry of runtimeModules) {
      if (!claimed.has(entry.id)) errors.push(`runtime module ${entry.id} is not attached to any module`);
    }
  }
  // The runtime block is the only callable surface. Without it the manifest
  // describes a design that nothing serves, which is exactly the drift this
  // field exists to prevent.
  const runtime = manifest?.runtime;
  if (!runtime || typeof runtime !== "object") {
    errors.push("runtime block is required: it declares the callable surface");
  } else {
    if (typeof runtime.transport !== "string" || runtime.transport.length === 0) errors.push("runtime.transport is required");
    if (typeof runtime.serverName !== "string" || runtime.serverName.length === 0) errors.push("runtime.serverName is required");
    if (!Array.isArray(runtime.tools) || runtime.tools.length === 0) {
      errors.push("runtime.tools must be a non-empty array");
    } else {
      const seenRuntime = new Set();
      for (const name of runtime.tools) {
        if (typeof name !== "string" || !RUNTIME_TOOL_RE.test(name)) {
          errors.push(`runtime.tools has an invalid tool name: ${name}`);
        } else if (seenRuntime.has(name)) {
          errors.push(`runtime.tools has a duplicate: ${name}`);
        } else {
          seenRuntime.add(name);
        }
      }
    }
  }

  // Declared meta tools that have no implementation must say so, otherwise the
  // manifest advertises a kernel surface nothing serves.
  const metaTools = manifest?.kernel?.meta_tools ?? [];
  if (Array.isArray(metaTools) && metaTools.length > 0) {
    const metaStatus = manifest?.kernel?.meta_tools_status;
    if (!MODULE_STATUS.has(metaStatus)) {
      errors.push("kernel.meta_tools_status is required when meta_tools are declared");
    } else if (metaStatus !== "available" && !manifest?.kernel?.meta_tools_note) {
      errors.push("kernel.meta_tools_note is required while meta_tools are not available");
    }
  }

  return { ok: errors.length === 0, errors };
}

/** Resolve each declared runtime module to its file and report whether it exists. */
export async function verifyRuntimeModules(manifest, root = PLUGIN_ROOT) {
  const entries = manifest?.kernel?.runtime_modules ?? [];
  const results = [];
  for (const entry of entries) {
    try {
      const handle = await stat(join(root, entry.path));
      results.push({ id: entry.id, path: entry.path, exists: handle.isFile(), status: entry.status });
    } catch (error) {
      results.push({ id: entry.id, path: entry.path, exists: false, status: entry.status, error: error.code });
    }
  }
  return results;
}

// Kahn over hard deps only; soft deps express ordering intent and never block.
export function computeLoadPlan(manifest) {
  const modules = manifest.modules ?? {};
  const ids = Object.keys(modules);
  const order = [];
  const errors = [];
  const pending = new Map(ids.map((id) => [id, new Set(modules[id].deps ?? [])]));

  while (pending.size) {
    const ready = ids.filter((id) => pending.has(id) && [...pending.get(id)].every((d) => !pending.has(d)));
    if (!ready.length) {
      errors.push(`dependency cycle among: ${[...pending.keys()].join(", ")}`);
      break;
    }
    const next = ready[0];
    order.push(next);
    pending.delete(next);
  }
  return { order, errors };
}

export function computeUnloadOrder(order) {
  return [...order].reverse();
}

// A planned hard dependency makes its dependants unavailable; it never disappears silently.
export function effectiveStatus(manifest, id, seen = new Set()) {
  const mod = manifest.modules?.[id];
  if (!mod) return "unavailable";
  if (!LOADABLE.has(mod.status)) return mod.status;
  for (const dep of mod.deps ?? []) {
    if (seen.has(dep)) continue;
    const s = effectiveStatus(manifest, dep, new Set([...seen, id]));
    if (!LOADABLE.has(s)) return "unavailable";
  }
  return mod.status;
}

export function createRegistry({ manifest, aliases, handlers = {} }) {
  const guard = validateManifest(manifest);
  if (!guard.ok) throw new Error(`invalid plugin.json: ${guard.errors.join("; ")}`);

  const plan = computeLoadPlan(manifest);
  const aliasByOld = new Map(aliases.entries.map((e) => [e.old_name, e]));
  const status = new Map(
    Object.keys(manifest.modules).map((id) => [id, effectiveStatus(manifest, id)]),
  );
  const disabled = new Set(
    Object.keys(manifest.modules).filter((id) => !LOADABLE.has(status.get(id))),
  );

  const runtimeNames = [...(manifest.runtime?.tools ?? [])];
  const runtimeSet = new Set(runtimeNames);
  const aliasByCanonical = new Map(aliases.entries.map((entry) => [entry.new_name, entry]));
  const moduleByLegacy = new Map(aliases.entries.map((entry) => [entry.old_name, entry.module]));
  // Bridge names that predate the alias table and therefore have no legacy entry.
  const RUNTIME_MODULE_HINTS = {
    im2a_kernel_status: "kernel",
    im2a_whoami: "kernel",
    governance_manage: "governance",
    im2a_harness_list: "fleet",
    im2a_send: "fleet",
    im2a_recv: "fleet",
    im2a_presence: "fleet",
    im2a_history: "fleet",
  };

  function moduleOf(name) {
    if (moduleByLegacy.has(name)) return moduleByLegacy.get(name);
    if (RUNTIME_MODULE_HINTS[name]) return RUNTIME_MODULE_HINTS[name];
    const match = /^im2a_([a-z][a-z0-9_]*?)_[a-z]/.exec(name);
    if (match && manifest.modules[match[1]]) return match[1];
    return "unmapped";
  }

  /**
   * Design surface: the canonical names the manifest describes. Some of them are
   * callable (they also appear in runtime.tools); most are not yet served.
   */
  function declaredTools() {
    const out = [];
    const push = (name, module, implemented = true) => {
      if (out.some((tool) => tool.name === name)) return;
      out.push({ name, module, canonical: true, callable: runtimeSet.has(name), implemented });
    };
    for (const id of plan.order) {
      for (const tool of manifest.modules[id].tools ?? []) push(`im2a_${id}_${tool}`, id);
    }
    for (const tool of manifest.kernel.tools ?? []) push(`im2a_kernel_${tool}`, "kernel");
    const metaStatus = manifest.kernel.meta_tools_status;
    for (const tool of manifest.kernel.meta_tools ?? []) {
      const name = `im2a_kernel_${tool}`;
      // A meta tool counts as implemented only if the runtime actually serves it
      // or the whole meta surface has shipped.
      push(name, "kernel", metaStatus === "available" || runtimeSet.has(name));
    }
    return out;
  }

  /** Callable surface: exactly the tools the MCP bridge serves, nothing more. */
  function runtimeTools() {
    return runtimeNames.map((name) => {
      const alias = aliasByOld.get(name);
      return {
        name,
        module: moduleOf(name),
        canonical: alias?.new_name ?? (aliasByCanonical.has(name) ? name : null),
        deprecated: Boolean(alias),
      };
    });
  }

  /**
   * The only list a caller may treat as "tools that exist". Design names that the
   * bridge does not serve are reported by toolGap(), never silently included here.
   */
  function publicTools() {
    return runtimeTools();
  }

  /** Where the design surface and the callable surface diverge. */
  function toolGap() {
    const declared = declaredTools().map((tool) => tool.name);
    const declaredSet = new Set(declared);
    return {
      declared: declared.length,
      runtime: runtimeNames.length,
      both: runtimeNames.filter((name) => declaredSet.has(name)),
      declared_only: declared.filter((name) => !runtimeSet.has(name)),
      runtime_only: runtimeNames.filter((name) => !declaredSet.has(name)),
    };
  }

  function resolve(name) {
    const alias = aliasByOld.get(name);
    const canonicalName = alias?.new_name ?? name;
    const runtimeName = runtimeSet.has(name) ? name : runtimeSet.has(canonicalName) ? canonicalName : null;

    if (runtimeName) {
      return {
        ok: true,
        name,
        runtimeName,
        module: moduleOf(runtimeName),
        tool: canonicalName,
        fixedArgs: alias?.fixed_args ?? {},
        deprecated: Boolean(alias),
        callable: true,
        handler: handlers[runtimeName] ?? handlers[canonicalName] ?? null,
      };
    }

    if (!RUNTIME_TOOL_RE.test(canonicalName)) return failure("unknown_tool", `invalid tool name ${name}`);

    // Declared by the manifest but not served by the bridge: say so precisely.
    const design = declaredTools().find((tool) => tool.name === canonicalName);
    if (design) {
      if (design.module !== "kernel" && disabled.has(design.module)) {
        const declaredStatus = status.get(design.module);
        if (declaredStatus === "planned") {
          return failure("not_implemented", `${design.module} is planned`, { module: design.module });
        }
        return failure("module_unavailable", `module ${design.module} is not loaded`, { module: design.module });
      }
      return failure(
        "not_wired",
        `${canonicalName} is declared by the manifest but ${manifest.runtime?.serverName ?? "the runtime"} does not serve it`,
        { module: design.module, runtime: manifest.runtime?.url ?? null },
      );
    }

    for (const id of [...plan.order, "kernel"]) {
      if (canonicalName.startsWith(`im2a_${id}_`)) {
        return failure("unknown_tool", `${canonicalName} is not declared by ${id}`, { module: id });
      }
    }
    return failure("unknown_tool", `no module owns ${name}`);
  }

  async function call(name, args = {}) {
    const target = resolve(name);
    if (!target.ok) return target;
    if (!target.handler) {
      return failure("not_wired", `${target.runtimeName} is served by the MCP bridge; no in-process handler is registered`, {
        module: target.module,
        runtimeName: target.runtimeName,
      });
    }
    const payload = { ...target.fixedArgs, ...args };
    try {
      return await target.handler(payload);
    } catch (error) {
      return failure("upstream_unavailable", error?.message ?? String(error), { module: target.module });
    }
  }

  function dependants(id) {
    return Object.entries(manifest.modules)
      .filter(([other, mod]) => other !== id && (mod.deps ?? []).includes(id))
      .map(([other]) => other);
  }

  function setModule(id, enabled) {
    if (!manifest.modules[id]) return failure("unknown_module", `${id} is not declared`);
    const deps = dependants(id).filter((d) => !disabled.has(d));
    if (!enabled && deps.length) {
      return failure("dependency_conflict", `cannot disable ${id}: required by ${deps.join(", ")}`, {
        module: id,
        dependants: deps,
      });
    }
    if (enabled && !LOADABLE.has(status.get(id))) {
      return failure("not_implemented", `${id} is ${status.get(id)} in ${manifest.version}`, { module: id });
    }
    if (enabled) disabled.delete(id); else disabled.add(id);
    return { ok: true, module: id, enabled };
  }

  function statusReport() {
    return {
      name: manifest.name,
      version: manifest.version,
      kernel: manifest.kernel.module_version,
      loadOrder: plan.order,
      modules: Object.keys(manifest.modules).map((id) => ({
        id,
        declared: manifest.modules[id].status,
        effective: status.get(id),
        enabled: !disabled.has(id),
        deps: manifest.modules[id].deps ?? [],
        softDeps: manifest.modules[id].soft_deps ?? [],
        tools: disabled.has(id) ? 0 : (manifest.modules[id].tools ?? []).length,
        runtimeModules: manifest.modules[id].runtime_modules ?? [],
        ...(manifest.modules[id].status_note ? { status_note: manifest.modules[id].status_note } : {}),
      })),
      premium: {
        license_implemented: false,
        relay_required: manifest.premium.relay_required,
        modules: Object.entries(manifest.premium.modules).map(([id, mod]) => ({ id, status: mod.status })),
      },
      runtimeModules: (manifest.kernel.runtime_modules ?? []).map((entry) => ({
        id: entry.id,
        layer: entry.layer ?? null,
        status: entry.status,
        path: entry.path,
        ...(entry.status_note ? { status_note: entry.status_note } : {}),
      })),
      surface: (() => {
        const gap = toolGap();
        return {
          transport: manifest.runtime?.transport ?? null,
          server: manifest.runtime?.serverName ?? null,
          declared: gap.declared,
          runtime: gap.runtime,
          callable_declared: gap.both,
          declared_only: gap.declared_only.length,
          runtime_only: gap.runtime_only.length,
        };
      })(),
    };
  }

  return {
    manifest,
    aliases,
    plan,
    status,
    publicTools,
    declaredTools,
    runtimeTools,
    toolGap,
    resolve,
    call,
    setModule,
    statusReport,
  };
}
