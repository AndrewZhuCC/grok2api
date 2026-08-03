package resinquality

import (
	"testing"
	"time"
)

func TestClassifySpeedHard(t *testing.T) {
	cfg := Config{
		SoftTPS:         500,
		HardTPS:         1000,
		MinGeneration:   time.Second,
		MinOutputTokens: 32,
	}
	// 2000 tokens over 1s generation after 200ms ttft
	class, reason := classifySpeed(cfg, 2000, 2000, 1200, 200, nil)
	if class != ClassHard || reason != "hard_tps" {
		t.Fatalf("got %s/%s", class, reason)
	}
}

func TestClassifySpeedShortIgnored(t *testing.T) {
	cfg := Config{
		SoftTPS:         500,
		HardTPS:         1000,
		MinGeneration:   time.Second,
		MinOutputTokens: 32,
	}
	class, _ := classifySpeed(cfg, 5000, 200, 150, 50, nil)
	if class != ClassIgnored {
		t.Fatalf("expected ignored, got %s", class)
	}
}

func TestOutputTokensPerSecond(t *testing.T) {
	// 100 tokens / 200ms gen = 500 tps
	got := outputTokensPerSecond(100, 300, 100)
	if got < 499 || got > 501 {
		t.Fatalf("got %v", got)
	}
}

func TestClassifyAuditHealthy(t *testing.T) {
	cfg := Config{
		SoftTPS:         500,
		HardTPS:         1000,
		MinGeneration:   time.Second,
		MinOutputTokens: 32,
	}
	streaming := true
	// 200 tokens over 2s generation after 200ms ttft => 100 tps healthy
	class, reason, speed, tokens := classifyAudit(cfg, AuditSample{
		Provider:     "grok_build",
		Streaming:    &streaming,
		Status:       "success",
		StatusCode:   200,
		OutputTokens: 200,
		DurationMS:   2200,
		FirstTokenMS: 200,
	})
	if class != ClassHealthy || reason != "ok" {
		t.Fatalf("got %s/%s speed=%v tokens=%d", class, reason, speed, tokens)
	}
	if speed < 99 || speed > 101 {
		t.Fatalf("unexpected speed %v", speed)
	}
}

func TestClassifyAuditNonBuildIgnored(t *testing.T) {
	cfg := Config{SoftTPS: 500, HardTPS: 1000, MinGeneration: time.Second, MinOutputTokens: 32}
	streaming := true
	class, reason, _, _ := classifyAudit(cfg, AuditSample{
		Provider:     "grok_web",
		Streaming:    &streaming,
		StatusCode:   200,
		OutputTokens: 500,
		DurationMS:   3000,
		FirstTokenMS: 100,
	})
	if class != ClassIgnored || reason != "non_build_provider" {
		t.Fatalf("got %s/%s", class, reason)
	}
}

func TestClassifyAuditZeroReasoningSoftDespiteShortGen(t *testing.T) {
	cfg := Config{
		SoftTPS:           500,
		HardTPS:           1000,
		MinGeneration:     time.Second,
		MinOutputTokens:   32,
		ZeroReasoningSoft: true,
	}
	streaming := true
	// Real degraded pattern: long TTFT, tiny generation window, reasoningTokens=0.
	// TPS path alone would ignore; zero-reasoning OR must catch it as soft.
	class, reason, _, _ := classifyAudit(cfg, AuditSample{
		Provider:        "grok_build",
		Streaming:       &streaming,
		Status:          "success",
		StatusCode:      200,
		OutputTokens:    86,
		ReasoningTokens: 0,
		ReasoningKnown:  true,
		DurationMS:      9807,
		FirstTokenMS:    9796,
	})
	if class != ClassSoft || reason != "zero_reasoning" {
		t.Fatalf("got %s/%s", class, reason)
	}
}

func TestClassifyAuditZeroReasoningORHardTPS(t *testing.T) {
	cfg := Config{
		SoftTPS:           500,
		HardTPS:           1000,
		MinGeneration:     time.Second,
		MinOutputTokens:   32,
		ZeroReasoningSoft: true,
	}
	streaming := true
	class, reason, speed, _ := classifyAudit(cfg, AuditSample{
		Provider:        "grok_build",
		Streaming:       &streaming,
		Status:          "success",
		StatusCode:      200,
		OutputTokens:    2000,
		ReasoningTokens: 0,
		ReasoningKnown:  true,
		DurationMS:      2200,
		FirstTokenMS:    200,
	})
	if class != ClassHard || reason != "hard_tps" {
		t.Fatalf("got %s/%s speed=%v", class, reason, speed)
	}
}

func TestClassifyAuditReasoningPresentKeepsHealthy(t *testing.T) {
	cfg := Config{
		SoftTPS:           500,
		HardTPS:           1000,
		MinGeneration:     time.Second,
		MinOutputTokens:   32,
		ZeroReasoningSoft: true,
	}
	streaming := true
	class, reason, _, _ := classifyAudit(cfg, AuditSample{
		Provider:        "grok_build",
		Streaming:       &streaming,
		Status:          "success",
		StatusCode:      200,
		OutputTokens:    200,
		ReasoningTokens: 88,
		ReasoningKnown:  true,
		DurationMS:      2200,
		FirstTokenMS:    200,
	})
	if class != ClassHealthy || reason != "ok" {
		t.Fatalf("got %s/%s", class, reason)
	}
}
