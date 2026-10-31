/**
 * v2.6.0 durable drive declarations.
 *
 * A drive is a local behavior description, not a scheduled action. The
 * description is a filter condition that curator_audit / memory_recall_v2
 * output can satisfy; trigger_hint is a hint for future scheduling only.
 */
import { mkdir, readFile, readdir, rename, writeFile } from "node:fs/promises";
import path from "node:path";

export const DRIVE_SCHEMA_VERSION = "1.0";
export const DRIVE_SCOPE = "self_owned_skills";
export const DRIVE_ID_RE = /^[a-z][a-z0-9_]{2,63}$/;
const ALLOWED_KEYS = new Set(["drive_id", "description", "priority", "trigger_hint", "scope"]);

export class DriveError extends Error {
  constructor(code, message = code) {
    super(message);
    this.name = "DriveError";
    this.code = code;
  }
}

function scalar(raw) {
  const value = raw.trim();
  if (value.startsWith("\"")) {
    try { return JSON.parse(value); }
    catch { throw new DriveError("DRIVE_INVALID", "invalid quoted frontmatter value"); }
  }
  if (value.startsWith("'") && value.endsWith("'")) return value.slice(1, -1);
  return value;
}

export function parseDrive(text, { file = "drive.md" } = {}) {
  if (typeof text !== "string") throw new DriveError("DRIVE_INVALID", "drive must be text");
  const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?([\s\S]*)$/.exec(text);
  if (!match) throw new DriveError("DRIVE_INVALID", "drive frontmatter is required");
  const fields = {};
  for (const line of match[1].split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith("#")) continue;
    const split = trimmed.indexOf(":");
    if (split <= 0) throw new DriveError("DRIVE_INVALID", `invalid frontmatter line: ${trimmed}`);
    const key = trimmed.slice(0, split).trim();
    if (!ALLOWED_KEYS.has(key)) throw new DriveError("DRIVE_INVALID", `unknown frontmatter key: ${key}`);
    if (Object.hasOwn(fields, key)) throw new DriveError("DRIVE_INVALID", `duplicate frontmatter key: ${key}`);
    fields[key] = scalar(trimmed.slice(split + 1));
  }
  const driveId = String(fields.drive_id ?? "").trim();
  if (!DRIVE_ID_RE.test(driveId)) throw new DriveError("DRIVE_INVALID", "drive_id must match ^[a-z][a-z0-9_]{2,63}$");
  const description = String(fields.description ?? "").trim();
  if (!description || description.length > 500) throw new DriveError("DRIVE_INVALID", "description is required (max 500)");
  const triggerHint = String(fields.trigger_hint ?? "").trim();
  if (!triggerHint || triggerHint.length > 500) throw new DriveError("DRIVE_INVALID", "trigger_hint is required (max 500)");
  const priority = Number(fields.priority);
  if (!Number.isFinite(priority) || priority < 0 || priority > 1) throw new DriveError("DRIVE_INVALID", "priority must be 0..1");
  if (fields.scope !== DRIVE_SCOPE) throw new DriveError("DRIVE_INVALID", `scope must be ${DRIVE_SCOPE}`);
  return {
    schema_version: DRIVE_SCHEMA_VERSION,
    drive_id: driveId,
    description,
    priority,
    trigger_hint: triggerHint,
    scope: DRIVE_SCOPE,
    body: match[2].trim(),
    file,
  };
}

export async function loadDrive(filePath) {
  let text;
  try { text = await readFile(filePath, "utf8"); }
  catch (error) {
    if (error.code === "ENOENT") throw new DriveError("DRIVE_NOT_FOUND");
    throw error;
  }
  return parseDrive(text, { file: path.basename(filePath) });
}

export async function loadDrives(dir) {
  let names;
  try { names = await readdir(dir); }
  catch (error) {
    if (error.code === "ENOENT") return [];
    throw error;
  }
  const drives = [];
  for (const name of names.filter((entry) => entry.endsWith(".md")).sort()) {
    const drive = await loadDrive(path.join(dir, name));
    if (drives.some((item) => item.drive_id === drive.drive_id)) {
      throw new DriveError("DRIVE_DUPLICATE", `duplicate drive_id: ${drive.drive_id}`);
    }
    drives.push(drive);
  }
  return drives;
}

function frontmatterValue(value) {
  return JSON.stringify(String(value));
}

export function renderDrive(declaration) {
  const drive = parseDrive(`---\ndrive_id: ${frontmatterValue(declaration.drive_id)}\ndescription: ${frontmatterValue(declaration.description)}\npriority: ${Number(declaration.priority)}\ntrigger_hint: ${frontmatterValue(declaration.trigger_hint)}\nscope: ${DRIVE_SCOPE}\n---\n${declaration.body ?? ""}`);
  const body = drive.body || `# ${drive.drive_id}\n\n${drive.description}\n`;
  return `---\ndrive_id: ${frontmatterValue(drive.drive_id)}\ndescription: ${frontmatterValue(drive.description)}\npriority: ${drive.priority}\ntrigger_hint: ${frontmatterValue(drive.trigger_hint)}\nscope: ${DRIVE_SCOPE}\n---\n${body}`;
}

export async function writeDrive(dir, declaration) {
  if (!DRIVE_ID_RE.test(String(declaration.drive_id ?? ""))) throw new DriveError("DRIVE_INVALID", "drive_id is invalid");
  const filePath = path.join(dir, `${declaration.drive_id}.md`);
  await mkdir(dir, { recursive: true, mode: 0o750 });
  const content = renderDrive(declaration);
  const temp = `${filePath}.tmp-${process.pid}-${Date.now()}`;
  await writeFile(temp, content, { encoding: "utf8", mode: 0o640 });
  await rename(temp, filePath);
  return loadDrive(filePath);
}
