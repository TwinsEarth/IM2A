// IM2A v3.2.1 kernel registry: one manifest, one bus, one gate.
// Phase 1 scope: topology, lifecycle, alias resolution, honest module status.
// No business handlers live here; modules forward to the existing bridge.

import { readFile } from "node:fs/promises";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

export const MODULE_STATUS = new Set(["available", "partial", "planned", "unavailable"]);
export const TOOL_NAME_RE = /^im2a_[a-z][a-z0-9_]{2,40}$/;
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
  if (manifest?.name !== "dsh-im2a") errors.push("name must be dsh-im2a");
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
  return { ok: errors.length === 0, errors };
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

  function publicTools() {
    const out = [];
    for (const id of plan.order) {
      if (disabled.has(id)) continue;
      for (const tool of manifest.modules[id].tools ?? []) {
        out.push({ name: `im2a_${id}_${tool}`, module: id, canonical: true });
      }
    }
    for (const tool of manifest.kernel.tools ?? []) out.push({ name: `im2a_kernel_${tool}`, module: "kernel", canonical: true });
    for (const tool of manifest.kernel.meta_tools ?? []) out.push({ name: `im2a_kernel_${tool}`, module: "kernel", canonical: true });
    const seen = new Set(out.map((t) => t.name));
    for (const entry of aliases.entries) {
      if (seen.has(entry.old_name)) continue;
      if (disabled.has(entry.module)) continue;
      out.push({ name: entry.old_name, module: entry.module, canonical: false, replaced_by: entry.new_name });
    }
    return out;
  }

  function resolve(name) {
    const alias = aliasByOld.get(name);
    const canonicalName = alias?.new_name ?? name;
    if (!TOOL_NAME_RE.test(canonicalName)) return failure("unknown_tool", `invalid tool name ${name}`);

    for (const id of [...plan.order, "kernel"]) {
      const prefix = `im2a_${id}_`;
      if (!canonicalName.startsWith(prefix)) continue;
      if (id !== "kernel" && disabled.has(id)) {
        const declared = status.get(id);
        if (declared === "planned") {
          return failure("not_implemented", `${id} is planned in 3.2.1`, { module: id });
        }
        return failure("module_unavailable", `module ${id} is not loaded`, { module: id });
      }
      const tool = canonicalName.slice(prefix.length);
      const declared = id === "kernel"
        ? [...(manifest.kernel.tools ?? []), ...(manifest.kernel.meta_tools ?? [])]
        : manifest.modules[id].tools ?? [];
      if (!declared.includes(tool)) {
        return failure("unknown_tool", `${canonicalName} is not declared by ${id}`, { module: id });
      }
      return {
        ok: true,
        name: canonicalName,
        module: id,
        tool,
        fixedArgs: alias?.fixed_args ?? {},
        deprecated: Boolean(alias),
        handler: handlers[canonicalName] ?? handlers[`${id}.${tool}`] ?? null,
      };
    }
    return failure("unknown_tool", `no module owns ${name}`);
  }

  async function call(name, args = {}) {
    const target = resolve(name);
    if (!target.ok) return target;
    if (!target.handler) {
      return failure("not_implemented", `${target.name} has no handler in 3.2.1`, { module: target.module });
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
      return failure("not_implemented", `${id} is ${status.get(id)} in 3.2.1`, { module: id });
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
      })),
      premium: {
        license_implemented: false,
        relay_required: manifest.premium.relay_required,
        modules: Object.entries(manifest.premium.modules).map(([id, mod]) => ({ id, status: mod.status })),
      },
    };
  }

  return { manifest, aliases, plan, status, publicTools, resolve, call, setModule, statusReport };
}
