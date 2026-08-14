package gateway

import (
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/chenyme/grok2api/backend/internal/domain/audit"
)

// newOnceFinalizer mirrors production: the audit write is guarded by sync.Once,
// so only the first caller decides the recorded error code.
func newOnceFinalizer(recorded *[]string) func(string) {
	var once sync.Once
	return func(code string) {
		once.Do(func() { *recorded = append(*recorded, code) })
	}
}

// Closing the body normally must still record the default stream_closed audit.
func TestFinalizingBodyRecordsDefaultOnClose(t *testing.T) {
	var recorded []string
	finalize := newOnceFinalizer(&recorded)
	body := &finalizingBody{
		ReadCloser: io.NopCloser(strings.NewReader("payload")),
		finalize:   func() { finalize("stream_closed") },
	}

	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(recorded) != 1 || recorded[0] != "stream_closed" {
		t.Fatalf("recorded = %v, want [stream_closed]", recorded)
	}
}

// Regression: a quality-guard interrupt records its own outcome and then closes
// the body. Without suppression, Close() claimed the sync.Once first and the
// interrupt was audited as the generic stream_closed — which is why the request
// list never showed status -1 or a stream_degraded_* reason.
func TestFinalizingBodySuppressionKeepsInterruptAudit(t *testing.T) {
	var recorded []string
	finalize := newOnceFinalizer(&recorded)
	body := &finalizingBody{
		ReadCloser: io.NopCloser(strings.NewReader("payload")),
		finalize:   func() { finalize("stream_closed") },
	}

	// Production order: record the real cause, suppress, then close.
	finalize("stream_degraded_content_without_thinking")
	SuppressCloseFinalize(body)
	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if len(recorded) != 1 {
		t.Fatalf("audit must be written exactly once, got %v", recorded)
	}
	if recorded[0] != "stream_degraded_content_without_thinking" {
		t.Fatalf("recorded = %q, want the interrupt code", recorded[0])
	}
}

// Suppression alone must not write an audit: it only disables the Close default.
func TestSuppressCloseFinalizeWritesNothingByItself(t *testing.T) {
	var recorded []string
	finalize := newOnceFinalizer(&recorded)
	body := &finalizingBody{
		ReadCloser: io.NopCloser(strings.NewReader("payload")),
		finalize:   func() { finalize("stream_closed") },
	}

	SuppressCloseFinalize(body)
	if err := body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded = %v, want no audit", recorded)
	}
}

// The helper takes an io.ReadCloser, so a body that is not a finalizingBody must
// be tolerated rather than panicking.
func TestSuppressCloseFinalizeIgnoresOtherReadClosers(t *testing.T) {
	plain := io.NopCloser(strings.NewReader("payload"))
	SuppressCloseFinalize(plain)
	if err := plain.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// isQualityGuardInterrupt drives the 599 audit status. stream_closed must not
// qualify, or the overwrite bug would silently look correct again.
func TestQualityGuardInterruptClassification(t *testing.T) {
	interrupts := []string{
		"stream_degraded_content_without_thinking",
		"stream_degraded_exhausted",
	}
	for _, code := range interrupts {
		if !isQualityGuardInterrupt(code) {
			t.Fatalf("%q must be a quality-guard interrupt", code)
		}
	}
	for _, code := range []string{"", "stream_closed", "upstream_stream_interrupted", "upstream_error"} {
		if isQualityGuardInterrupt(code) {
			t.Fatalf("%q must not be a quality-guard interrupt", code)
		}
	}
}

func TestAuditStatusForStreamFinalizeKeeps429WhenMixedWithInterrupt(t *testing.T) {
	rateLimited := 429
	attempts := []audit.Attempt{{
		Source:             audit.AttemptSourceUpstreamHTTP,
		Stage:              "upstream_response",
		UpstreamStatusCode: &rateLimited,
	}}
	got := auditStatusForStreamFinalize(200, "stream_degraded_content_without_thinking", attempts)
	if got != 429 {
		t.Fatalf("mixed 429+interrupt status = %d, want 429", got)
	}
}

func TestAuditStatusForStreamFinalizeMarksPureInterrupt(t *testing.T) {
	got := auditStatusForStreamFinalize(200, "stream_degraded_content_without_thinking", nil)
	if got != audit.StatusQualityGuardInterrupt {
		t.Fatalf("pure interrupt status = %d, want %d", got, audit.StatusQualityGuardInterrupt)
	}
}

func TestAuditStatusForStreamFinalizePreservesUpstreamWhenNotInterrupt(t *testing.T) {
	rateLimited := 429
	attempts := []audit.Attempt{{UpstreamStatusCode: &rateLimited}}
	if got := auditStatusForStreamFinalize(200, "", attempts); got != 200 {
		t.Fatalf("success status = %d, want 200", got)
	}
	if got := auditStatusForStreamFinalize(200, "stream_closed", attempts); got != 200 {
		t.Fatalf("stream_closed status = %d, want 200", got)
	}
}
