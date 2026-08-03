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
