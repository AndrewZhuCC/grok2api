package resinquality

import (
	"io"
	"strings"
	"testing"
	"time"
)

// Degradation policy, fixed by decision:
//   - no reasoning at all, or reasoning of length 0, followed by content -> interrupt
//   - a completed stream that only carries tool calls -> NOT degraded, pass through
//
// Grok cannot disable thinking, so content with no preceding thinking text is a
// degraded reply. A tool-only turn legitimately carries no visible reasoning.

// An empty reasoning item shell must not satisfy the thinking requirement.
func TestPolicyEmptyReasoningShellThenContentIsDegraded(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"reasoning"}}`,
		`data: {"type":"response.output_item.done","item":{"type":"reasoning"}}`,
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

// A reasoning delta whose text is blank is length-0 reasoning: still degraded.
func TestPolicyBlankReasoningDeltaThenContentIsDegraded(t *testing.T) {
	for _, blank := range []string{"", " ", "\\n"} {
		source := strings.Join([]string{
			`data: {"type":"response.reasoning_text.delta","delta":"` + blank + `"}`,
			`data: {"type":"response.output_text.delta","delta":"answer"}`,
			`data: {"type":"response.completed"}`,
			"",
		}, "\n")

		v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
		if err != nil {
			t.Fatalf("preflight(%q): %v", blank, err)
		}
		if !v.Degraded || v.Reason != "content_without_thinking" {
			t.Fatalf("blank delta %q: verdict = %+v, want degraded", blank, v)
		}
	}
}

// Content with no reasoning event whatsoever is the baseline degraded case.
func TestPolicyContentWithNoReasoningIsDegraded(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.output_item.added","item":{"type":"message"}}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !v.Degraded {
		t.Fatalf("verdict = %+v, want degraded", v)
	}
}

// Real reasoning text before content is healthy, and must stay healthy.
func TestPolicyReasoningTextThenContentIsHealthy(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.reasoning_text.delta","delta":"thinking it through"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "thinking_present" {
		t.Fatalf("verdict = %+v, want healthy thinking_present", v)
	}
}

// A tool-only turn carries zero reasoning and no content. By decision this is
// NOT degraded: the request never produced visible text to judge.
func TestPolicyToolOnlyCompletedStreamIsNotDegraded(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call"}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"a\":1}"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call"}}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded {
		t.Fatalf("tool-only stream must not be degraded, verdict = %+v", v)
	}
}

// Tool calls must not mask a later degradation: if content still arrives with no
// reasoning, the interrupt applies even though tools came first.
func TestPolicyToolCallThenContentWithoutReasoningIsDegraded(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.output_item.added","item":{"type":"function_call"}}`,
		`data: {"type":"response.function_call_arguments.done"}`,
		`data: {"type":"response.output_text.delta","delta":"answer"}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "content_then_tool" {
		t.Fatalf("verdict = %+v, want healthy content_then_tool", v)
	}
}

// Grok 4.6 tool turns often emit a short visible preface before the function
// call. That must stay healthy; interrupting it aborts every tool request.
func TestPolicyContentPrefaceThenToolIsHealthy(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.created"}`,
		`data: {"type":"response.in_progress"}`,
		`data: {"type":"response.output_item.added","item":{"type":"message"}}`,
		`data: {"type":"response.content_part.added"}`,
		`data: {"type":"response.output_text.delta","delta":"I'll look that up."}`,
		`data: {"type":"response.output_item.added","item":{"type":"function_call"}}`,
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"q\":\"x\"}"}`,
		`data: {"type":"response.function_call_arguments.done"}`,
		`data: {"type":"response.output_item.done","item":{"type":"function_call"}}`,
		`data: {"type":"response.completed"}`,
		"",
	}, "\n")

	v, err := PreflightStream(io.NopCloser(strings.NewReader(source)), StreamProtocolResponses, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if v.Degraded || v.Reason != "content_then_tool" {
		t.Fatalf("verdict = %+v, want healthy content_then_tool", v)
	}
}

// Some 4.6 builds emit object-shaped text deltas. Those still count as content.
func TestPolicyObjectShapedOutputTextDeltaIsContent(t *testing.T) {
	source := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":{"text":"answer"}}`,
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
