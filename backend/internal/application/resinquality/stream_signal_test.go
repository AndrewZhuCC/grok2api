package resinquality

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestClassifyChatThinking(t *testing.T) {
	data := []byte(`{"choices":[{"delta":{"reasoning_content":"step 1"}}]}`)
	if got := ClassifyStreamData(data, StreamProtocolChat); got != SignalThinking {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyChatContent(t *testing.T) {
	data := []byte(`{"choices":[{"delta":{"content":"hello"}}]}`)
	if got := ClassifyStreamData(data, StreamProtocolChat); got != SignalContent {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyChatToolIgnored(t *testing.T) {
	data := []byte(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"x","arguments":"{"}}]}}]}`)
	if got := ClassifyStreamData(data, StreamProtocolChat); got != SignalTool {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyAnthropicEmptyThinkingShellIgnored(t *testing.T) {
	// Empty thinking block start is a zero-reasoning shell — must not count.
	think := []byte(`{"type":"content_block_start","content_block":{"type":"thinking","thinking":""}}`)
	if got := ClassifyStreamData(think, StreamProtocolAnthropic); got != SignalNone {
		t.Fatalf("empty think start got %s, want none", got)
	}
	text := []byte(`{"type":"content_block_start","content_block":{"type":"text","text":""}}`)
	if got := ClassifyStreamData(text, StreamProtocolAnthropic); got != SignalContent {
		t.Fatalf("text start got %s", got)
	}
}

func TestClassifyAnthropicNonEmptyThinkingDelta(t *testing.T) {
	delta := []byte(`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"step"}}`)
	if got := ClassifyStreamData(delta, StreamProtocolAnthropic); got != SignalThinking {
		t.Fatalf("non-empty thinking_delta got %s", got)
	}
	empty := []byte(`{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":""}}`)
	if got := ClassifyStreamData(empty, StreamProtocolAnthropic); got != SignalNone {
		t.Fatalf("empty thinking_delta got %s, want none", got)
	}
}

func TestClassifyResponsesReasoning(t *testing.T) {
	data := []byte(`{"type":"response.reasoning_text.delta","delta":"plan"}`)
	if got := ClassifyStreamData(data, StreamProtocolResponses); got != SignalThinking {
		t.Fatalf("got %s", got)
	}
	out := []byte(`{"type":"response.output_text.delta","delta":"hi"}`)
	if got := ClassifyStreamData(out, StreamProtocolResponses); got != SignalContent {
		t.Fatalf("got %s", got)
	}
}

func TestClassifyResponsesEmptyReasoningItemIgnored(t *testing.T) {
	// Empty reasoning item shell must not count as thinking.
	for _, typ := range []string{"response.output_item.added", "response.output_item.done"} {
		data := []byte(`{"type":"` + typ + `","item":{"type":"reasoning"}}`)
		if got := ClassifyStreamData(data, StreamProtocolResponses); got != SignalNone {
			t.Fatalf("%s empty reasoning item got %s, want none", typ, got)
		}
	}
	emptyDelta := []byte(`{"type":"response.reasoning_text.delta","delta":""}`)
	if got := ClassifyStreamData(emptyDelta, StreamProtocolResponses); got != SignalNone {
		t.Fatalf("empty reasoning delta got %s, want none", got)
	}
}

func TestPreflightResponsesEmptyReasoningShellThenContentDegraded(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_1"}}`,
		``,
		`data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1"}}`,
		``,
		`data: {"type":"response.output_text.delta","delta":"hello without think"}`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolResponses, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" || v.FirstSignal != SignalContent {
		t.Fatalf("verdict=%+v", v)
	}
}

func TestPreflightAnthropicEmptyThinkingShellThenTextDegraded(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":""}}`,
		``,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolAnthropic, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" || v.FirstSignal != SignalContent {
		t.Fatalf("verdict=%+v", v)
	}
}

func TestPreflightAnthropicNonEmptyThinkingHealthy(t *testing.T) {
	body := strings.Join([]string{
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		``,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		``,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolAnthropic, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if v.Degraded || v.FirstSignal != SignalThinking || v.Reason != "thinking_present" {
		t.Fatalf("verdict=%+v", v)
	}
}

func TestPreflightDegradedContentFirst(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolChat, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" || v.FirstSignal != SignalContent {
		t.Fatalf("verdict=%+v", v)
	}
	if !strings.Contains(string(v.Buffered), `"content":"hello"`) {
		t.Fatalf("buffer missing content: %q", v.Buffered)
	}
}

func TestPreflightHealthyThinkingFirst(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"role":"assistant"}}]}`,
		``,
		`data: {"choices":[{"delta":{"reasoning_content":"think"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"answer"}}]}`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolChat, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if v.Degraded || v.FirstSignal != SignalThinking || v.Reason != "thinking_present" {
		t.Fatalf("verdict=%+v", v)
	}
}

func TestPreflightSkipsToolThenContentIsDegraded(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"no think"}}]}`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolChat, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Degraded || v.Reason != "content_without_thinking" {
		t.Fatalf("verdict=%+v", v)
	}
}

func TestPreflightSkipsToolThenThinkingHealthy(t *testing.T) {
	body := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"reasoning_content":"ok"}}]}`,
		``,
	}, "\n")
	v, err := PreflightStream(strings.NewReader(body), StreamProtocolChat, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if v.Degraded || v.FirstSignal != SignalThinking {
		t.Fatalf("verdict=%+v", v)
	}
}

func TestReplayBody(t *testing.T) {
	live := io.NopCloser(strings.NewReader("TAIL"))
	body := NewReplayBody([]byte("HEAD"), live)
	defer body.Close()
	all, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	if string(all) != "HEADTAIL" {
		t.Fatalf("got %q", all)
	}
}
