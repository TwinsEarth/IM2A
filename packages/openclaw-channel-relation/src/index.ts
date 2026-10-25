// SPDX-License-Identifier: MIT
//
// IM2A Relation · OpenClaw 频道插件入口（运行时实现）。
//
// 定位：IM2A 的**平台版本之一**（edition），不是产品替代。三样能力（关系档位门控 /
// 付费注意力状态机 / 三态 UI）的规则以 IM2A core 为准，本包只做可分发封装；插件版本
// 独立演进（0.1.0），core 版本单一源在 internal/version/version.go。
//
// 能力边界（诚实声明）：
//   - 本 edition 不实现外部 IM 传输（它不是"又一个聊天通道"）；channel 壳用于注册
//     配置/状态/出站治理回调，真正的消息收发由 OpenClaw 宿主通道负责；
//   - 关系判定与付费注意力是纯函数/纯内存状态机，不依赖网络，不提供进程隔离。

import {
  PERMISSION_MATRIX,
  SCOPES,
  TRUST_LEVEL_NAMES,
  check,
  decide,
  permissionsFor,
} from "./relation.ts";
import type { CallContext, Capability, Decision, ReasonCode } from "./relation.ts";
import {
  ATTENTION_LEVEL_NAMES,
  PAID_TRANSITIONS,
  PaidAttentionError,
  PaidAttentionMachine,
} from "./paid-attention.ts";
import type {
  AttentionLevel,
  BillingMode,
  DecisionStatus,
  LedgerStatus,
  PaidMessageStatus,
  ReceiverPolicy,
  SendInput,
  SendResult,
} from "./paid-attention.ts";
import {
  UI_STATES,
  UI_STATE_LABELS,
  UI_TRANSITIONS,
  UiTransitionError,
  buildApprovalView,
  buildConversationView,
  buildOverviewView,
  initialUiState,
  switchState,
} from "./ui.ts";
import type { UiEvent, UiState } from "./ui.ts";

// ---- edition 元数据（与 package.json / edition.json 同步；edition.test.mjs 守护漂移） ----

export const EDITION = "openclaw-channel-relation";
export const PLUGIN_VERSION = "0.1.0";
export const COMPATIBLE_CORE = ">=2.0.2 <2.1.0";
export const CHANNEL_ID = "im2a-relation";
export const PLUGIN_ID = CHANNEL_ID;
export const EDITION_CAPABILITIES = [
  "relation-gating",
  "paid-attention-state-machine",
  "three-state-ui",
] as const;

// ---- OpenClaw 宿主最小接口（结构性类型；不依赖 openclaw 包，保持零 runtime 依赖） ----

export interface OpenClawChannelPluginApi {
  registrationMode?: string;
  runtime?: { version?: string };
  registerChannel(ctx: { plugin: unknown }): void;
  logger?: { info?(msg: string): void; warn?(msg: string): void };
}

/** channelConfigs 的 JSON Schema（与 openclaw.plugin.json 内 schema 保持一致，测试守护）。 */
export const CHANNEL_CONFIG_SCHEMA = {
  type: "object",
  additionalProperties: true,
  properties: {
    defaultTrustLevel: {
      type: "integer",
      minimum: 0,
      maximum: 4,
      default: 4,
      description: "Default trust level for unknown senders (0=self .. 4=stranger).",
    },
    minPriceCents: {
      type: "integer",
      minimum: 0,
      default: 0,
      description: "Minimum price (cents) a sender must offer to reach this user.",
    },
    autoRejectAboveCents: {
      type: "integer",
      minimum: 0,
      default: 0,
      description: "Auto-reject offers >= this amount (anti price-harassment); 0 = disabled.",
    },
    blockedSenders: {
      type: "array",
      items: { type: "string" },
      default: [],
      description: "Sender ids blocked from paid messages.",
    },
    transportUrl: {
      type: "string",
      description: "Optional IM2A core webhook base URL for outbound governance notifications.",
    },
    defaultUiState: {
      type: "string",
      enum: ["approval", "overview", "conversation"],
      default: "approval",
      description: "Initial three-state UI view.",
    },
  },
} as const;

export const CHANNEL_META = {
  id: CHANNEL_ID,
  aliases: ["im2a-openclaw-channel-relation"],
  label: "IM2A Relation",
  selectionLabel: "IM2A Relation (trust & paid attention)",
  docsPath: "https://github.com/TwinsEarth/im2agent#readme",
  docsLabel: "IM2A Relation",
  blurb: "关系档位门控 + 付费注意力 + 三态审批（IM2A 平台版本）",
  order: 300,
} as const;

interface ChannelAccountConfig {
  defaultTrustLevel?: number;
  minPriceCents?: number;
  autoRejectAboveCents?: number;
  blockedSenders?: string[];
  transportUrl?: string;
  defaultUiState?: string;
}

function readChannelConfig(config: unknown): ChannelAccountConfig {
  const cfg = config as { channels?: Record<string, ChannelAccountConfig> } | undefined;
  return cfg?.channels?.[CHANNEL_ID] ?? {};
}

/** OpenClaw channel 插件对象（形状对照已安装 openclaw-weixin：id/meta/configSchema/capabilities/config/outbound/status/reload）。 */
export const im2aRelationChannel = {
  id: CHANNEL_ID,
  meta: CHANNEL_META,
  configSchema: CHANNEL_CONFIG_SCHEMA,
  capabilities: {
    chatTypes: ["direct"] as const,
    media: false,
    blockStreaming: false,
  },
  agentPrompt: {
    messageToolHints: () => [
      "IM2A Relation 的三态：审批态（approval）/ 巡视态（overview）/ 会话态（conversation）；未经审批的动作不要伪装成已批准。",
      "陌生人的付费消息必须走 paid-attention 状态机：先 compliance 预审，再按 billingMode 1|2 处理；重复 messageId 不得二次扣费。",
      "关系门控由 decide() 五步判定（skill 白名单 → 档位下限 → 领域矩阵 → 金额阈值 → 预算），不得用模型判断替代。",
    ],
  },
  reload: { configPrefixes: [`channels.${CHANNEL_ID}`] },
  config: {
    listAccountIds: (_config: unknown): string[] => ["default"],
    resolveAccount: (config: unknown, accountId?: string) => {
      const cfg = readChannelConfig(config);
      return {
        accountId: accountId ?? "default",
        enabled: true,
        configured: true,
        defaultTrustLevel: cfg.defaultTrustLevel ?? 4,
        defaultUiState: initialUiState(cfg.defaultUiState),
        transportUrl: cfg.transportUrl,
      };
    },
    isEnabled: (_account: unknown): boolean => true,
    isConfigured: (account: { configured?: boolean } | undefined): boolean => account?.configured !== false,
    describeAccount: (account: { accountId?: string; enabled?: boolean; configured?: boolean } | undefined) => ({
      accountId: account?.accountId ?? "default",
      name: "IM2A Relation",
      enabled: account?.enabled ?? true,
      configured: account?.configured ?? true,
    }),
  },
  status: {
    describe: (account: { accountId?: string } | undefined) => ({
      accountId: account?.accountId ?? "default",
      edition: EDITION,
      version: PLUGIN_VERSION,
      compatibleCore: COMPATIBLE_CORE,
      capabilities: EDITION_CAPABILITIES,
    }),
  },
  outbound: {
    /**
     * 出站治理通知：把决策/卡片事件投递到 IM2A core（transportUrl）。
     * 本 edition 不直接连接外部 IM；未配置 transportUrl 时 fail-closed（显式报错，不静默吞）。
     */
    sendText: async (
      ctx: { to: string; text: string; accountId?: string },
      config?: unknown,
    ): Promise<{ ok: true; delivered: false; forwardedTo: string }> => {
      const cfg = readChannelConfig(config);
      if (!cfg.transportUrl) {
        throw new Error(
          "im2a-relation: transportUrl not configured; this edition forwards governance events to IM2A core instead of owning an external IM transport",
        );
      }
      const res = await fetch(cfg.transportUrl, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({
          channel: CHANNEL_ID,
          to: ctx.to,
          text: ctx.text,
          accountId: ctx.accountId ?? "default",
          edition: EDITION,
          version: PLUGIN_VERSION,
        }),
      });
      if (!res.ok) {
        throw new Error(`im2a-relation: transport ${cfg.transportUrl} returned ${res.status}`);
      }
      return { ok: true, delivered: false, forwardedTo: cfg.transportUrl };
    },
  },
} as const;

const PLUGIN = {
  id: CHANNEL_ID,
  name: "IM2A Relation",
  description:
    "IM2A platform edition for OpenClaw: relation trust gating, paid-attention state machine, and three-state approval UI.",
  configSchema: CHANNEL_CONFIG_SCHEMA,
  register(api: OpenClawChannelPluginApi): void {
    // 宿主在 CLI 元数据收集模式下不执行运行时副作用（对照 openclaw-weixin index.ts）。
    if (api.registrationMode === "cli-metadata") return;
    if (!api || typeof api.registerChannel !== "function") {
      throw new Error("im2a-relation: incompatible OpenClaw plugin api (registerChannel missing)");
    }
    api.registerChannel({ plugin: im2aRelationChannel });
  },
};

export default PLUGIN;

export {
  // 关系门控
  PERMISSION_MATRIX,
  SCOPES,
  TRUST_LEVEL_NAMES,
  check,
  decide,
  permissionsFor,
  // 付费注意力
  ATTENTION_LEVEL_NAMES,
  PAID_TRANSITIONS,
  PaidAttentionError,
  PaidAttentionMachine,
  // 三态 UI
  UI_STATES,
  UI_STATE_LABELS,
  UI_TRANSITIONS,
  UiTransitionError,
  buildApprovalView,
  buildConversationView,
  buildOverviewView,
  initialUiState,
  switchState,
};

export type {
  AttentionLevel,
  BillingMode,
  CallContext,
  Capability,
  Decision,
  DecisionStatus,
  LedgerStatus,
  PaidMessageStatus,
  ReasonCode,
  ReceiverPolicy,
  SendInput,
  SendResult,
  UiEvent,
  UiState,
};
