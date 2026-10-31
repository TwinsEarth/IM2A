/**
 * Configuration layer for reflect_trigger (v2.5.5).
 *
 * The agent loop must not own "worth reflecting" thresholds. They live here,
 * are loaded per call, and may be overridden with IM2A_REFLECT_CONFIG as an
 * inline JSON object or a path to a JSON file.
 */
import { readFileSync } from "node:fs";

const DEFAULT_CONFIG = {
  schemaVersion: "1.0",
  minToolCalls: 5,
  minUniqueSpans: 2,
  complexityFloor: 0.25,
  maxCandidates: 3,
  requireSuccess: false,
  enableFailureRecovery: true,
  enableCoordination: true,
  weights: {
    toolCalls: 0.35,
    uniqueSpans: 0.15,
    failureRecovery: 0.2,
    coordination: 0.2,
    tokenCost: 0.1,
  },
  confidence: {
    base: 0.45,
    toolCalls: 0.03,
    uniqueSpans: 0.05,
    failureRecovery: 0.08,
    coordination: 0.08,
    max: 0.9,
  },
};

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

function finiteNumber(value, field, min, max) {
  if (typeof value !== "number" || !Number.isFinite(value) || value < min || value > max) {
    throw new Error(`${field} must be a finite number in [${min}, ${max}]`);
  }
  return value;
}

function integer(value, field, min, max) {
  if (!Number.isInteger(value) || value < min || value > max) {
    throw new Error(`${field} must be an integer in [${min}, ${max}]`);
  }
  return value;
}

function boolean(value, field) {
  if (typeof value !== "boolean") throw new Error(`${field} must be boolean`);
  return value;
}

export function validateReflectConfig(input) {
  if (!input || typeof input !== "object" || Array.isArray(input)) throw new Error("reflect config must be an object");
  const weights = input.weights ?? {};
  const confidence = input.confidence ?? {};
  const config = {
    schemaVersion: String(input.schemaVersion ?? "1.0"),
    minToolCalls: integer(input.minToolCalls, "minToolCalls", 1, 100),
    minUniqueSpans: integer(input.minUniqueSpans, "minUniqueSpans", 1, 50),
    complexityFloor: finiteNumber(input.complexityFloor, "complexityFloor", 0, 1),
    maxCandidates: integer(input.maxCandidates, "maxCandidates", 1, 10),
    requireSuccess: boolean(input.requireSuccess, "requireSuccess"),
    enableFailureRecovery: boolean(input.enableFailureRecovery, "enableFailureRecovery"),
    enableCoordination: boolean(input.enableCoordination, "enableCoordination"),
    weights: {
      toolCalls: finiteNumber(weights.toolCalls, "weights.toolCalls", 0, 1),
      uniqueSpans: finiteNumber(weights.uniqueSpans, "weights.uniqueSpans", 0, 1),
      failureRecovery: finiteNumber(weights.failureRecovery, "weights.failureRecovery", 0, 1),
      coordination: finiteNumber(weights.coordination, "weights.coordination", 0, 1),
      tokenCost: finiteNumber(weights.tokenCost, "weights.tokenCost", 0, 1),
    },
    confidence: {
      base: finiteNumber(confidence.base, "confidence.base", 0, 1),
      toolCalls: finiteNumber(confidence.toolCalls, "confidence.toolCalls", 0, 1),
      uniqueSpans: finiteNumber(confidence.uniqueSpans, "confidence.uniqueSpans", 0, 1),
      failureRecovery: finiteNumber(confidence.failureRecovery, "confidence.failureRecovery", 0, 1),
      coordination: finiteNumber(confidence.coordination, "confidence.coordination", 0, 1),
      max: finiteNumber(confidence.max, "confidence.max", 0, 1),
    },
  };
  const weightSum = Object.values(config.weights).reduce((sum, value) => sum + value, 0);
  if (Math.abs(weightSum - 1) > 0.001) throw new Error("reflect weights must sum to 1");
  if (config.confidence.max < config.confidence.base) throw new Error("confidence.max must be >= confidence.base");
  return config;
}

export function mergeReflectConfig(overrides = {}) {
  const merged = {
    ...DEFAULT_CONFIG,
    ...overrides,
    weights: { ...DEFAULT_CONFIG.weights, ...(overrides.weights ?? {}) },
    confidence: { ...DEFAULT_CONFIG.confidence, ...(overrides.confidence ?? {}) },
  };
  return validateReflectConfig(merged);
}

export function loadReflectConfig({ env = process.env, readFile = readFileSync } = {}) {
  const source = env.IM2A_REFLECT_CONFIG;
  if (!source || source.trim() === "") return clone(DEFAULT_CONFIG);
  const text = source.trim().startsWith("{") ? source : readFile(source, "utf8");
  let overrides;
  try {
    overrides = JSON.parse(text);
  } catch (error) {
    throw new Error(`IM2A_REFLECT_CONFIG is not valid JSON: ${error.message}`);
  }
  return mergeReflectConfig(overrides);
}

export const DEFAULT_REFLECT_CONFIG = Object.freeze(clone(DEFAULT_CONFIG));
