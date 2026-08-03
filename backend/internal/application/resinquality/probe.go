package resinquality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var ipRe = regexp.MustCompile(`(?m)^ip=([0-9a-fA-F:.]+)\s*$`)

// SampleExitIP uses curl -x when available (socks5h friendly on VPS).
func SampleExitIP(ctx context.Context, proxyURL string, timeout time.Duration) (exitIP string, err error) {
	if strings.TrimSpace(proxyURL) == "" {
		return "", fmt.Errorf("RESIN_PROXY_URL empty")
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	curl, lookErr := exec.LookPath("curl")
	if lookErr != nil {
		return "", fmt.Errorf("curl not found for exit sampling")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, curl, "-sS", "-x", proxyURL, "--max-time", fmt.Sprintf("%d", int(timeout.Seconds())), "https://1.1.1.1/cdn-cgi/trace")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("curl exit sample: %w", err)
	}
	m := ipRe.FindSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("no ip= in trace")
	}
	return string(m[1]), nil
}

// RunModelProbe calls local/public OpenAI-compatible chat completions (non-stream).
// When grok2api accounts route via resin, this exercises the pool path.
func RunModelProbe(ctx context.Context, baseURL, apiKey, model string, maxTokens int, timeout time.Duration) (ProbeResult, error) {
	if maxTokens <= 0 {
		maxTokens = 256
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	prompt := "Reply with exactly: QUALITY_OK"
	body := map[string]any{
		"model":      model,
		"stream":     false,
		"max_tokens": maxTokens,
		"messages":   []map[string]string{{"role": "user", "content": prompt}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error()}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error()}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: timeout}
	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error()}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	durationMS := time.Since(started).Milliseconds()
	if err != nil {
		return ProbeResult{OK: false, Error: err.Error(), DurationMS: durationMS}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("probe HTTP %d", resp.StatusCode)
		return ProbeResult{OK: false, Error: err.Error(), DurationMS: durationMS}, err
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ProbeResult{OK: false, Error: err.Error(), DurationMS: durationMS}, err
	}
	content := ""
	if len(parsed.Choices) > 0 {
		content = parsed.Choices[0].Message.Content
	}
	tokens := parsed.Usage.CompletionTokens
	if tokens <= 0 {
		words := strings.Fields(content)
		if len(words) == 0 {
			tokens = 1
		} else {
			tokens = int64(len(words))
		}
	}
	// non-stream TTFT approximation
	first := durationMS / 10
	if first < 1 {
		first = 1
	}
	if first > durationMS {
		first = durationMS
	}
	gen := durationMS - first
	if gen < 1 {
		gen = 1
	}
	tps := float64(tokens) / (float64(gen) / 1000.0)
	matched := strings.Contains(strings.ReplaceAll(content, " ", ""), "QUALITY_OK")
	preview := content
	if len(preview) > 80 {
		preview = preview[:80]
	}
	return ProbeResult{
		OK:                    true,
		OutputTokens:          tokens,
		DurationMS:            durationMS,
		FirstTokenMS:          first,
		OutputTokensPerSecond: tps,
		ExpectedMatched:       matched,
		ContentPreview:        preview,
	}, nil
}
