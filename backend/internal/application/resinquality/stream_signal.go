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
		Type  string `json:"type"`
		Delta string `json:"delta"`
		Item  struct {
			Type string `json:"type"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil {
		return SignalNone
	}
	switch event.Type {
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if strings.TrimSpace(event.Delta) != "" {
			return SignalThinking
		}
	case "response.output_text.delta", "response.refusal.delta":
		if strings.TrimSpace(event.Delta) != "" {
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
	verdict := StreamWatchVerdict{FirstSignal: SignalNone}

	for {
		if time.Now().After(deadline) {
			verdict.Buffered = buf.Bytes()
			verdict.PeekMS = time.Since(started).Milliseconds()
			// Timeout with no semantic signal: treat as non-degraded (don't false-positive).
			verdict.Reason = "preflight_timeout"
			return verdict, nil
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
						verdict.Buffered = buf.Bytes()
						verdict.PeekMS = time.Since(started).Milliseconds()
						verdict.Reason = "preflight_line_too_long"
						return verdict, nil
					}
					break
				}
				line := bytes.TrimSpace(pending[:idx])
				pending = pending[idx+1:]
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				kind := ClassifyStreamData(payload, protocol)
				switch kind {
				case SignalNone:
					continue
				case SignalTool:
					// Ignore tools; keep watching for thinking/content.
					continue
				case SignalThinking:
					sawThinking = true
					verdict.FirstSignal = SignalThinking
					verdict.Buffered = buf.Bytes()
					verdict.PeekMS = time.Since(started).Milliseconds()
					verdict.Reason = "thinking_present"
					return verdict, nil
				case SignalContent:
					verdict.FirstSignal = SignalContent
					verdict.Buffered = buf.Bytes()
					verdict.PeekMS = time.Since(started).Milliseconds()
					if sawThinking {
						verdict.Reason = "content_after_thinking"
						return verdict, nil
					}
					verdict.Degraded = true
					verdict.Reason = "content_without_thinking"
					return verdict, nil
				default:
					verdict.FirstSignal = kind
					verdict.Buffered = buf.Bytes()
					verdict.PeekMS = time.Since(started).Milliseconds()
					verdict.Reason = "other_signal"
					return verdict, nil
				}
			}
		}
		if readErr != nil {
			verdict.Buffered = buf.Bytes()
			verdict.PeekMS = time.Since(started).Milliseconds()
			if readErr == io.EOF {
				verdict.Reason = "stream_eof_before_signal"
				return verdict, nil
			}
			return verdict, readErr
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
