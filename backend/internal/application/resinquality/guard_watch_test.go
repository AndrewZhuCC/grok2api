package resinquality

import (
	"path/filepath"
	"testing"
)

func TestStreamWatchEnabledOverride(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Enabled:            true,
		ResinBaseURL:       "http://127.0.0.1:2260",
		ResinAdminToken:    "test-token",
		StreamWatchEnabled: true,
		StreamMaxAttempts:  3,
		StateFile:          filepath.Join(dir, "state.json"),
	}
	g := NewGuard(cfg, nil, nil, nil)
	if !g.StreamWatchEnabled() {
		t.Fatal("expected env default stream watch on")
	}

	if got := g.UpdateStreamWatchEnabled(false); got {
		t.Fatal("expected false after disable")
	}
	if g.StreamWatchEnabled() {
		t.Fatal("UI off override must disable interrupt/retry path")
	}
	status := g.GetStatus(t.Context())
	if status.Config.StreamWatchEnabled {
		t.Fatal("public config must reflect effective off")
	}

	if got := g.UpdateStreamWatchEnabled(true); !got {
		t.Fatal("expected true after enable")
	}
	if !g.StreamWatchEnabled() {
		t.Fatal("UI on override must re-enable stream watch")
	}

	// Reload from disk: override must persist across process restart.
	g2 := NewGuard(cfg, nil, nil, nil)
	if !g2.StreamWatchEnabled() {
		t.Fatal("persisted on override should survive reload")
	}
	g2.UpdateStreamWatchEnabled(false)
	g3 := NewGuard(cfg, nil, nil, nil)
	if g3.StreamWatchEnabled() {
		t.Fatal("persisted off override should survive reload")
	}
}

func TestStreamWatchEnabledRequiresCanRun(t *testing.T) {
	g := NewGuard(Config{Enabled: true, StreamWatchEnabled: true}, nil, nil, nil)
	if g.StreamWatchEnabled() {
		t.Fatal("missing resin token must keep stream watch off")
	}
}
