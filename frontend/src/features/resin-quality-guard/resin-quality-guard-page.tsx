import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { KeyRound, RefreshCw, ShieldCheck, Shuffle, Zap } from "lucide-react";
import type { ReactNode } from "react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import {
  getResinQualityStatus,
  postResinQualityProbe,
  postResinQualityReshuffle,
  setResinQualityProbeKey,
} from "@/features/resin-quality-guard/resin-quality-guard-api";
import { ErrorState } from "@/shared/components/data-state";
import { PageHeader } from "@/shared/components/page-header";

function formatTs(ts?: number): string {
  if (!ts) return "—";
  try {
    return new Date(ts * 1000).toLocaleString();
  } catch {
    return "—";
  }
}

export function ResinQualityGuardPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [localKeyId, setLocalKeyId] = useState<string | null>(null);

  const statusQuery = useQuery({
    queryKey: ["resin-quality-guard"],
    queryFn: getResinQualityStatus,
    refetchInterval: 5_000,
  });

  const probeKeys = statusQuery.data?.probeKeys ?? [];
  const serverSelected = statusQuery.data?.selectedProbeKeyId ?? "";
  const effectiveKeyId = statusQuery.data?.effectiveProbeKeyId ?? "";
  const selectValue = useMemo(() => {
    if (localKeyId !== null) return localKeyId || "auto";
    return serverSelected || "auto";
  }, [localKeyId, serverSelected]);

  const keyMutation = useMutation({
    mutationFn: (keyId: string) => setResinQualityProbeKey(keyId === "auto" ? "auto" : keyId),
    onSuccess: async () => {
      setLocalKeyId(null);
      await queryClient.invalidateQueries({ queryKey: ["resin-quality-guard"] });
      toast.success(t("resinQualityGuard.keySaved"));
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("resinQualityGuard.actionFailed")),
  });

  const reshuffleMutation = useMutation({
    mutationFn: postResinQualityReshuffle,
    onMutate: () => toast.loading(t("resinQualityGuard.reshuffling"), { id: "resin-qg" }),
    onSuccess: (data) => {
      toast.success(t("resinQualityGuard.reshuffleDone", { cleared: String(data.cleared ?? 0) }), { id: "resin-qg" });
      void queryClient.invalidateQueries({ queryKey: ["resin-quality-guard"] });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("resinQualityGuard.actionFailed"), { id: "resin-qg" }),
  });

  const probeMutation = useMutation({
    mutationFn: postResinQualityProbe,
    onMutate: () => toast.loading(t("resinQualityGuard.probing"), { id: "resin-qg" }),
    onSuccess: (data) => {
      const tps = typeof data.outputTokensPerSecond === "number" ? data.outputTokensPerSecond.toFixed(1) : "?";
      toast.success(t("resinQualityGuard.probeDone", { tps }), { id: "resin-qg" });
      void queryClient.invalidateQueries({ queryKey: ["resin-quality-guard"] });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : t("resinQualityGuard.actionFailed"), { id: "resin-qg" }),
  });

  if (statusQuery.isLoading) {
    return (
      <div className="flex min-h-[40vh] items-center justify-center">
        <Spinner className="size-5" />
      </div>
    );
  }
  if (statusQuery.isError) {
    return <ErrorState message={statusQuery.error instanceof Error ? statusQuery.error.message : t("errors.generic")} onRetry={() => void statusQuery.refetch()} />;
  }

  const status = statusQuery.data;
  const pool = status?.state?.pool;
  const stats = status?.state?.statistics;
  const events = status?.state?.events ?? [];
  const cfg = status?.config;
  // Auto mode needs at least one listed key; env-only mode uses canProbe without listing secrets.
  const canProbe = Boolean(
    status?.available && (probeKeys.length > 0 || (cfg?.canProbe && cfg?.autoSelectKey === false)),
  );

  return (
    <div className="space-y-6">
      <PageHeader
        title={t("resinQualityGuard.title")}
        description={t("resinQualityGuard.subtitle")}
        actions={
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" size="sm" onClick={() => void statusQuery.refetch()}>
              <RefreshCw className="size-4" />
              {t("common.refresh")}
            </Button>
            <Button variant="outline" size="sm" disabled={!canProbe || probeMutation.isPending} onClick={() => probeMutation.mutate()}>
              <Zap className="size-4" />
              {t("resinQualityGuard.probe")}
            </Button>
            <Button variant="destructive" size="sm" disabled={!status?.available || reshuffleMutation.isPending} onClick={() => reshuffleMutation.mutate()}>
              <Shuffle className="size-4" />
              {t("resinQualityGuard.reshuffle")}
            </Button>
          </div>
        }
      />

      {!status?.available ? (
        <div className="rounded-xl border border-dashed p-6 text-sm text-muted-foreground">{t("resinQualityGuard.disabledHint")}</div>
      ) : (
        <>
          <aside className="flex shrink-0 flex-col gap-2 rounded-lg bg-secondary/45 px-4 py-2.5 text-xs leading-5 text-muted-foreground sm:flex-row sm:items-center sm:justify-between sm:gap-4">
            <div className="flex min-w-0 items-center gap-3">
              <KeyRound className="size-4 shrink-0" />
              <span>{t("resinQualityGuard.probeKeyHint")}</span>
            </div>
            <div className="flex min-w-[14rem] items-center gap-2">
              <Select
                value={selectValue}
                onValueChange={(value) => {
                  setLocalKeyId(value === "auto" ? "" : value);
                  keyMutation.mutate(value === "auto" ? "auto" : value);
                }}
                disabled={keyMutation.isPending || probeKeys.length === 0}
              >
                <SelectTrigger className="h-8 w-full min-w-[12rem] bg-background text-foreground">
                  <SelectValue placeholder={t("resinQualityGuard.selectKey")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="auto">{t("resinQualityGuard.autoKey")}</SelectItem>
                  {probeKeys.map((key) => (
                    <SelectItem key={key.id} value={key.id}>
                      {key.name} ({key.prefix}…)
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </aside>
          {probeKeys.length === 0 ? (
            <p className="text-xs text-amber-600 dark:text-amber-400">{t("resinQualityGuard.noKeys")}</p>
          ) : (
            <p className="text-xs text-muted-foreground">
              {t("resinQualityGuard.effectiveKey", { id: effectiveKeyId || "—" })}
              {cfg?.probeModel ? ` · model ${cfg.probeModel}` : null}
            </p>
          )}

          <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
            <InfoCard label={t("resinQualityGuard.status")} value={pool?.quarantineActive ? t("resinQualityGuard.quarantined") : t("resinQualityGuard.healthy")}>
              <Badge variant={pool?.quarantineActive ? "destructive" : "secondary"}>{pool?.lastClassification || "—"}</Badge>
            </InfoCard>
            <InfoCard label={t("resinQualityGuard.lastExitIp")} value={pool?.lastExitIp || "—"} />
            <InfoCard label={t("resinQualityGuard.lastTps")} value={pool?.lastOutputTps != null ? pool.lastOutputTps.toFixed(1) : "—"} />
            <InfoCard label={t("resinQualityGuard.mode")} value={`${cfg?.mode ?? "—"} / ${cfg?.actionMode ?? "—"}`} />
          </div>

          <div className="grid gap-4 lg:grid-cols-2">
            <section className="rounded-xl border p-4">
              <h2 className="mb-3 flex items-center gap-2 text-sm font-medium">
                <ShieldCheck className="size-4" />
                {t("resinQualityGuard.pool")}
              </h2>
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
                <dt className="text-muted-foreground">{t("resinQualityGuard.softStrikes")}</dt>
                <dd>
                  {pool?.softStrikes ?? 0} / {cfg?.consecutiveSoft ?? "—"}
                </dd>
                <dt className="text-muted-foreground">{t("resinQualityGuard.errorStrikes")}</dt>
                <dd>
                  {pool?.errorStrikes ?? 0} / {cfg?.consecutiveErrors ?? "—"}
                </dd>
                <dt className="text-muted-foreground">{t("resinQualityGuard.quarantineUntil")}</dt>
                <dd>{formatTs(pool?.quarantinedUntil)}</dd>
                <dt className="text-muted-foreground">{t("resinQualityGuard.lastReason")}</dt>
                <dd className="truncate">{pool?.lastReason || "—"}</dd>
                <dt className="text-muted-foreground">{t("resinQualityGuard.suspectIps")}</dt>
                <dd className="truncate">{(pool?.suspectExitIps || []).join(", ") || "—"}</dd>
                <dt className="text-muted-foreground">{t("resinQualityGuard.thresholds")}</dt>
                <dd>
                  soft {cfg?.softTps} / hard {cfg?.hardTps}
                </dd>
              </dl>
            </section>

            <section className="rounded-xl border p-4">
              <h2 className="mb-3 text-sm font-medium">{t("resinQualityGuard.stats")}</h2>
              <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
                <dt className="text-muted-foreground">active total</dt>
                <dd>{stats?.active?.total ?? 0}</dd>
                <dt className="text-muted-foreground">active hard/soft/error</dt>
                <dd>
                  {stats?.active?.hard ?? 0}/{stats?.active?.soft ?? 0}/{stats?.active?.error ?? 0}
                </dd>
                <dt className="text-muted-foreground">actions quarantined</dt>
                <dd>{stats?.actions?.quarantined ?? 0}</dd>
                <dt className="text-muted-foreground">actions restored</dt>
                <dd>{stats?.actions?.restored ?? 0}</dd>
                <dt className="text-muted-foreground">reshuffles</dt>
                <dd>{stats?.actions?.reshuffles ?? 0}</dd>
                <dt className="text-muted-foreground">last active cycle</dt>
                <dd>{formatTs(status?.state?.lastActiveCycleAt)}</dd>
              </dl>
            </section>
          </div>

          <section className="rounded-xl border p-4">
            <h2 className="mb-3 text-sm font-medium">{t("resinQualityGuard.events")}</h2>
            {events.length === 0 ? (
              <p className="text-sm text-muted-foreground">{t("common.noData")}</p>
            ) : (
              <div className="max-h-80 space-y-2 overflow-auto text-sm">
                {[...events].reverse().slice(0, 50).map((ev, index) => (
                  <div key={`${ev.ts}-${ev.event}-${index}`} className="flex flex-wrap gap-x-3 gap-y-1 border-b border-border/50 py-2 last:border-0">
                    <span className="text-muted-foreground">{formatTs(ev.ts)}</span>
                    <span className="font-medium">{ev.event}</span>
                    {ev.reason ? <span>{ev.reason}</span> : null}
                    {ev.classification ? <Badge variant="outline">{ev.classification}</Badge> : null}
                    {ev.exitIp ? <span className="text-muted-foreground">{ev.exitIp}</span> : null}
                    {typeof ev.cleared === "number" ? <span>cleared={ev.cleared}</span> : null}
                  </div>
                ))}
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}

function InfoCard({ label, value, children }: { label: string; value: string; children?: ReactNode }) {
  return (
    <div className="rounded-xl border p-4">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 flex items-center gap-2 text-lg font-semibold">
        {value}
        {children}
      </div>
    </div>
  );
}
