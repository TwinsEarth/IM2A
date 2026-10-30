/**
 * Backend registry. Registration never implies enablement.
 *
 * `register(backend)` leaves the backend disabled; `enable(id)` is a separate
 * call. There is no default backend, no environment-variable switch, and no
 * "first registered backend wins" behavior. A missing backend fails closed.
 */

const REQUIRED_METHODS = ["capabilities", "estimate", "submit", "poll", "cancel", "collect", "verify"];
const KINDS = new Set(["local", "managed", "batch-api", "fake"]);
const entries = new Map();

export class BackendRegistryError extends Error {
  constructor(code, message) {
    super(`${code}: ${message}`);
    this.name = "BackendRegistryError";
    this.code = code;
  }
}

function assertBackendShape(backend) {
  if (!backend || typeof backend !== "object") throw new BackendRegistryError("BACKEND_INVALID", "backend is not an object");
  if (typeof backend.id !== "string" || backend.id.length === 0) {
    throw new BackendRegistryError("BACKEND_INVALID", "backend.id must be a non-empty string");
  }
  if (!KINDS.has(backend.kind)) throw new BackendRegistryError("BACKEND_INVALID", `unknown backend kind ${backend.kind}`);
  for (const method of REQUIRED_METHODS) {
    if (typeof backend[method] !== "function") {
      throw new BackendRegistryError("BACKEND_INVALID", `${backend.id} is missing ${method}()`);
    }
  }
}

export function register(backend, { enabled = false } = {}) {
  assertBackendShape(backend);
  if (entries.has(backend.id)) throw new BackendRegistryError("DUPLICATE_BACKEND", backend.id);
  entries.set(backend.id, { backend, enabled: enabled === true });
  return { id: backend.id, enabled: enabled === true };
}

export function enable(id) {
  const entry = entries.get(id);
  if (!entry) throw new BackendRegistryError("UNKNOWN_BACKEND", id);
  entry.enabled = true;
  return { id, enabled: true };
}

export function disable(id) {
  const entry = entries.get(id);
  if (!entry) throw new BackendRegistryError("UNKNOWN_BACKEND", id);
  entry.enabled = false;
  return { id, enabled: false };
}

export function list() {
  return [...entries.values()].map(({ backend, enabled }) => ({
    id: backend.id,
    kind: backend.kind,
    enabled,
  }));
}

export function listEnabled() {
  return list().filter((entry) => entry.enabled);
}

export function resolve(id) {
  const entry = entries.get(id);
  if (!entry) throw new BackendRegistryError("UNKNOWN_BACKEND", id);
  if (!entry.enabled) throw new BackendRegistryError("BACKEND_DISABLED", id);
  return entry.backend;
}
