package resinquality

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"strings"
	"time"

	auditapp "github.com/chenyme/grok2api/backend/internal/application/audit"
	clientkeyapp "github.com/chenyme/grok2api/backend/internal/application/clientkey"
	auditdomain "github.com/chenyme/grok2api/backend/internal/domain/audit"
	clientkeydomain "github.com/chenyme/grok2api/backend/internal/domain/clientkey"
	"github.com/chenyme/grok2api/backend/internal/repository"
)

// Guard runs passive/active cycles and executes Resin lease actions.
type Guard struct {
	cfg        Config
	log        *slog.Logger
	resin      *ResinClient
	store      *stateStore
	clientKeys *clientkeyapp.Service
	audits     *auditapp.Service
}

// NewGuard constructs a guard.
// clientKeys may be nil (then only env ProbeAPIKey works).
// audits may be nil (then passive quality classification is skipped).
func NewGuard(cfg Config, logger *slog.Logger, clientKeys *clientkeyapp.Service, audits *auditapp.Service) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	g := &Guard{
		cfg:        cfg,
		log:        logger.With("component", "resin_quality_guard"),
		store:      newStateStore(cfg.StateFile),
		clientKeys: clientKeys,
		audits:     audits,
	}
	if cfg.CanRun() {
		g.resin = NewResinClient(cfg.ResinBaseURL, cfg.ResinAdminToken, cfg.RequestTimeout)
	}
	return g
}

// Enabled reports whether background work should run.
func (g *Guard) Enabled() bool {
	return g != nil && g.cfg.CanRun()
}

// ProbeKeyOption is a safe client-key row for UI select (no secret).
type ProbeKeyOption struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// Status is the admin UI payload.
type Status struct {
	Available          bool             `json:"available"`
	Enabled            bool             `json:"enabled"`
	Config             PublicCfg        `json:"config"`
	State              State            `json:"state"`
	ProbeKeys          []ProbeKeyOption `json:"probeKeys,omitempty"`
	SelectedProbeKeyID string           `json:"selectedProbeKeyId,omitempty"`
	EffectiveProbeKey  string           `json:"effectiveProbeKeyId,omitempty"`
}

// PublicCfg is safe config for UI (no tokens).
type PublicCfg struct {
	Mode              string  `json:"mode"`
	ActionMode        string  `json:"actionMode"`
	SoftTPS           float64 `json:"softTps"`
	HardTPS           float64 `json:"hardTps"`
	ConsecutiveSoft   int     `json:"consecutiveSoft"`
	ConsecutiveErrors int     `json:"consecutiveErrors"`
	QuarantineSeconds int     `json:"quarantineSeconds"`
	ActiveIntervalSec int     `json:"activeIntervalSeconds"`
	PassivePollSec    int     `json:"passivePollSeconds"`
	FailClosed        bool    `json:"failClosed"`
	ZeroReasoningSoft bool    `json:"zeroReasoningSoft"`
	PlatformID        string  `json:"platformId"`
	CanProbe          bool    `json:"canProbe"`
	HasProxyURL       bool    `json:"hasProxyUrl"`
	ProbeModel        string  `json:"probeModel"`
	AutoSelectKey     bool    `json:"autoSelectKey"`
}

func (g *Guard) publicCfg() PublicCfg {
	return PublicCfg{
		Mode:              g.cfg.Mode,
		ActionMode:        g.cfg.ActionMode,
		SoftTPS:           g.cfg.SoftTPS,
		HardTPS:           g.cfg.HardTPS,
		ConsecutiveSoft:   g.cfg.ConsecutiveSoft,
		ConsecutiveErrors: g.cfg.ConsecutiveErrors,
		QuarantineSeconds: int(g.cfg.Quarantine.Seconds()),
		ActiveIntervalSec: int(g.cfg.ActiveInterval.Seconds()),
		PassivePollSec:    int(g.cfg.PassivePoll.Seconds()),
		FailClosed:        g.cfg.FailClosed,
		ZeroReasoningSoft: g.cfg.ZeroReasoningSoft,
		PlatformID:        g.cfg.PlatformID,
		CanProbe:          g.canProbe(),
		HasProxyURL:       g.cfg.ResinProxyURL != "",
		ProbeModel:        g.cfg.ProbeModel,
		AutoSelectKey:     g.cfg.ProbeAPIKey == "" && g.clientKeys != nil,
	}
}

func (g *Guard) canProbe() bool {
	if g == nil || g.cfg.ProbeBaseURL == "" {
		return false
	}
	if g.cfg.ProbeAPIKey != "" {
		return true
	}
	return g.clientKeys != nil
}

// GetStatus returns UI status including usable client keys for probe select.
func (g *Guard) GetStatus(ctx context.Context) Status {
	if g == nil {
		return Status{Available: false}
	}
	st := g.store.snapshot()
	keys := g.listProbeKeyOptions(ctx)
	selected := st.SelectedProbeKeyID
	if selected == 0 {
		selected = g.cfg.ProbeKeyID
	}
	effective := g.effectiveProbeKeyID(keys, selected)
	return Status{
		Available:          g.Enabled(),
		Enabled:            g.cfg.Enabled,
		Config:             g.publicCfg(),
		State:              st,
		ProbeKeys:          keys,
		SelectedProbeKeyID: formatKeyID(selected),
		EffectiveProbeKey:  formatKeyID(effective),
	}
}

func formatKeyID(id uint64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 10)
}

func (g *Guard) listProbeKeyOptions(ctx context.Context) []ProbeKeyOption {
	if g.clientKeys == nil {
		return nil
	}
	items, _, err := g.clientKeys.List(ctx, 1, 200, "", clientkeyapp.ListFilter{Status: "active"})
	if err != nil {
		g.log.Warn("list_probe_keys_failed", "error", err)
		return nil
	}
	now := time.Now().UTC()
	out := make([]ProbeKeyOption, 0, len(items))
	for _, key := range items {
		if !key.IsAvailable(now) {
			continue
		}
		out = append(out, ProbeKeyOption{
			ID:     strconv.FormatUint(key.ID, 10),
			Name:   key.Name,
			Prefix: key.Prefix,
		})
	}
	return out
}

func (g *Guard) effectiveProbeKeyID(keys []ProbeKeyOption, selected uint64) uint64 {
	if selected != 0 {
		for _, k := range keys {
			if k.ID == strconv.FormatUint(selected, 10) {
				return selected
			}
		}
	}
	if g.cfg.ProbeKeyID != 0 {
		for _, k := range keys {
			if k.ID == strconv.FormatUint(g.cfg.ProbeKeyID, 10) {
				return g.cfg.ProbeKeyID
			}
		}
	}
	if len(keys) > 0 {
		id, _ := strconv.ParseUint(keys[0].ID, 10, 64)
		return id
	}
	return 0
}

// SetSelectedProbeKey stores UI selection (0 = auto).
func (g *Guard) SetSelectedProbeKey(id uint64) {
	g.store.update(func(st *State) {
		st.SelectedProbeKeyID = id
	})
}

// resolveProbeAPIKey returns bearer secret for model probe.
func (g *Guard) resolveProbeAPIKey(ctx context.Context) (secret string, keyID uint64, err error) {
	if g.cfg.ProbeAPIKey != "" {
		return g.cfg.ProbeAPIKey, 0, nil
	}
	if g.clientKeys == nil {
		return "", 0, errProbeNotConfigured
	}
	keys := g.listProbeKeyOptions(ctx)
	st := g.store.snapshot()
	selected := st.SelectedProbeKeyID
	if selected == 0 {
		selected = g.cfg.ProbeKeyID
	}
	id := g.effectiveProbeKeyID(keys, selected)
	if id == 0 {
		return "", 0, fmt.Errorf("%w: no active client key", errProbeNotConfigured)
	}
	secret, err = g.clientKeys.RevealSecret(ctx, id)
	if err != nil {
		return "", 0, err
	}
	return secret, id, nil
}

func usableKey(key clientkeydomain.Key, now time.Time) bool {
	return key.IsAvailable(now)
}

var _ = usableKey // keep for tests / clarity

// Run is the background loop (startBackground compatible).
func (g *Guard) Run(ctx context.Context) error {
	if !g.Enabled() {
		g.log.Info("resin_quality_guard_disabled")
		<-ctx.Done()
		return nil
	}
	g.log.Info("resin_quality_guard_started",
		"mode", g.cfg.Mode,
		"action_mode", g.cfg.ActionMode,
		"platform_id", g.cfg.PlatformID,
	)

	passiveEnabled := g.cfg.Mode == "passive" || g.cfg.Mode == "hybrid"
	activeEnabled := g.cfg.Mode == "active" || g.cfg.Mode == "hybrid"

	var nextPassive, nextActive time.Time
	now := time.Now()
	if passiveEnabled {
		nextPassive = now
	}
	if activeEnabled {
		// stagger first active slightly
		nextActive = now.Add(5 * time.Second)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			g.log.Info("resin_quality_guard_stopped")
			return nil
		case t := <-ticker.C:
			if passiveEnabled && !t.Before(nextPassive) {
				g.runPassiveCycle(ctx)
				nextPassive = time.Now().Add(g.cfg.PassivePoll)
			}
			if activeEnabled && !t.Before(nextActive) {
				g.runActiveCycle(ctx)
				jitter := time.Duration(0)
				if g.cfg.Jitter > 0 {
					jitter = time.Duration(rand.Int63n(int64(g.cfg.Jitter)*2)) - g.cfg.Jitter
				}
				d := g.cfg.ActiveInterval + jitter
				if d < time.Minute {
					d = time.Minute
				}
				nextActive = time.Now().Add(d)
			}
		}
	}
}

func (g *Guard) runPassiveCycle(ctx context.Context) {
	g.tryRecover(ctx)
	g.consumeNewAudits(ctx)
	g.store.update(func(st *State) {
		st.LastPassivePollAt = float64(time.Now().Unix())
	})
}

// consumeNewAudits reads successful stream audits newer than the watermark and classifies TPS.
func (g *Guard) consumeNewAudits(ctx context.Context) {
	if g.audits == nil {
		return
	}
	snap := g.store.snapshot()

	const pageSize = 100
	filter := auditapp.ListFilter{
		Status: "success",
		Mode:   "stream",
		Sort:   repository.SortQuery{Field: "createdAt", Direction: repository.SortDescending},
	}
	result, err := g.audits.ListCursor(ctx, "", pageSize, "", "24h", filter)
	if err != nil {
		g.log.Warn("passive_list_audits_failed", "error", err)
		return
	}
	if len(result.Items) == 0 {
		return
	}

	// First run: baseline only — remember newest ID, do not classify backlog.
	if !snap.PassiveInitialized {
		newest := result.Items[0].ID
		g.store.update(func(st *State) {
			st.PassiveInitialized = true
			st.PassiveWatermarkID = newest
		})
		g.log.Info("passive_baseline_initialized", "watermark_id", newest, "page_size", len(result.Items))
		return
	}

	watermark := snap.PassiveWatermarkID
	// Items are newest-first; collect those with ID > watermark, then process oldest-first.
	var batch []AuditSample
	var maxSeen uint64 = watermark
	for _, rec := range result.Items {
		if rec.ID <= watermark {
			break
		}
		if rec.ID > maxSeen {
			maxSeen = rec.ID
		}
		// While quarantined, only advance watermark so recovery is not hit by stale backlog.
		if snap.Pool.QuarantineActive {
			continue
		}
		// Skip our own quality probes (by key name heuristic).
		name := strings.ToLower(rec.ClientKeyName)
		if strings.Contains(name, "quality-guard") || strings.Contains(name, "resin-qg") || strings.Contains(name, "resin-guard") {
			continue
		}
		// Only chat/responses-style generation traffic is meaningful for TPS.
		switch rec.Operation {
		case auditdomain.OperationChat, auditdomain.OperationResponses, auditdomain.OperationMessages, "":
			// ok
		default:
			continue
		}
		batch = append(batch, auditRecordToSample(rec))
	}
	if maxSeen > watermark {
		g.store.update(func(st *State) {
			st.PassiveWatermarkID = maxSeen
		})
	}
	if snap.Pool.QuarantineActive || len(batch) == 0 {
		return
	}

	// process oldest first
	for i := len(batch) - 1; i >= 0; i-- {
		sample := batch[i]
		class, reason, speed, tokens := classifyAudit(g.cfg, sample)
		now := float64(time.Now().Unix())
		g.store.update(func(st *State) {
			st.bump("passive", "total", 1)
			if class == ClassIgnored {
				st.bump("passive", "ignored", 1)
			} else if class == ClassHealthy || class == ClassSoft || class == ClassHard {
				st.bump("passive", class, 1)
			}
			if class != ClassIgnored {
				st.LastPassiveSampleTS = now
				st.LastPassiveTPS = speed
				st.LastPassiveReason = reason
				st.LastPassiveClass = class
				st.LastPassiveAuditID = sample.ID
			}
			if sample.ExitIP != "" && (class == ClassSoft || class == ClassHard) {
				st.Pool.SuspectExitIPs = appendUnique(st.Pool.SuspectExitIPs, sample.ExitIP)
				if len(st.Pool.SuspectExitIPs) > 20 {
					st.Pool.SuspectExitIPs = st.Pool.SuspectExitIPs[len(st.Pool.SuspectExitIPs)-20:]
				}
				st.Pool.LastExitIP = sample.ExitIP
			}
			if class == ClassSoft {
				st.appendSoftSample(SoftSample{
					TS: now, AuditID: sample.ID, Reason: reason, OutputTPS: speed,
					Tokens: tokens, Source: "passive", ExitIP: sample.ExitIP,
				})
				st.appendEvent(Event{
					TS: now, Event: "soft_sample", Reason: reason, Classification: class,
					OutputTPS: speed, ExitIP: sample.ExitIP, AuditID: sample.ID, Source: "passive", Tokens: tokens,
				})
			}
		})
		if class == ClassIgnored {
			continue
		}
		g.log.Info("passive_audit_sample",
			"audit_id", sample.ID,
			"class", class,
			"reason", reason,
			"tps", speed,
			"tokens", tokens,
			"provider", sample.Provider,
			"exit_ip", sample.ExitIP,
		)
		g.observe(class, reason, speed, "passive")
	}
}

func auditRecordToSample(rec auditdomain.Record) AuditSample {
	streaming := rec.Streaming
	firstTokenMS := int64(-1)
	if rec.FirstTokenMS != nil {
		firstTokenMS = *rec.FirstTokenMS
	}
	// Visible generation tokens only (exclude pure reasoning if broken out).
	tokens := rec.OutputTokens
	if tokens <= 0 && rec.ReasoningTokens > 0 {
		tokens = rec.ReasoningTokens
	}
	return AuditSample{
		ID:              strconv.FormatUint(rec.ID, 10),
		Provider:        rec.Provider,
		Streaming:       &streaming,
		Status:          "success",
		StatusCode:      rec.StatusCode,
		ErrorCode:       rec.ErrorCode,
		OutputTokens:    tokens,
		ReasoningTokens: rec.ReasoningTokens,
		ReasoningKnown:  true,
		DurationMS:      rec.DurationMS,
		FirstTokenMS:    firstTokenMS,
		// Audit schema has no resin exit IP; leave empty (pool reshuffle does not need it).
		ExitIP: "",
	}
}

func (g *Guard) runActiveCycle(ctx context.Context) {
	g.tryRecover(ctx)
	snap := g.store.snapshot()
	if snap.Pool.QuarantineActive {
		g.log.Info("active_cycle_skipped_quarantine", "until", snap.Pool.QuarantinedUntil)
		g.store.update(func(st *State) {
			st.LastActiveCycleAt = float64(time.Now().Unix())
		})
		return
	}

	// 1) exit sample — connectivity only; never quarantine solely on sample failure
	// (avoids mass lease clears from TLS/DNS blips).
	if g.cfg.ResinProxyURL != "" {
		ip, err := SampleExitIP(ctx, g.cfg.ResinProxyURL, 15*time.Second)
		if err != nil {
			g.log.Warn("active_exit_sample_failed", "error", err)
		} else {
			g.log.Info("active_exit_sample", "exit_ip", ip)
			g.store.update(func(st *State) {
				st.Pool.LastExitIP = ip
			})
		}
	}

	// 2) optional model probe (auto client key like creative console)
	if g.canProbe() {
		secret, keyID, err := g.resolveProbeAPIKey(ctx)
		if err != nil {
			g.log.Info("active_probe_skipped", "reason", err.Error())
		} else {
			result, err := RunModelProbe(ctx, g.cfg.ProbeBaseURL, secret, g.cfg.ProbeModel, g.cfg.ProbeMaxTokens, 120*time.Second)
			g.store.update(func(st *State) {
				st.Pool.LastProbeAt = float64(time.Now().Unix())
				st.bump("active", "total", 1)
			})
			if err != nil {
				g.log.Warn("active_probe_failed", "error", err, "key_id", keyID)
				g.store.update(func(st *State) { st.bump("active", "error", 1) })
				g.observe(ClassError, "active_probe_error", 0, "active")
			} else {
				class, reason := classifyProbe(g.cfg, result)
				g.store.update(func(st *State) {
					if class != ClassIgnored {
						st.bump("active", class, 1)
					}
				})
				g.log.Info("active_probe_done", "key_id", keyID, "class", class, "tps", result.OutputTokensPerSecond)
				g.observe(class, reason, result.OutputTokensPerSecond, "active")
			}
		}
	} else {
		g.log.Info("active_probe_skipped", "reason", "probe not configured")
	}

	g.store.update(func(st *State) {
		st.LastActiveCycleAt = float64(time.Now().Unix())
	})
}

func (g *Guard) observe(class, reason string, speed float64, source string) {
	now := float64(time.Now().Unix())
	var shouldQuarantine bool
	var qReason, qClass string
	var qSpeed float64

	g.store.update(func(st *State) {
		st.Pool.LastObservedAt = now
		st.Pool.LastClassification = class
		st.Pool.LastOutputTPS = speed
		// Always keep the latest non-empty reason visible in UI, including healthy ok.
		if reason != "" {
			st.Pool.LastReason = reason
		}
		if class == ClassIgnored {
			return
		}
		if class == ClassHealthy {
			st.Pool.SoftStrikes = 0
			st.Pool.ErrorStrikes = 0
			return
		}
		if class == ClassSoft {
			st.Pool.SoftStrikes++
			// zero_reasoning is immediate (1 hit). Other soft reasons still honor consecutiveSoft,
			// unless fail-closed is enabled.
			immediate := reason == "zero_reasoning"
			if immediate || g.cfg.FailClosed || st.Pool.SoftStrikes >= g.cfg.ConsecutiveSoft {
				shouldQuarantine = true
				qReason, qClass, qSpeed = reason, class, speed
			}
			return
		}
		if class == ClassHard {
			shouldQuarantine = true
			qReason, qClass, qSpeed = reason, class, speed
			return
		}
		if class == ClassError {
			st.Pool.ErrorStrikes++
			if st.Pool.ErrorStrikes >= g.cfg.ConsecutiveErrors {
				shouldQuarantine = true
				qReason, qClass, qSpeed = reason, class, speed
			}
		}
		_ = source
	})

	if shouldQuarantine {
		g.quarantine(context.Background(), qReason, qClass, qSpeed)
	}
}

func (g *Guard) quarantine(ctx context.Context, reason, class string, speed float64) {
	snap := g.store.snapshot()
	now := time.Now()
	if snap.Pool.QuarantineActive && float64(now.Unix()) < snap.Pool.QuarantinedUntil {
		g.log.Info("quarantine_already_active", "reason", reason)
		return
	}
	action, err := g.executePoolAction(ctx, reason, snap.Pool.SuspectExitIPs)
	if err != nil {
		g.log.Error("quarantine_action_failed", "error", err, "reason", reason)
		// still mark quarantine so we backoff
	}
	actionMap := actionToMap(action)
	g.store.update(func(st *State) {
		st.Pool.QuarantineActive = true
		st.Pool.QuarantinedUntil = float64(now.Add(g.cfg.Quarantine).Unix())
		st.Pool.LastReason = reason
		st.Pool.LastClassification = class
		st.Pool.LastOutputTPS = speed
		st.Pool.SoftStrikes = 0
		st.Pool.ErrorStrikes = 0
		st.Pool.LastAction = actionMap
		if st.Pool.LastExitIP != "" {
			st.Pool.SuspectExitIPs = appendUnique(st.Pool.SuspectExitIPs, st.Pool.LastExitIP)
			if len(st.Pool.SuspectExitIPs) > 20 {
				st.Pool.SuspectExitIPs = st.Pool.SuspectExitIPs[len(st.Pool.SuspectExitIPs)-20:]
			}
		}
		st.bump("actions", "quarantined", 1)
		st.appendEvent(Event{
			TS: float64(now.Unix()), Event: "pool_quarantined", Reason: reason,
			Classification: class, OutputTPS: speed, Cleared: action.Cleared, ExitIP: st.Pool.LastExitIP,
		})
	})
	g.log.Warn("pool_quarantined", "reason", reason, "class", class, "cleared", action.Cleared)
}

func (g *Guard) tryRecover(ctx context.Context) {
	snap := g.store.snapshot()
	if !snap.Pool.QuarantineActive {
		return
	}
	now := time.Now()
	if float64(now.Unix()) < snap.Pool.QuarantinedUntil {
		return
	}
	g.log.Info("recovery_started", "reason", snap.Pool.LastReason)

	var sampleIP string
	sampleOK := true
	if g.cfg.ResinProxyURL != "" {
		ip, err := SampleExitIP(ctx, g.cfg.ResinProxyURL, 15*time.Second)
		if err != nil {
			sampleOK = false
			g.log.Warn("recovery_exit_sample_failed", "error", err)
		} else {
			sampleIP = ip
			g.store.update(func(st *State) { st.Pool.LastExitIP = ip })
		}
	}

	healthy := true
	reason := "connectivity_only"
	speed := 0.0
	if g.canProbe() {
		secret, _, err := g.resolveProbeAPIKey(ctx)
		if err != nil {
			healthy = sampleOK
			if healthy {
				reason = "connectivity_ok"
			} else {
				reason = "connectivity_failed"
			}
		} else {
			result, err := RunModelProbe(ctx, g.cfg.ProbeBaseURL, secret, g.cfg.ProbeModel, g.cfg.ProbeMaxTokens, 120*time.Second)
			g.store.update(func(st *State) { st.bump("active", "total", 1) })
			if err != nil {
				healthy = false
				reason = "recovery_probe_error"
				g.store.update(func(st *State) { st.bump("active", "error", 1) })
			} else {
				class, r := classifyProbe(g.cfg, result)
				speed = result.OutputTokensPerSecond
				reason = r
				healthy = class == ClassHealthy
				if class != ClassIgnored {
					g.store.update(func(st *State) { st.bump("active", class, 1) })
				}
			}
		}
	} else {
		healthy = sampleOK
		if healthy {
			reason = "connectivity_ok"
		} else {
			reason = "connectivity_failed"
		}
	}

	// connectivity-only: refuse restore onto still-suspect IP
	if healthy && !g.canProbe() && sampleIP != "" {
		for _, s := range snap.Pool.SuspectExitIPs {
			if s == sampleIP {
				healthy = false
				reason = "exit_still_suspect"
				break
			}
		}
	}

	if !healthy {
		g.store.update(func(st *State) {
			st.Pool.QuarantinedUntil = float64(now.Add(g.cfg.Quarantine).Unix())
			st.Pool.LastReason = reason
			st.appendEvent(Event{TS: float64(now.Unix()), Event: "quarantine_extended", Reason: reason, OutputTPS: speed})
		})
		if _, err := g.executePoolAction(ctx, reason, snap.Pool.SuspectExitIPs); err != nil {
			g.log.Warn("recovery_reshuffle_failed", "error", err)
		}
		g.log.Warn("quarantine_extended", "reason", reason)
		return
	}

	g.store.update(func(st *State) {
		if sampleIP != "" {
			filtered := st.Pool.SuspectExitIPs[:0]
			for _, s := range st.Pool.SuspectExitIPs {
				if s != sampleIP {
					filtered = append(filtered, s)
				}
			}
			st.Pool.SuspectExitIPs = filtered
		}
		st.Pool.QuarantineActive = false
		st.Pool.QuarantinedUntil = 0
		st.Pool.LastReason = ""
		st.Pool.SoftStrikes = 0
		st.Pool.ErrorStrikes = 0
		st.Pool.LastClassification = ClassHealthy
		st.bump("actions", "restored", 1)
		st.appendEvent(Event{TS: float64(now.Unix()), Event: "pool_restored", Reason: reason, ExitIP: sampleIP, OutputTPS: speed})
	})
	g.log.Info("pool_restored", "reason", reason, "exit_ip", sampleIP)
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
	// post sample
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

// ManualProbe runs one active model probe for admin UI (uses selected/auto client key).
func (g *Guard) ManualProbe(ctx context.Context) (ProbeResult, error) {
	if !g.Enabled() {
		return ProbeResult{}, errDisabled
	}
	if !g.canProbe() {
		return ProbeResult{OK: false, Error: "probe not configured"}, errProbeNotConfigured
	}
	secret, keyID, err := g.resolveProbeAPIKey(ctx)
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error()}, err
	}
	result, err := RunModelProbe(ctx, g.cfg.ProbeBaseURL, secret, g.cfg.ProbeModel, g.cfg.ProbeMaxTokens, 120*time.Second)
	if err != nil {
		return result, err
	}
	g.log.Info("manual_probe_done", "key_id", keyID, "tps", result.OutputTokensPerSecond, "matched", result.ExpectedMatched)
	return result, nil
}

var (
	errDisabled           = errString("resin quality guard disabled")
	errProbeNotConfigured = errString("probe not configured")
)

type errString string

func (e errString) Error() string { return string(e) }

func actionToMap(a ActionResult) map[string]any {
	return map[string]any{
		"action": a.Action, "beforeTotal": a.BeforeTotal, "afterTotal": a.AfterTotal,
		"cleared": a.Cleared, "matched": a.Matched, "errors": a.Errors,
		"ok": a.OK, "verified": a.Verified, "reason": a.Reason, "fallback": a.Fallback,
	}
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
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
