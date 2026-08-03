package resinquality

import "strings"

// Classification labels aligned with grok2api-egress-enhancements.
const (
	ClassHealthy = "healthy"
	ClassSoft    = "soft"
	ClassHard    = "hard"
	ClassError   = "error"
	ClassIgnored = "ignored"
)

func outputTokensPerSecond(outputTokens, durationMS, firstTokenMS int64) float64 {
	gen := durationMS - firstTokenMS
	if gen <= 0 || outputTokens <= 0 {
		return 0
	}
	return float64(outputTokens) / (float64(gen) / 1000.0)
}

func classifySpeed(cfg Config, speed float64, outputTokens, durationMS, firstTokenMS int64, expectedMatched *bool) (class, reason string) {
	if expectedMatched != nil && !*expectedMatched {
		return ClassSoft, "expected_marker_missing"
	}
	if outputTokens < int64(cfg.MinOutputTokens) {
		return ClassIgnored, "insufficient_output_tokens"
	}
	genMS := durationMS - firstTokenMS
	minGenMS := cfg.MinGeneration.Milliseconds()
	if genMS < minGenMS {
		if cfg.FailClosed && speed >= cfg.SoftTPS {
			return ClassSoft, "buffered_burst"
		}
		return ClassIgnored, "insufficient_generation_window"
	}
	if speed >= cfg.HardTPS {
		return ClassHard, "hard_tps"
	}
	if speed >= cfg.SoftTPS {
		return ClassSoft, "soft_tps"
	}
	return ClassHealthy, "ok"
}

// ProbeResult is produced by an active model probe.
type ProbeResult struct {
	OK                    bool    `json:"ok"`
	OutputTokens          int64   `json:"outputTokens"`
	DurationMS            int64   `json:"durationMs"`
	FirstTokenMS          int64   `json:"firstTokenMs"`
	OutputTokensPerSecond float64 `json:"outputTokensPerSecond"`
	ExpectedMatched       bool    `json:"expectedMatched"`
	ContentPreview        string  `json:"contentPreview,omitempty"`
	Error                 string  `json:"error,omitempty"`
}

func classifyProbe(cfg Config, result ProbeResult) (class, reason string) {
	if !result.OK {
		return ClassError, "probe_error"
	}
	matched := result.ExpectedMatched
	return classifySpeed(cfg, result.OutputTokensPerSecond, result.OutputTokens, result.DurationMS, result.FirstTokenMS, &matched)
}

// AuditSample is a minimal passive audit record (from local gateway if wired later).
type AuditSample struct {
	ID           string
	Provider     string
	Streaming    *bool
	Status       string
	StatusCode   int
	ErrorCode    string
	OutputTokens int64
	DurationMS   int64
	FirstTokenMS int64
	ExitIP       string
}

func classifyAudit(cfg Config, sample AuditSample) (class, reason string, speed float64, tokens int64) {
	provider := strings.ToLower(strings.TrimSpace(sample.Provider))
	if provider != "" && provider != "grok_build" && provider != "build" && provider != "grok" && provider != "unknown" {
		return ClassIgnored, "non_build_provider", 0, 0
	}
	if sample.Streaming != nil && !*sample.Streaming {
		return ClassIgnored, "non_streaming", 0, 0
	}
	if strings.TrimSpace(sample.ErrorCode) != "" {
		return ClassIgnored, "audit_error", 0, 0
	}
	if sample.StatusCode != 0 && (sample.StatusCode < 200 || sample.StatusCode >= 300) {
		return ClassIgnored, "non_success_status", 0, 0
	}
	status := strings.ToLower(strings.TrimSpace(sample.Status))
	if status != "" && status != "success" && status != "ok" && status != "completed" {
		return ClassIgnored, "non_success", 0, sample.OutputTokens
	}
	if sample.FirstTokenMS < 0 {
		return ClassIgnored, "missing_first_token", 0, sample.OutputTokens
	}
	speed = outputTokensPerSecond(sample.OutputTokens, sample.DurationMS, sample.FirstTokenMS)
	class, reason = classifySpeed(cfg, speed, sample.OutputTokens, sample.DurationMS, sample.FirstTokenMS, nil)
	return class, reason, speed, sample.OutputTokens
}
