package resinquality

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strconv"
	"time"

	clientkeyapp "github.com/chenyme/grok2api/backend/internal/application/clientkey"
	clientkeydomain "github.com/chenyme/grok2api/backend/internal/domain/clientkey"
)

// Guard runs passive/active cycles and executes Resin lease actions.
type Guard struct {
	cfg        Config
	log        *slog.Logger
	resin      *ResinClient
	store      *stateStore
	clientKeys *clientkeyapp.Service
}

// NewGuard constructs a guard. clientKeys may be nil (then only env ProbeAPIKey works).
func NewGuard(cfg Config, logger *slog.Logger, clientKeys *clientkeyapp.Service) *Guard {
	if logger == nil {
		logger = slog.Default()
	}
	g := &Guard{
		cfg:        cfg,
		log:        logger.With("component", "resin_quality_guard"),
		store:      newStateStore(cfg.StateFile),
		clientKeys: clientKeys,
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
		PlatformID:        g.cfg.PlatformID,
		CanProbe:          g.canProbe(),
		HasProxyURL:       g.cfg.ResinProxyURL != "",
		ProbeModel:        g.cfg.ProbeModel,
		AutoSelectKey:     g.clientKeys != nil && g.cfg.ProbeAPIKey == "",
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
	// Passive is reserved for audit feed wiring; still drive recovery + heartbeat.
	g.tryRecover(ctx)
	g.store.update(func(st *State) {
		st.LastPassivePollAt = float64(time.Now().Unix())
	})
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
			if g.cfg.FailClosed || st.Pool.SoftStrikes >= g.cfg.ConsecutiveSoft {
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
