package resinquality

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"time"
)

// Stream protocol labels used by the inference layer.
const (
	StreamProtocolResponses = "responses"
	StreamProtocolChat      = "chat"
	StreamProtocolAnthropic = "anthropic"
)

// First-signal kinds observed on a converted SSE body.
const (
	SignalNone     = "none"
	SignalThinking = "thinking"
	SignalContent  = "content"
	SignalTool     = "tool"
	SignalOther    = "other"
)

// StreamWatchVerdict is the preflight classification of a stream body.
type StreamWatchVerdict struct {
	// Degraded is true when the first semantic signal is content without prior thinking.
	Degraded bool
	// FirstSignal is thinking|content|tool|other|none.
	FirstSignal string
	// Reason is a stable machine code for stats/UI (e.g. content_without_thinking).
	Reason string
	// Buffered holds all bytes consumed during preflight (must be replayed to the client if healthy).
	Buffered []byte
	// PeekMS is how long preflight waited for the first semantic signal.
	PeekMS int64
	// EventTrace lists the SSE event type names seen during preflight, in order,
	// with consecutive duplicates collapsed. Diagnostics only: it never carries
	// delta text, tool arguments, or any other user payload.
	EventTrace []string
	// DataFrames counts "data:" frames consumed during preflight, including the
	// ones classified as SignalNone. A high count with an empty EventTrace means
	// the payloads parsed but carried no recognized event type.
	DataFrames int
}

// maxEventTraceEntries bounds the diagnostic trace so a chatty stream cannot
// grow the verdict without limit. Overflow is reported via the truncation marker.
const maxEventTraceEntries = 24

// eventTraceTruncated marks a trace that hit maxEventTraceEntries.
const eventTraceTruncated = "…"

// streamEventName extracts only the discriminator field of an SSE payload for
// diagnostics: Responses uses "type", Anthropic uses "type", Chat has none and
// is reported by its delta field names. No user content is read.
func streamEventName(data []byte, protocol string) string {
	switch protocol {
	case StreamProtocolChat:
		return chatDeltaFieldName(data)
	default:
		// Delta stays raw: Anthropic sends an object, Responses sends a string.
		// Decoding it into a fixed shape would fail the whole payload and report
		// every Responses delta as unparsable.
		var event struct {
			Type         string `json:"type"`
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
			Delta json.RawMessage `json:"delta"`
			Item  struct {
				Type string `json:"type"`
			} `json:"item"`
		}
		if json.Unmarshal(data, &event) != nil {
			return "unparsable"
		}
		name := strings.TrimSpace(event.Type)
		if name == "" {
			return "no_type"
		}
		// Qualify the container events whose meaning depends on a nested type,
		// so the trace distinguishes e.g. an empty thinking shell from text.
		switch name {
		case "content_block_start", "content_block_stop":
			if inner := strings.TrimSpace(event.ContentBlock.Type); inner != "" {
				return name + ":" + inner
			}
		case "content_block_delta":
			if inner := deltaTypeName(event.Delta); inner != "" {
				return name + ":" + inner
			}
		case "response.output_item.added", "response.output_item.done":
			if inner := strings.TrimSpace(event.Item.Type); inner != "" {
				return name + ":" + inner
			}
		}
		return name
	}
}

// deltaTypeName reads the nested delta discriminator when the delta is an object
// (Anthropic). A string delta (Responses) has no inner type and yields "".
func deltaTypeName(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var inner struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &inner) != nil {
		return ""
	}
	return strings.TrimSpace(inner.Type)
}

// chatDeltaFieldName reports which delta fields are present in a Chat chunk.
// Only field presence is inspected; values are never copied into the trace.
func chatDeltaFieldName(data []byte) string {
	var event struct {
		Choices []struct {
			Delta struct {
				Content          *string         `json:"content"`
				Reasoning        *string         `json:"reasoning"`
				ReasoningContent *string         `json:"reasoning_content"`
				Refusal          *string         `json:"refusal"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &event) != nil {
		return "unparsable"
	}
	if len(event.Choices) == 0 {
		return "no_choices"
	}
	d := event.Choices[0].Delta
	fields := make([]string, 0, 4)
	if len(bytes.TrimSpace(d.ToolCalls)) > 0 && !bytes.Equal(bytes.TrimSpace(d.ToolCalls), []byte("null")) {
		fields = append(fields, "tool_calls")
	}
	if d.Reasoning != nil {
		fields = append(fields, emptyQualifiedField("reasoning", *d.Reasoning))
	}
	if d.ReasoningContent != nil {
		fields = append(fields, emptyQualifiedField("reasoning_content", *d.ReasoningContent))
	}
	if d.Content != nil {
		fields = append(fields, emptyQualifiedField("content", *d.Content))
	}
	if d.Refusal != nil {
		fields = append(fields, emptyQualifiedField("refusal", *d.Refusal))
	}
	if len(fields) == 0 {
		return "empty_delta"
	}
	return strings.Join(fields, "+")
}

// emptyQualifiedField reports a field name plus whether it was blank, which is
// what distinguishes a real signal from an empty shell. The value is not copied.
func emptyQualifiedField(name, value string) string {
	if strings.TrimSpace(value) == "" {
		return name + "(empty)"
	}
	return name
}

// appendTrace records an event name, collapsing consecutive duplicates so a long
// delta run stays one entry, and stops growing at maxEventTraceEntries.
func appendTrace(trace []string, name string) []string {
	if name == "" {
		return trace
	}
	if n := len(trace); n > 0 {
		if trace[n-1] == name || trace[n-1] == eventTraceTruncated {
			return trace
		}
	}
	if len(trace) >= maxEventTraceEntries {
		return append(trace, eventTraceTruncated)
	}
	return append(trace, name)
}

// ClassifyStreamData classifies one SSE data payload (without the "data:" prefix).
// tool signals are ignored for degradation (return SignalTool).
func ClassifyStreamData(data []byte, protocol string) string {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return SignalNone
	}
	switch protocol {
	case StreamProtocolChat:
		return classifyChatDelta(data)
	case StreamProtocolAnthropic:
		return classifyAnthropicEvent(data)
	case StreamProtocolResponses:
		return classifyResponsesEvent(data)
	default:
		return SignalNone
	}
}

func classifyChatDelta(data []byte) string {
	var event struct {
		Choices []struct {
			Delta struct {
				Content          string          `json:"content"`
				Reasoning        string          `json:"reasoning"`
				ReasoningContent string          `json:"reasoning_content"`
				Refusal          string          `json:"refusal"`
				ToolCalls        json.RawMessage `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &event) != nil {
		return SignalNone
	}
	for _, choice := range event.Choices {
		d := choice.Delta
		if len(bytes.TrimSpace(d.ToolCalls)) > 0 && !bytes.Equal(bytes.TrimSpace(d.ToolCalls), []byte("null")) {
			return SignalTool
		}
		if strings.TrimSpace(d.Reasoning) != "" || strings.TrimSpace(d.ReasoningContent) != "" {
			return SignalThinking
		}
		if strings.TrimSpace(d.Content) != "" || strings.TrimSpace(d.Refusal) != "" {
			return SignalContent
		}
	}
	return SignalNone
}

func classifyAnthropicEvent(data []byte) string {
	var event struct {
		Type         string `json:"type"`
		ContentBlock struct {
			Type string `json:"type"`
		} `json:"content_block"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if json.Unmarshal(data, &event) != nil {
		return SignalNone
	}
	switch event.Type {
	case "content_block_start":
		switch event.ContentBlock.Type {
		case "thinking":
			// Empty thinking shell must NOT count as thinking. Only non-empty
			// thinking_delta text is a real signal (zero-reasoning bypass guard).
			return SignalNone
		case "text":
			// Empty text block start alone is not yet content; wait for delta.
			// But starting text before any thinking is the degradation signal.
			return SignalContent
		case "tool_use", "server_tool_use", "web_search_tool_result":
			return SignalTool
		}
	case "content_block_delta":
		switch event.Delta.Type {
		case "thinking_delta":
			if strings.TrimSpace(event.Delta.Thinking) != "" {
				return SignalThinking
			}
		case "text_delta":
			if strings.TrimSpace(event.Delta.Text) != "" {
				return SignalContent
			}
		case "input_json_delta":
			return SignalTool
		}
	}
	return SignalNone
}

func classifyResponsesEvent(data []byte) string {
	var event struct {
		Type  string          `json:"type"`
		Delta json.RawMessage `json:"delta"`
		Item  struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil {
		return SignalNone
	}
	delta := responsesDeltaText(event.Delta)
	switch event.Type {
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if delta != "" {
			return SignalThinking
		}
	case "response.output_text.delta", "response.refusal.delta":
		if delta != "" {
			return SignalContent
		}
	case "response.output_item.added", "response.output_item.done":
		switch event.Item.Type {
		case "reasoning":
			// Empty reasoning item shell must NOT count as thinking. Real
			// thinking requires non-empty reasoning_*_text.delta content.
			return SignalNone
		case "function_call", "web_search_call", "custom_tool_call":
			return SignalTool
		case "message":
			// message item without text yet — ignore until output_text.delta
			return SignalNone
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		return SignalTool
	}
	return SignalNone
}

// responsesDeltaText accepts both official string deltas and object-shaped
// payloads some Grok 4.6 builds emit ({"text":"..."}). Empty shells stay blank.
func terminalReason(sawThinking, sawTool, sawContent bool) string {
	switch {
	case sawThinking:
		return "thinking_present"
	case sawTool && sawContent:
		return "content_then_tool"
	case sawTool:
		return "tool_only_stream"
	case sawContent:
		return "content_without_thinking"
	default:
		return "stream_completed_before_signal"
	}
}

func responsesDeltaText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strings.TrimSpace(text)
	}
	var object struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &object) == nil {
		return strings.TrimSpace(object.Text)
	}
	return ""
}

// isStreamTerminalEvent reports whether a payload ends the reply, meaning no
// thinking or content signal can still arrive. Used to cut preflight short
// instead of blocking until EOF or the timeout.
func isStreamTerminalEvent(data []byte, protocol string) bool {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("[DONE]")) {
		return true
	}
	switch protocol {
	case StreamProtocolChat:
		// Chat marks completion with a finish_reason on the choice.
		var event struct {
			Choices []struct {
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &event) != nil {
			return false
		}
		for _, choice := range event.Choices {
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				return true
			}
		}
		return false
	default:
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &event) != nil {
			return false
		}
		switch strings.TrimSpace(event.Type) {
		case "response.completed", "response.incomplete", "response.failed",
			"message_stop", "error":
			return true
		}
		return false
	}
}

// PreflightStream reads from source until the first semantic signal (or timeout/EOF),
// buffering all consumed bytes. Tool signals are skipped. Content-without-thinking is degraded.
//
// The caller MUST:
//   - if !Degraded: replay verdict.Buffered then continue reading source
//   - if Degraded: close source and retry (do not send buffered bytes to the client)
func PreflightStream(source io.Reader, protocol string, timeout time.Duration) (StreamWatchVerdict, error) {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	started := time.Now()
	deadline := started.Add(timeout)
	var buf bytes.Buffer
	pending := make([]byte, 0, 4096)
	chunk := make([]byte, 32<<10)
	sawThinking := false
	sawTool := false
	sawContent := false
	verdict := StreamWatchVerdict{FirstSignal: SignalNone}
	trace := make([]string, 0, 8)
	dataFrames := 0
	// finish stamps the diagnostic fields shared by every return path.
	finish := func(reason string) StreamWatchVerdict {
		verdict.Buffered = buf.Bytes()
		verdict.PeekMS = time.Since(started).Milliseconds()
		verdict.EventTrace = trace
		verdict.DataFrames = dataFrames
		if reason != "" {
			verdict.Reason = reason
		}
		verdict.Degraded = reason == "content_without_thinking"
		return verdict
	}

	for {
		if time.Now().After(deadline) {
			if sawContent || sawTool || sawThinking {
				return finish(terminalReason(sawThinking, sawTool, sawContent)), nil
			}
			// Timeout with no semantic signal: treat as non-degraded (don't false-positive).
			return finish("preflight_timeout"), nil
		}
		// Best-effort deadline via short reads isn't possible on plain Reader;
		// rely on upstream/request context closing the body. We still bound wall time
		// by checking deadline each loop after a read returns.
		n, readErr := source.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			pending = append(pending, chunk[:n]...)
			for {
				idx := bytes.IndexByte(pending, '\n')
				if idx < 0 {
					if len(pending) > 8<<20 {
						// Pathological line — give up watching, pass through.
						return finish("preflight_line_too_long"), nil
					}
					break
				}
				line := bytes.TrimSpace(pending[:idx])
				pending = pending[idx+1:]
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				dataFrames++
				trace = appendTrace(trace, streamEventName(payload, protocol))
				// A terminal event means the reply is over. Waiting for a
				// thinking/content signal that can no longer arrive just burns
				// wall time — tool-only turns were stalling here for 30-40s.
				if isStreamTerminalEvent(payload, protocol) {
					return finish(terminalReason(sawThinking, sawTool, sawContent)), nil
				}
				kind := ClassifyStreamData(payload, protocol)
				switch kind {
				case SignalNone:
					continue
				case SignalTool:
					// Grok 4.6 often emits a short visible preface, then a
					// function call. That is a tool turn, not a degraded
					// final answer. Keep watching until thinking or the
					// terminal event decides the request.
					sawTool = true
					if verdict.FirstSignal == SignalNone {
						verdict.FirstSignal = SignalTool
					}
					continue
				case SignalThinking:
					sawThinking = true
					if verdict.FirstSignal == SignalNone {
						verdict.FirstSignal = SignalThinking
					}
					return finish("thinking_present"), nil
				case SignalContent:
					sawContent = true
					if verdict.FirstSignal == SignalNone {
						verdict.FirstSignal = SignalContent
					}
					// Do not interrupt on the first content token. 4.6 tool
					// turns commonly leak a preface before the function call.
					continue
				default:
					if verdict.FirstSignal == SignalNone {
						verdict.FirstSignal = kind
					}
					return finish("other_signal"), nil
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if sawContent || sawTool || sawThinking {
					return finish(terminalReason(sawThinking, sawTool, sawContent)), nil
				}
				return finish("stream_eof_before_signal"), nil
			}
			return finish(""), readErr
		}
	}
}

// multiReaderBody replays buffered bytes then continues from the live source.
type multiReaderBody struct {
	reader io.Reader
	closer io.Closer
}

// NewReplayBody returns a ReadCloser that first yields buffered preflight bytes,
// then continues reading from live. Closing closes live.
func NewReplayBody(buffered []byte, live io.ReadCloser) io.ReadCloser {
	if len(buffered) == 0 {
		return live
	}
	return &multiReaderBody{
		reader: io.MultiReader(bytes.NewReader(buffered), live),
		closer: live,
	}
}

func (m *multiReaderBody) Read(p []byte) (int, error) { return m.reader.Read(p) }
func (m *multiReaderBody) Close() error {
	if m.closer != nil {
		return m.closer.Close()
	}
	return nil
}
