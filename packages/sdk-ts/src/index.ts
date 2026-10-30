export const SDK_VERSION = "2.3.45";

export interface HealthzSnapshot {
  gateway: "pending" | "passed" | "failed" | "blocked";
  imBot: "pending" | "passed" | "failed" | "blocked";
  browser: "pending" | "passed" | "failed" | "blocked";
  wechat: "pending" | "passed" | "failed" | "blocked";
  openclaw: "pending" | "passed" | "failed" | "blocked";
  agent: "pending" | "passed" | "failed" | "blocked";
  ops: "pending" | "passed" | "failed" | "blocked";
}
