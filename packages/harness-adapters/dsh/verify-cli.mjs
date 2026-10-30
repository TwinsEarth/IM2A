import { writeFile } from "node:fs/promises";
import path from "node:path";
import { createDshAdapter } from "./adapter.mjs";

const args = process.argv.slice(2);
const get = (name) => {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : undefined;
};

const profileDir = get("--profile") ?? process.env.DSH_PROFILE_DIR;
const url = get("--url") ?? process.env.IM2A_HARNESS_URL ?? "http://127.0.0.1:18080";
const out = get("--out") ?? path.resolve(process.cwd(), "P0-EVIDENCE.json");

if (!profileDir) {
  console.error("usage: node verify-cli.mjs --profile <sandbox-profile> [--url <gateway>] [--out <file>]");
  process.exit(2);
}
if (!process.env.IM2A_HARNESS_TOKEN) {
  console.error("BLOCKED: IM2A_HARNESS_TOKEN is not set");
  process.exit(2);
}

const adapter = createDshAdapter({ harnessUrl: url });
const result = await adapter.verify({
  profileDir,
  env: { ...process.env, IM2A_HARNESS_URL: url },
});
await writeFile(out, JSON.stringify(result.evidence, null, 2) + "\n", { encoding: "utf8", mode: 0o600 });
console.log(JSON.stringify({
  ok: result.ok,
  status: result.status,
  code: result.code ?? null,
  transport: result.transport,
  harnessCount: result.harnessCount ?? null,
  evidence: out,
}, null, 2));
if (!result.ok) process.exit(1);
