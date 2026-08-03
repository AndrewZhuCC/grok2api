import { apiRequest } from "@/shared/api/client";
import { createObjectDecoder, createValidatedDecoder, hasShape, isArrayOf, isBoolean, isNumber, isOptional, isRecordOf, isString } from "@/shared/api/decoder";

export type ResinQualityPublicCfg = {
  mode: string;
  actionMode: string;
  softTps: number;
  hardTps: number;
  consecutiveSoft: number;
  consecutiveErrors: number;
  quarantineSeconds: number;
  activeIntervalSeconds: number;
  passivePollSeconds: number;
  failClosed: boolean;
  platformId: string;
  canProbe: boolean;
  hasProxyUrl: boolean;
};

export type ResinQualityPoolState = {
  softStrikes: number;
  errorStrikes: number;
  quarantinedUntil: number;
  quarantineActive: boolean;
  lastReason: string;
  lastClassification: string;
  lastOutputTps: number;
  lastExitIp: string;
  suspectExitIps: string[];
  lastObservedAt: number;
  lastProbeAt: number;
};

export type ResinQualityEvent = {
  ts: number;
  event: string;
  reason?: string;
  classification?: string;
  outputTps?: number;
  exitIp?: string;
  cleared?: number;
};

export type ResinQualityStatus = {
  available: boolean;
  enabled: boolean;
  config?: ResinQualityPublicCfg;
  state?: {
    version: number;
    pool: ResinQualityPoolState;
    statistics: {
      passive: Record<string, number>;
      active: Record<string, number>;
      actions: Record<string, number>;
    };
    events: ResinQualityEvent[];
    lastPassivePollAt: number;
    lastActiveCycleAt: number;
    startedAt: number;
    updatedAt: number;
  };
};

const numberRecord = isRecordOf(isNumber);

const configShape = hasShape({
  mode: isString,
  actionMode: isString,
  softTps: isNumber,
  hardTps: isNumber,
  consecutiveSoft: isNumber,
  consecutiveErrors: isNumber,
  quarantineSeconds: isNumber,
  activeIntervalSeconds: isNumber,
  passivePollSeconds: isNumber,
  failClosed: isBoolean,
  platformId: isString,
  canProbe: isBoolean,
  hasProxyUrl: isBoolean,
});

const poolShape = hasShape({
  softStrikes: isNumber,
  errorStrikes: isNumber,
  quarantinedUntil: isNumber,
  quarantineActive: isBoolean,
  lastReason: isString,
  lastClassification: isString,
  lastOutputTps: isNumber,
  lastExitIp: isString,
  suspectExitIps: isArrayOf(isString),
  lastObservedAt: isNumber,
  lastProbeAt: isNumber,
});

const eventShape = hasShape({
  ts: isNumber,
  event: isString,
  reason: isOptional(isString),
  classification: isOptional(isString),
  outputTps: isOptional(isNumber),
  exitIp: isOptional(isString),
  cleared: isOptional(isNumber),
});

const stateShape = hasShape({
  version: isNumber,
  pool: poolShape,
  statistics: hasShape({
    passive: numberRecord,
    active: numberRecord,
    actions: numberRecord,
  }),
  events: isArrayOf(eventShape),
  lastPassivePollAt: isNumber,
  lastActiveCycleAt: isNumber,
  startedAt: isNumber,
  updatedAt: isNumber,
});

const statusDecoder = createObjectDecoder<ResinQualityStatus>("resin quality status", {
  available: isBoolean,
  enabled: isBoolean,
  config: isOptional(configShape),
  state: isOptional(stateShape),
});

const looseObjectDecoder = createValidatedDecoder<Record<string, unknown>>("loose object", (value) => typeof value === "object" && value !== null);

export async function getResinQualityStatus(): Promise<ResinQualityStatus> {
  return apiRequest("/api/admin/v1/resin-quality-guard", { method: "GET" }, statusDecoder);
}

export async function postResinQualityReshuffle(): Promise<Record<string, unknown>> {
  return apiRequest("/api/admin/v1/resin-quality-guard/reshuffle", { method: "POST" }, looseObjectDecoder);
}

export async function postResinQualityProbe(): Promise<Record<string, unknown>> {
  return apiRequest("/api/admin/v1/resin-quality-guard/probe", { method: "POST" }, looseObjectDecoder);
}
