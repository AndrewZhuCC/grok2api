package resinquality

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// decodeLines parses the JSON log records emitted into buf.
func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	records := make([]map[string]any, 0, 4)
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not JSON: %q (%v)", line, err)
		}
		records = append(records, record)
	}
	return records
}

func guardWithBuffer(buf *bytes.Buffer, level slog.Level) *Guard {
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
	return NewGuard(Config{}, logger, nil, nil)
}

// A pass-through verdict must still be logged. These are the requests that
// previously left no trace at all, which made the timeout/EOF share unknowable.
func TestLogStreamVerdictEmitsPassThroughAtInfo(t *testing.T) {
	var buf bytes.Buffer
	g := guardWithBuffer(&buf, slog.LevelInfo)

	g.LogStreamVerdict(false, StreamDegradedMeta{
		RequestID: "req-1", Model: "grok-4.5", Protocol: StreamProtocolResponses,
		Provider: "grok_build", FirstSignal: SignalNone, Reason: "preflight_timeout",
		PeekMS: 45000, Attempt: 1, MaxAttempts: 5,
		DataFrames: 7, EventTrace: []string{"response.created", "response.in_progress"},
	})

	records := decodeLines(t, &buf)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	record := records[0]
	if record["msg"] != "stream_watch_verdict" {
		t.Fatalf("msg = %v", record["msg"])
	}
	if record["level"] != "INFO" {
		t.Fatalf("pass-through verdict must be visible at info, level = %v", record["level"])
	}
	if record["degraded"] != false {
		t.Fatalf("degraded = %v, want false", record["degraded"])
	}
	if record["reason"] != "preflight_timeout" {
		t.Fatalf("reason = %v", record["reason"])
	}
	// The trace is joined with ">" so one grep-able field shows event ordering.
	if record["event_trace"] != "response.created>response.in_progress" {
		t.Fatalf("event_trace = %v", record["event_trace"])
	}
	if record["data_frames"] != float64(7) {
		t.Fatalf("data_frames = %v, want 7", record["data_frames"])
	}
}

// A degraded verdict carries the same fields, so both outcomes are comparable
// from one log query rather than needing two different shapes.
func TestLogStreamVerdictEmitsDegradedWithTrace(t *testing.T) {
	var buf bytes.Buffer
	g := guardWithBuffer(&buf, slog.LevelInfo)

	g.LogStreamVerdict(true, StreamDegradedMeta{
		RequestID: "req-2", AccountID: 4321, AccountName: "acct", Model: "grok-4.5",
		Protocol: StreamProtocolResponses, Provider: "grok_build",
		FirstSignal: SignalContent, Reason: "content_without_thinking",
		PeekMS: 16770, Attempt: 2, MaxAttempts: 5, DataFrames: 3,
		EventTrace: []string{"response.created", "response.output_text.delta"},
	})

	record := decodeLines(t, &buf)[0]
	if record["degraded"] != true {
		t.Fatalf("degraded = %v, want true", record["degraded"])
	}
	if record["first_signal"] != SignalContent {
		t.Fatalf("first_signal = %v", record["first_signal"])
	}
	if record["account_id"] != float64(4321) {
		t.Fatalf("account_id = %v", record["account_id"])
	}
	if record["event_trace"] != "response.created>response.output_text.delta" {
		t.Fatalf("event_trace = %v", record["event_trace"])
	}
}

// An empty trace must stay distinguishable from a missing field: paired with
// data_frames it tells whether payloads arrived but matched no known type.
func TestLogStreamVerdictKeepsEmptyTraceExplicit(t *testing.T) {
	var buf bytes.Buffer
	g := guardWithBuffer(&buf, slog.LevelInfo)

	g.LogStreamVerdict(false, StreamDegradedMeta{
		RequestID: "req-3", Reason: "stream_eof_before_signal", DataFrames: 0,
	})

	record := decodeLines(t, &buf)[0]
	trace, ok := record["event_trace"]
	if !ok {
		t.Fatal("event_trace must always be present, even when empty")
	}
	if trace != "" {
		t.Fatalf("event_trace = %v, want empty string", trace)
	}
	if record["data_frames"] != float64(0) {
		t.Fatalf("data_frames = %v, want 0", record["data_frames"])
	}
}

// The skip log explains traffic that never reaches preflight. It is debug-level,
// so it stays silent under the default level and requires LOG_LEVEL=debug.
func TestLogStreamWatchSkippedIsDebugLevel(t *testing.T) {
	var infoBuf bytes.Buffer
	guardWithBuffer(&infoBuf, slog.LevelInfo).
		LogStreamWatchSkipped("stream_watch_disabled", "req-4", StreamProtocolResponses, "grok_build", "grok-4.5", 200)
	if strings.TrimSpace(infoBuf.String()) != "" {
		t.Fatalf("skip log must be silent at info, got %q", infoBuf.String())
	}

	var debugBuf bytes.Buffer
	guardWithBuffer(&debugBuf, slog.LevelDebug).
		LogStreamWatchSkipped("stream_watch_disabled", "req-4", StreamProtocolResponses, "grok_build", "grok-4.5", 200)

	record := decodeLines(t, &debugBuf)[0]
	if record["msg"] != "stream_watch_skipped" {
		t.Fatalf("msg = %v", record["msg"])
	}
	if record["level"] != "DEBUG" {
		t.Fatalf("level = %v, want DEBUG", record["level"])
	}
	if record["reason"] != "stream_watch_disabled" {
		t.Fatalf("reason = %v", record["reason"])
	}
}

// A nil guard must never panic: the handler calls these on a path where the
// guard can legitimately be absent.
func TestStreamDiagnosticLogsTolerateNilGuard(t *testing.T) {
	var g *Guard
	g.LogStreamVerdict(true, StreamDegradedMeta{RequestID: "req-5"})
	g.LogStreamWatchSkipped("guard_absent", "req-5", StreamProtocolChat, "", "", 0)
}
