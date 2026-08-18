package resinquality

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config controls the in-process Resin pool quality guard.
type Config struct {
	Enabled bool

	ResinBaseURL    string
	ResinAdminToken string
	PlatformID      string
	PlatformName    string
	ResinProxyURL   string // optional SOCKS/HTTP URL for exit sampling

	Mode              string // passive|active|hybrid
	ActionMode        string // reshuffle|targeted
	SoftTPS           float64
	HardTPS           float64
	ConsecutiveSoft   int
	ConsecutiveErrors int
	Quarantine        time.Duration
	MinGeneration     time.Duration
	MinOutputTokens   int
	ActiveInterval    time.Duration
	PassivePoll       time.Duration
	Jitter            time.Duration
	FailClosed        bool
	// ZeroReasoningSoft treats successful build stream samples with reasoningTokens==0
	// as soft degradation, OR-combined with TPS classification.
	ZeroReasoningSoft bool
	// StreamWatchEnabled peeks streaming Build replies: content without prior thinking
	// is treated as degraded → clear Resin pool and transparently retry (each attempt
	// is preflighted again) up to StreamMaxAttempts total tries.
	StreamWatchEnabled bool
	// StreamWatchTimeout bounds how long preflight waits for the first semantic signal.
	StreamWatchTimeout time.Duration
	// StreamMaxAttempts is the total number of preflighted upstream attempts per request
	// (1 = watch only, no retry; 3 = original + up to 2 retries). Clamped to 1..8.
	StreamMaxAttempts int
	RequestTimeout    time.Duration
	StateFile         string

	// Optional: use local gateway base for model probe.
	// Prefer in-process ClientKey List/Reveal (like creative console) over env secret.
	ProbeBaseURL   string
	ProbeAPIKey    string // optional legacy override; leave empty to auto-pick client keys
	ProbeKeyID     uint64 // optional preferred client key id (0 = auto first usable)
	ProbeModel     string
	ProbeMaxTokens int
}

// LoadConfigFromEnv reads RESIN_QUALITY_GUARD_* and related env vars.
func LoadConfigFromEnv() Config {
	cfg := Config{
		Enabled:           envBool("RESIN_QUALITY_GUARD_ENABLED", false),
		ResinBaseURL:      strings.TrimRight(envStr("RESIN_BASE_URL", "http://127.0.0.1:2260"), "/"),
		ResinAdminToken:   envStr("RESIN_ADMIN_TOKEN", ""),
		PlatformID:        envStr("RESIN_PLATFORM_ID", "00000000-0000-0000-0000-000000000000"),
		PlatformName:      envStr("RESIN_PLATFORM_NAME", "Default"),
		ResinProxyURL:     envStr("RESIN_PROXY_URL", ""),
		Mode:              strings.ToLower(envStr("RESIN_QUALITY_GUARD_MODE", "hybrid")),
		ActionMode:        strings.ToLower(envStr("RESIN_QUALITY_GUARD_ACTION_MODE", "reshuffle")),
		SoftTPS:           envFloat("RESIN_QUALITY_GUARD_SOFT_TPS", 500),
		HardTPS:           envFloat("RESIN_QUALITY_GUARD_HARD_TPS", 1000),
		ConsecutiveSoft:   envInt("RESIN_QUALITY_GUARD_CONSECUTIVE_SOFT", 2),
		ConsecutiveErrors: envInt("RESIN_QUALITY_GUARD_CONSECUTIVE_ERRORS", 2),
		Quarantine:        time.Duration(envInt("RESIN_QUALITY_GUARD_QUARANTINE_SECONDS", 300)) * time.Second,
		MinGeneration:     time.Duration(envInt("RESIN_QUALITY_GUARD_MIN_GENERATION_MS", 1000)) * time.Millisecond,
		MinOutputTokens:   envInt("RESIN_QUALITY_GUARD_MIN_OUTPUT_TOKENS", 32),
		ActiveInterval:    time.Duration(envInt("RESIN_QUALITY_GUARD_ACTIVE_INTERVAL_SECONDS", 1800)) * time.Second,
		PassivePoll:       time.Duration(envInt("RESIN_QUALITY_GUARD_PASSIVE_POLL_SECONDS", 5)) * time.Second,
		Jitter:            time.Duration(envInt("RESIN_QUALITY_GUARD_JITTER_SECONDS", 30)) * time.Second,
		FailClosed:        envBool("RESIN_QUALITY_GUARD_FAIL_CLOSED", false),
		// Default on: live grok-4.5 degraded exits often show reasoningTokens=0.
		ZeroReasoningSoft:  envBool("RESIN_QUALITY_GUARD_ZERO_REASONING_SOFT", true),
		StreamWatchEnabled: envBool("RESIN_QUALITY_GUARD_STREAM_WATCH", false),
		StreamWatchTimeout: time.Duration(envInt("RESIN_QUALITY_GUARD_STREAM_WATCH_TIMEOUT_SECONDS", 45)) * time.Second,
		StreamMaxAttempts:  envInt("RESIN_QUALITY_GUARD_STREAM_MAX_ATTEMPTS", 3),
		RequestTimeout:     time.Duration(envInt("RESIN_QUALITY_GUARD_REQUEST_TIMEOUT_SECONDS", 60)) * time.Second,
		StateFile:          envStr("RESIN_QUALITY_GUARD_STATE_FILE", "/app/data/resin-quality-guard-state.json"),
		ProbeBaseURL:       strings.TrimRight(envStr("RESIN_QUALITY_GUARD_PROBE_BASE_URL", ""), "/"),
		ProbeAPIKey:        envStr("RESIN_QUALITY_GUARD_PROBE_API_KEY", ""),
		ProbeKeyID:         uint64(envInt("RESIN_QUALITY_GUARD_PROBE_KEY_ID", 0)),
		ProbeModel:         envStr("RESIN_QUALITY_GUARD_PROBE_MODEL", "grok-4.5"),
		ProbeMaxTokens:     envInt("RESIN_QUALITY_GUARD_PROBE_MAX_TOKENS", 256),
	}
	if cfg.Mode != "passive" && cfg.Mode != "active" && cfg.Mode != "hybrid" {
		cfg.Mode = "hybrid"
	}
	if cfg.ActionMode != "targeted" {
		cfg.ActionMode = "reshuffle"
	}
	if cfg.SoftTPS >= cfg.HardTPS {
		cfg.SoftTPS = 500
		cfg.HardTPS = 1000
	}
	cfg.StreamMaxAttempts = clampStreamMaxAttempts(cfg.StreamMaxAttempts)
	return cfg
}

func clampStreamMaxAttempts(n int) int {
	if n < 1 {
		return 3
	}
	if n > 8 {
		return 8
	}
	return n
}

func (c Config) CanRun() bool {
	return c.Enabled && c.ResinBaseURL != "" && c.ResinAdminToken != ""
}

// CanProbeWithKeys is true when base URL is set and either env secret or key store can supply a key.
func (c Config) CanProbeWithKeys(hasKeyStore bool) bool {
	if c.ProbeBaseURL == "" {
		return false
	}
	return c.ProbeAPIKey != "" || hasKeyStore
}

func envStr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}

func envBool(name string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func envInt(name string, def int) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloat(name string, def float64) float64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return n
}
