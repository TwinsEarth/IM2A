/**
 * IM2A Harness Adapter Contract (P0)
 *
 * Adapters are declarative planners first: detect and plan are read-only.
 * apply/rollback are the only mutating operations and must be atomic.
 * This file is a contract, not an implementation of the 12 adapters.
 */

export type HarnessId =
  | "dsh"
  | "workbuddy"
  | "doubao"
  | "qwen-code"
  | "mimo-code"
  | "codex"
  | "claude"
  | "gemini-cli"
  | "openclaw"
  | "muse"
  | "copilot"
  | "vscode";

export type AdapterTier = "T0" | "T1" | "T2";

export type AdapterStatus = "planned" | "observed" | "verified" | "blocked";

export type Transport = "stdio" | "streamable-http";

export interface DetectResult {
  installed: boolean;
  version: string | null;
  configPaths: string[];
  platform: string;
}

export interface PlanStep {
  action: "write" | "merge" | "backup" | "remove";
  path: string;
  preview: string;
}

export interface ApplyResult {
  changed: string[];
  backups: string[];
}

export interface VerifyResult {
  ok: boolean;
  checks: Array<{ name: string; ok: boolean; detail: string }>;
}

export interface HarnessAdapter {
  readonly id: HarnessId;
  readonly tier: AdapterTier;
  readonly status: AdapterStatus;
  readonly transports: Transport[];

  /** Read-only: never writes config or installs anything. */
  detect(): Promise<DetectResult>;

  /** Read-only: produces an atomic, reviewable change set. */
  plan(): Promise<PlanStep[]>;

  /** Mutating: all writes must be atomic and backed up. */
  apply(steps: PlanStep[]): Promise<ApplyResult>;

  /** Read-only: one real MCP tool call or an explicit unsupported result. */
  verify(): Promise<VerifyResult>;

  /** Mutating: restore every backup from apply. */
  rollback(result: ApplyResult): Promise<void>;
}
