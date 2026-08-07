import { apiRequest } from "@/shared/api/client";
import { createObjectDecoder, createValidatedDecoder, hasShape, isArrayOf, isBoolean, isNumber, isOptional, isRecordOf, isString } from "@/shared/api/decoder";

export type ResinQualityPublicCfg = {
  actionMode: string;
  streamWatchEnabled?: boolean;
  streamMaxAttempts?: number;
  platformId: string;
  hasProxyUrl: boolean;
};

export type ResinQualityEvent = {
  ts: number;
  event: string;
  reason?: string;
  classification?: string;
  outputTps?: number;
  exitIp?: string;
  cleared?: number;
  auditId?: string;
  source?: string;
  tokens?: number;
  attempts?: number;
};

export type ResinQualityStatus = {
  available: boolean;
  enabled: boolean;
  config?: ResinQualityPublicCfg;
  state?: {
    version: number;
    pool: {
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
    statistics: {
      passive?: Record<string, number>;
      active?: Record<string, number>;
      actions?: Record<string, number>;
      stream?: Record<string, number>;
    };
    events: ResinQualityEvent[];
    startedAt: number;
    updatedAt: number;
    lastStreamSignal?: string;
    lastStreamReason?: string;
    lastStreamAt?: number;
    lastStreamDegraded?: boolean;
    lastStreamAttempts?: number;
    streamMaxAttemptsOverride?: number;
    streamWatchOverride?: string;
  };
};

const numberRecord = isRecordOf(isNumber);

const configShape = hasShape({
  actionMode: isString,
  streamWatchEnabled: isOptional(isBoolean),
  streamMaxAttempts: isOptional(isNumber),
  platformId: isString,
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
  auditId: isOptional(isString),
  source: isOptional(isString),
  tokens: isOptional(isNumber),
  attempts: isOptional(isNumber),
});

const stateShape = hasShape({
  version: isNumber,
  pool: poolShape,
  statistics: hasShape({
    passive: isOptional(numberRecord),
    active: isOptional(numberRecord),
    actions: isOptional(numberRecord),
    stream: isOptional(numberRecord),
  }),
  events: isArrayOf(eventShape),
  startedAt: isNumber,
  updatedAt: isNumber,
  lastStreamSignal: isOptional(isString),
  lastStreamReason: isOptional(isString),
  lastStreamAt: isOptional(isNumber),
  lastStreamDegraded: isOptional(isBoolean),
  lastStreamAttempts: isOptional(isNumber),
  streamMaxAttemptsOverride: isOptional(isNumber),
  streamWatchOverride: isOptional(isString),
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

export async function updateResinQualityConfig(input: {
  streamMaxAttempts?: number;
  streamWatchEnabled?: boolean;
}): Promise<Record<string, unknown>> {
  return apiRequest("/api/admin/v1/resin-quality-guard/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  }, looseObjectDecoder);
}
