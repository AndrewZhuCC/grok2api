package resinquality

import (
	"io"
	"strings"
	"testing"
	"time"
)

// The trace exists to answer one question the old logs could not: when a stream
// is judged content_without_thinking, did the upstream really send no reasoning
// event, or did it send one under a name the classifier does not recognize?
func TestPreflightTraceRecordsResponsesReasoningBeforeContent(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.output_item.added","item":{"type":"reasoning"}}`,
		`data: {"type":"response.reasoning_text.delta","delta":"weighing"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "thinking_present" {
		t.Fatalf("verdict = %+v, want healthy thinking_present", v)
	}
	// The empty reasoning shell must stay visible and distinguishable from the
	// real delta, otherwise the trace cannot explain a misjudgement.
	want := "response.created>response.output_item.added:reasoning>response.reasoning_text.delta"
	if got := strings.Join(v.EventTrace, ">"); got != want {
		t.Fatalf("event_trace = %q, want %q", got, want)
	}
	if v.DataFrames != 3 {
		t.Fatalf("data_frames = %d, want 3", v.DataFrames)
	}
}

// A degraded verdict must carry the frames it saw. An empty trace with a
// non-zero frame count would mean the payloads parsed but matched no known type.
func TestPreflightTraceOnDegradedResponsesStream(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.output_item.added","item":{"type":"message"}}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" {
		t.Fatalf("verdict = %+v, want degraded content_without_thinking", v)
	}
	want := "response.created>response.output_item.added:message>response.output_text.delta"
	if got := strings.Join(v.EventTrace, ">"); got != want {
		t.Fatalf("event_trace = %q, want %q", got, want)
	}
}

// Anthropic sends an empty thinking shell before any thinking_delta. The trace
// must separate the shell from the delta so a false interrupt is diagnosable.
func TestPreflightTraceSeparatesAnthropicThinkingShellFromDelta(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hm"}}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolAnthropic, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "thinking_present" {
		t.Fatalf("verdict = %+v, want healthy thinking_present", v)
	}
	want := "content_block_start:thinking>content_block_delta:thinking_delta"
	if got := strings.Join(v.EventTrace, ">"); got != want {
		t.Fatalf("event_trace = %q, want %q", got, want)
	}
}

// Chat chunks have no type field, so the trace reports delta field presence and
// marks blank values — an empty reasoning field must not read as a real signal.
func TestPreflightTraceReportsChatDeltaFieldsAndEmptiness(t *testing.T) {
	source := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning":""}}]}`,
		`data: {"choices":[{"delta":{"content":"hello"}}]}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolChat, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" {
		t.Fatalf("verdict = %+v, want degraded content_without_thinking", v)
	}
	want := "reasoning(empty)>content"
	if got := strings.Join(v.EventTrace, ">"); got != want {
		t.Fatalf("event_trace = %q, want %q", got, want)
	}
}

// Pass-through verdicts are the ones that previously left no trace at all, so
// they must still report what was consumed before the stream ended.
func TestPreflightTraceOnEOFPassThrough(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.in_progress"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "stream_eof_before_signal" {
		t.Fatalf("verdict = %+v, want pass-through stream_eof_before_signal", v)
	}
	if got := strings.Join(v.EventTrace, ">"); got != "response.created>response.in_progress" {
		t.Fatalf("event_trace = %q", got)
	}
	if v.DataFrames != 2 {
		t.Fatalf("data_frames = %d, want 2", v.DataFrames)
	}
}

// Consecutive duplicates collapse so a long delta run stays one entry, and the
// trace stops growing instead of expanding with the stream.
func TestPreflightTraceCollapsesDuplicatesAndBoundsLength(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5; i++ {
		b.WriteString(`data: {"type":"response.created"}` + "\n")
	}
	for i := 0; i < maxEventTraceEntries+10; i++ {
		// Alternate two names so every frame is a new entry.
		if i%2 == 0 {
			b.WriteString(`data: {"type":"response.in_progress"}` + "\n")
		} else {
			b.WriteString(`data: {"type":"response.created"}` + "\n")
		}
	}

	v, err := PreflightStream(io.NopCloser(strings.NewReader(b.String())), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(v.EventTrace) > maxEventTraceEntries+1 {
		t.Fatalf("trace length = %d, want <= %d", len(v.EventTrace), maxEventTraceEntries+1)
	}
	if v.EventTrace[len(v.EventTrace)-1] != eventTraceTruncated {
		t.Fatalf("trace must end with truncation marker, got %q", v.EventTrace[len(v.EventTrace)-1])
	}
	if v.EventTrace[0] != "response.created" || v.EventTrace[1] != "response.in_progress" {
		t.Fatalf("trace head = %v, want duplicates collapsed", v.EventTrace[:2])
	}
}

// The trace must never carry model output. Only type names and field names are
// allowed, so distinctive payload text must be absent from every entry.
func TestPreflightTraceExcludesUserPayload(t *testing.T) {
	secret := "SENSITIVE_PAYLOAD_MARKER"
	source := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"` + secret + `"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if strings.Contains(strings.Join(v.EventTrace, ">"), secret) {
		t.Fatal("event_trace leaked delta content")
	}
}
