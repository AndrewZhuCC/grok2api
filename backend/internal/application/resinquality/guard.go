package resinquality

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// Guard serves in-request stream-watch (content-without-thinking → reshuffle + retry)
// and manual Resin pool clear. Official egress quality-guard owns passive/active node quarantine.
type Guard struct {
	cfg   Config
	log   *slog.Logger
	resin *ResinClient
	store *stateStore
}

// NewGuard constructs a guard.
// clientKeys/audits are accepted for call-site compatibility but no longer used:
// stream-watch is synchronous on the inference path; passive audit TPS polling is gone.
func NewGuard(cfg Config, logger *slog.Logger, _ interface{}, _ interface{}) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	g := &Guard{
		cfg:   cfg,
		log:   logger.With("component", "resin_quality_guard"),
		store: newStateStore(cfg.StateFile),
	}
	if cfg.CanRun() {
		g.resin = NewResinClient(cfg.ResinBaseURL, cfg.ResinAdminToken, cfg.RequestTimeout)
	}
	return g
}

// Enabled reports whether Resin actions can run.
func (g *Guard) Enabled() bool {
	return g != nil && g.cfg.CanRun()
}

// Status is the admin UI payload.
type Status struct {
	Available bool      `json:"available"`
	Enabled   bool      `json:"enabled"`
	Config    PublicCfg `json:"config"`
	State     State     `json:"state"`
}

// PublicCfg is safe config for UI (no tokens).
type PublicCfg struct {
	ActionMode         string `json:"actionMode"`
	StreamWatchEnabled bool   `json:"streamWatchEnabled"`
	StreamMaxAttempts  int    `json:"streamMaxAttempts"`
	PlatformID         string `json:"platformId"`
	HasProxyURL        bool   `json:"hasProxyUrl"`
}

func (g *Guard) publicCfg() PublicCfg {
	return PublicCfg{
		ActionMode:         g.cfg.ActionMode,
		StreamWatchEnabled: g.StreamWatchEnabled(),
		StreamMaxAttempts:  g.StreamMaxAttempts(),
		PlatformID:         g.cfg.PlatformID,
		HasProxyURL:        g.cfg.ResinProxyURL != "",
	}
}

// StreamWatchEnabled reports whether live stream first-signal watching is on.
// When off, inference does not interrupt, reshuffle, or transparently retry.
// UI override in state wins over process config when set to "on"/"off".
func (g *Guard) StreamWatchEnabled() bool {
	if g == nil || !g.cfg.CanRun() {
		return false
	}
	snap := g.store.snapshot()
	switch strings.ToLower(strings.TrimSpace(snap.StreamWatchOverride)) {
	case "on", "true", "1":
		return true
	case "off", "false", "0":
		return false
	}
	return g.cfg.StreamWatchEnabled
}

// UpdateStreamWatchEnabled persists a UI-editable stream-watch on/off switch.
func (g *Guard) UpdateStreamWatchEnabled(enabled bool) bool {
	if g == nil {
		return false
	}
	value := "off"
	if enabled {
		value = "on"
	}
	g.store.update(func(st *State) {
		st.StreamWatchOverride = value
	})
	g.log.Info("stream_watch_toggled", "enabled", enabled)
	return enabled
}

// StreamWatchTimeout is the preflight wait bound.
func (g *Guard) StreamWatchTimeout() time.Duration {
	if g == nil || g.cfg.StreamWatchTimeout <= 0 {
		return 45 * time.Second
	}
	return g.cfg.StreamWatchTimeout
}

// StreamMaxAttempts returns the effective total preflight attempts per request.
// UI override in state wins over process config when set.
func (g *Guard) StreamMaxAttempts() int {
	if g == nil {
		return 3
	}
	snap := g.store.snapshot()
	if snap.StreamMaxAttemptsOverride > 0 {
		return clampStreamMaxAttempts(snap.StreamMaxAttemptsOverride)
	}
	return clampStreamMaxAttempts(g.cfg.StreamMaxAttempts)
}

// UpdateStreamMaxAttempts persists a UI-editable attempt cap (1..8).
func (g *Guard) UpdateStreamMaxAttempts(n int) int {
	if g == nil {
		return 3
	}
	n = clampStreamMaxAttempts(n)
	g.store.update(func(st *State) {
		st.StreamMaxAttemptsOverride = n
	})
	return n
}

// GetStatus returns UI status for stream-watch + pool actions.
func (g *Guard) GetStatus(ctx context.Context) Status {
	if g == nil {
		return Status{Available: false}
	}
	_ = ctx
	return Status{
		Available: g.Enabled(),
		Enabled:   g.cfg.Enabled,
		Config:    g.publicCfg(),
		State:     g.store.snapshot(),
	}
}

// RecordStreamWatch stores a preflight verdict and bumps stream stats.
//
// Accounting invariant: total == healthy + degraded.
// - degraded: content without prior thinking (interrupt + reshuffle + retry)
// - healthy: everything else that was preflighted without triggering interrupt
//   (thinking present, timeout/EOF pass-through, tool-only, etc.)
// attempt is 1-based within the current request cycle.
func (g *Guard) RecordStreamWatch(verdict StreamWatchVerdict, attempt int) {
	if g == nil {
		return
	}
	if attempt < 1 {
		attempt = 1
	}
	now := float64(time.Now().Unix())
	g.store.update(func(st *State) {
		st.bump("stream", "total", 1)
		st.LastStreamSignal = verdict.FirstSignal
		st.LastStreamReason = verdict.Reason
		st.LastStreamAt = now
		st.LastStreamDegraded = verdict.Degraded
		st.LastStreamAttempts = attempt
		if verdict.Degraded {
			st.bump("stream", "degraded", 1)
			st.appendEvent(Event{
				TS: now, Event: "stream_degraded", Reason: verdict.Reason,
				Classification: ClassHard, Source: "stream_watch", Attempts: attempt,
			})
		} else {
			st.bump("stream", "healthy", 1)
		}
	})
}

// RecordStreamRetryIssued bumps the retried counter when a reshuffle+re-request is started.
func (g *Guard) RecordStreamRetryIssued() {
	if g == nil {
		return
	}
	g.store.update(func(st *State) { st.bump("stream", "retried", 1) })
}

// RecordStreamCycleOutcome notes the end of a multi-attempt recovery cycle.
// healthy=true means a later attempt passed preflight; false means exhausted or retry transport failed.
func (g *Guard) RecordStreamCycleOutcome(healthy bool, meta StreamDegradedMeta) {
	if g == nil {
		return
	}
	attempts := meta.AttemptsUsed
	if attempts < 1 {
		attempts = meta.Attempt
	}
	if attempts < 1 {
		attempts = 1
	}
	outcome := "failed"
	if healthy {
		outcome = "healthy"
	}
	g.log.Info("stream_degraded_retry_outcome",
		"outcome", outcome,
		"request_id", meta.RequestID,
		"account_id", meta.AccountID,
		"account_name", meta.AccountName,
		"model", meta.Model,
		"protocol", meta.Protocol,
		"reason", meta.Reason,
		"first_signal", meta.FirstSignal,
		"peek_ms", meta.PeekMS,
		"attempt", meta.Attempt,
		"attempts_used", attempts,
		"max_attempts", meta.MaxAttempts,
	)
	eventName := "stream_retry_failed"
	if healthy {
		eventName = "stream_retry_healthy"
	}
	g.store.update(func(st *State) {
		st.LastStreamAttempts = attempts
		st.LastStreamDegraded = !healthy
		if healthy {
			st.bump("stream", "retryHealthy", 1)
			switch {
			case attempts <= 1:
				st.bump("stream", "recoveredAt1", 1)
			case attempts == 2:
				st.bump("stream", "recoveredAt2", 1)
			case attempts == 3:
				st.bump("stream", "recoveredAt3", 1)
			default:
				st.bump("stream", "recoveredAt4Plus", 1)
			}
		} else {
			st.bump("stream", "retryFailed", 1)
			st.bump("stream", "exhausted", 1)
		}
		st.appendEvent(Event{
			TS: float64(time.Now().Unix()), Event: eventName,
			Reason: meta.Reason, Classification: ClassHard, Source: "stream_watch",
			AuditID: meta.RequestID, Attempts: attempts,
		})
	})
}

// StreamDegradedMeta carries request-scoped diagnostics for stream-watch logs/events.
type StreamDegradedMeta struct {
	RequestID    string
	AccountID    uint64
	AccountName  string
	Model        string
	Protocol     string
	FirstSignal  string
	Reason       string
	PeekMS       int64
	Attempt      int // 1-based attempt that produced this verdict
	MaxAttempts  int
	AttemptsUsed int // final attempts used when recording cycle outcome
}

// HandleStreamDegraded clears the Resin pool after a content-without-thinking hit.
func (g *Guard) HandleStreamDegraded(ctx context.Context, meta StreamDegradedMeta) (ActionResult, error) {
	if g == nil || !g.cfg.CanRun() {
		return ActionResult{}, errDisabled
	}
	reason := strings.TrimSpace(meta.Reason)
	if reason == "" {
		reason = "content_without_thinking"
	}
	result, err := g.executePoolAction(ctx, reason, nil)
	cleared := result.Cleared
	logArgs := []any{
		"reason", reason,
		"request_id", meta.RequestID,
		"account_id", meta.AccountID,
		"account_name", meta.AccountName,
		"model", meta.Model,
		"protocol", meta.Protocol,
		"first_signal", meta.FirstSignal,
		"peek_ms", meta.PeekMS,
		"attempt", meta.Attempt,
		"max_attempts", meta.MaxAttempts,
		"cleared", cleared,
	}
	if err != nil {
		logArgs = append(logArgs, "error", err.Error())
		g.log.Warn("stream_degraded_reshuffle", logArgs...)
	} else {
		g.log.Warn("stream_degraded_reshuffle", logArgs...)
	}
	g.store.update(func(st *State) {
		st.bump("stream", "reshuffles", 1)
		st.Pool.LastReason = reason
		st.Pool.LastClassification = ClassHard
		st.Pool.LastObservedAt = float64(time.Now().Unix())
		if err == nil {
			st.Pool.LastAction = actionToMap(result)
		}
		st.appendEvent(Event{
			TS: float64(time.Now().Unix()), Event: "stream_pool_reshuffled",
			Reason: reason, Classification: ClassHard, Cleared: cleared, Source: "stream_watch",
			AuditID: meta.RequestID, Attempts: meta.Attempt,
		})
	})
	return result, err
}

// Run is kept for startBackground compatibility.
// No background polling: stream-watch runs on the inference path.
func (g *Guard) Run(ctx context.Context) error {
	if !g.Enabled() {
		g.log.Info("resin_quality_guard_disabled")
		<-ctx.Done()
		return nil
	}
	g.log.Info("resin_quality_guard_started",
		"mode", "stream_watch",
		"stream_watch", g.cfg.StreamWatchEnabled,
		"action_mode", g.cfg.ActionMode,
		"platform_id", g.cfg.PlatformID,
	)
	<-ctx.Done()
	g.log.Info("resin_quality_guard_stopped")
	return nil
}

func (g *Guard) executePoolAction(ctx context.Context, reason string, suspect []string) (ActionResult, error) {
	platformID := g.cfg.PlatformID
	set := map[string]struct{}{}
	for _, ip := range suspect {
		ip = trim(ip)
		if ip != "" {
			set[ip] = struct{}{}
		}
	}
	var result ActionResult
	var err error
	if g.cfg.ActionMode == "targeted" && len(set) > 0 {
		result, err = g.resin.ClearLeasesForExitIPs(ctx, platformID, set)
		if err == nil && result.Cleared == 0 {
			result, err = g.resin.ReshufflePlatform(ctx, platformID)
			result.Fallback = "reshuffle_empty_targeted"
			g.store.update(func(st *State) { st.bump("actions", "reshuffles", 1) })
		} else if err == nil {
			g.store.update(func(st *State) { st.bump("actions", "targetedClears", 1) })
		} else {
			g.log.Warn("targeted_clear_failed", "error", err)
			result, err = g.resin.ReshufflePlatform(ctx, platformID)
			result.Fallback = "reshuffle_after_error"
			g.store.update(func(st *State) { st.bump("actions", "reshuffles", 1) })
		}
	} else {
		result, err = g.resin.ReshufflePlatform(ctx, platformID)
		if err == nil {
			g.store.update(func(st *State) { st.bump("actions", "reshuffles", 1) })
		}
	}
	if err != nil {
		return result, err
	}
	result.Reason = reason
	if g.cfg.ResinProxyURL != "" {
		if ip, sErr := SampleExitIP(ctx, g.cfg.ResinProxyURL, 15*time.Second); sErr == nil {
			g.store.update(func(st *State) { st.Pool.LastExitIP = ip })
		}
	}
	g.log.Info("resin_pool_action", "action", result.Action, "cleared", result.Cleared, "fallback", result.Fallback, "reason", reason)
	return result, nil
}

// ManualReshuffle triggers an immediate full lease clear (admin API).
func (g *Guard) ManualReshuffle(ctx context.Context) (ActionResult, error) {
	if !g.Enabled() {
		return ActionResult{}, errDisabled
	}
	return g.executePoolAction(ctx, "manual", nil)
}

var errDisabled = errString("resin quality guard disabled")

type errString string

func (e errString) Error() string { return string(e) }

func actionToMap(a ActionResult) map[string]any {
	return map[string]any{
		"action": a.Action, "beforeTotal": a.BeforeTotal, "afterTotal": a.AfterTotal,
		"cleared": a.Cleared, "matched": a.Matched, "errors": a.Errors,
		"ok": a.OK, "verified": a.Verified, "reason": a.Reason, "fallback": a.Fallback,
	}
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
