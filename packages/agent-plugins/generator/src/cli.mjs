import path from "node:path";
import { fileURLToPath } from "node:url";
import { generate } from "./generate.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "..", "..", "..", "..");
const command = process.argv[2];

if (command !== "generate" && command !== "check") {
  console.error("usage: node .../cli.mjs <generate|check>");
  process.exit(2);
}

const result = await generate({ root, check: command === "check" });
console.log(JSON.stringify(result, null, 2));
