package resinquality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ResinClient talks to Resinat/Resin control-plane HTTP API.
type ResinClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewResinClient(baseURL, token string, timeout time.Duration) *ResinClient {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &ResinClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

type leaseDTO struct {
	PlatformID string `json:"platform_id"`
	Account    string `json:"account"`
	NodeHash   string `json:"node_hash"`
	NodeTag    string `json:"node_tag"`
	EgressIP   string `json:"egress_ip"`
}

type listLeasesResponse struct {
	Items  []leaseDTO `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

func (c *ResinClient) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return data, resp.StatusCode, fmt.Errorf("resin HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	return data, resp.StatusCode, nil
}

func (c *ResinClient) ListLeases(ctx context.Context, platformID string, limit, offset int) (listLeasesResponse, error) {
	if limit <= 0 {
		limit = 200
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	q.Set("offset", fmt.Sprintf("%d", offset))
	data, _, err := c.do(ctx, http.MethodGet, "/api/v1/platforms/"+url.PathEscape(platformID)+"/leases?"+q.Encode(), nil)
	if err != nil {
		return listLeasesResponse{}, err
	}
	var out listLeasesResponse
	if len(data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return listLeasesResponse{}, err
	}
	return out, nil
}

func (c *ResinClient) DeleteAllLeases(ctx context.Context, platformID string) error {
	_, _, err := c.do(ctx, http.MethodDelete, "/api/v1/platforms/"+url.PathEscape(platformID)+"/leases", nil)
	return err
}

func (c *ResinClient) DeleteLeaseForAccountKey(ctx context.Context, platformID, accountKey string) error {
	if accountKey == "" {
		return nil
	}
	path := "/api/v1/platforms/" + url.PathEscape(platformID) + "/leases/" + url.PathEscape(accountKey)
	_, status, err := c.do(ctx, http.MethodDelete, path, nil)
	// 404 = already gone; desired for per-account clear.
	if err != nil && status == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *ResinClient) ListAllLeases(ctx context.Context, platformID string) ([]leaseDTO, error) {
	var all []leaseDTO
	offset := 0
	const page = 200
	for {
		resp, err := c.ListLeases(ctx, platformID, page, offset)
		if err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		offset += len(resp.Items)
		if len(resp.Items) == 0 || offset >= resp.Total || len(resp.Items) < page {
			break
		}
	}
	return all, nil
}

// ActionResult is returned after reshuffle / targeted clear.
type ActionResult struct {
	Action      string   `json:"action"`
	BeforeTotal int      `json:"beforeTotal"`
	AfterTotal  int      `json:"afterTotal"`
	Cleared     int      `json:"cleared"`
	Matched     int      `json:"matched,omitempty"`
	Errors      int      `json:"errors,omitempty"`
	OK          bool     `json:"ok"`
	Verified    bool     `json:"verified"`
	ExitIPs     []string `json:"exitIps,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Fallback    string   `json:"fallback,omitempty"`
}

func (c *ResinClient) ReshufflePlatform(ctx context.Context, platformID string) (ActionResult, error) {
	before, err := c.ListLeases(ctx, platformID, 1, 0)
	if err != nil {
		return ActionResult{}, err
	}
	if err := c.DeleteAllLeases(ctx, platformID); err != nil {
		return ActionResult{}, err
	}
	after, err := c.ListLeases(ctx, platformID, 1, 0)
	if err != nil {
		return ActionResult{}, err
	}
	cleared := before.Total - after.Total
	if cleared < 0 {
		cleared = 0
	}
	return ActionResult{
		Action:      "reshuffle",
		BeforeTotal: before.Total,
		AfterTotal:  after.Total,
		Cleared:     cleared,
		OK:          after.Total == 0 || cleared > 0 || before.Total == 0,
		Verified:    after.Total == 0,
	}, nil
}

func (c *ResinClient) ClearLeasesForExitIPs(ctx context.Context, platformID string, exitIPs map[string]struct{}) (ActionResult, error) {
	if len(exitIPs) == 0 {
		return ActionResult{Action: "targeted", OK: true}, nil
	}
	leases, err := c.ListAllLeases(ctx, platformID)
	if err != nil {
		return ActionResult{}, err
	}
	var matched []leaseDTO
	for _, lease := range leases {
		ip := strings.TrimSpace(lease.EgressIP)
		if ip == "" {
			continue
		}
		if _, ok := exitIPs[ip]; ok {
			matched = append(matched, lease)
		}
	}
	cleared, errors := 0, 0
	ips := make([]string, 0, len(exitIPs))
	for ip := range exitIPs {
		ips = append(ips, ip)
	}
	for _, lease := range matched {
		if strings.TrimSpace(lease.Account) == "" {
			continue
		}
		if err := c.DeleteLeaseForAccountKey(ctx, platformID, lease.Account); err != nil {
			errors++
			continue
		}
		cleared++
	}
	return ActionResult{
		Action:   "targeted",
		Matched:  len(matched),
		Cleared:  cleared,
		Errors:   errors,
		OK:       errors == 0,
		ExitIPs:  ips,
		Verified: cleared > 0 && errors == 0,
	}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
