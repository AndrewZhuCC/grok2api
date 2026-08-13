package resinquality

import (
	"io"
	"strings"
	"testing"
	"time"
)

// A tool-only turn used to block preflight until EOF or the 45s timeout, costing
// 30-40s of pure latency on live traffic. The terminal event must end it at once.
func TestPreflightReturnsAtTerminalEventForToolOnlyStream(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call"}}`,
		`data: {"type":"response.function_call_arguments.done"}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded {
		t.Fatalf("tool-only stream must stay healthy, verdict = %+v", v)
	}
	if v.Reason != "tool_only_stream" {
		t.Fatalf("reason = %q, want tool_only_stream", v.Reason)
	}
}

// Terminating with neither tools nor a signal is distinct from a tool-only turn,
// so the two keep separate reasons for diagnosis.
func TestPreflightReportsCompletionWithoutAnySignal(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "stream_completed_before_signal" {
		t.Fatalf("verdict = %+v, want healthy stream_completed_before_signal", v)
	}
}

// Cutting short at the terminal event must not weaken degradation detection:
// content ahead of the terminal event still wins.
func TestPreflightStillDegradesWhenContentPrecedesTerminal(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" {
		t.Fatalf("verdict = %+v, want degraded content_without_thinking", v)
	}
}

// The early return must not consume the rest of the stream: bytes after the
// terminal event stay unread so a healthy reply can still be replayed intact.
func TestPreflightDoesNotDrainBeyondTerminalEvent(t *testing.T) {
	trailing := `data: {"type":"response.output_text.delta","delta":"late"}` + "\n"
	source := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call"}}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n") + trailing

	reader := strings.NewReader(source)
	v, err := PreflightStream(io.NopCloser(reader), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Reason != "tool_only_stream" {
		t.Fatalf("reason = %q, want tool_only_stream", v.Reason)
	}
	// Preflight reads in chunks, so trailing bytes land in Buffered rather than
	// staying in the reader. What matters is that they are preserved for replay.
	if !strings.Contains(string(v.Buffered), "late") && reader.Len() == 0 {
		t.Fatal("trailing bytes must survive either in Buffered or the reader")
	}
}

// Chat streams signal completion via finish_reason instead of a type field.
func TestPreflightRecognizesChatFinishReasonAsTerminal(t *testing.T) {
	source := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolChat, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "tool_only_stream" {
		t.Fatalf("verdict = %+v, want healthy tool_only_stream", v)
	}
}

// A null finish_reason appears on every ordinary Chat chunk and must not be
// mistaken for completion.
func TestPreflightIgnoresNullChatFinishReason(t *testing.T) {
	source := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"thinking"},"finish_reason":null}]}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolChat, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Reason != "thinking_present" {
		t.Fatalf("reason = %q, want thinking_present", v.Reason)
	}
}
