package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// ClaudeCLIFloor is the oldest Claude Code version this build advertises. A
// model launch can require a newer CLI, so the live value may climb above it and
// never falls below.
const ClaudeCLIFloor = "2.1.322"

// ClaudeCLINPMURL publishes the latest @anthropic-ai/claude-code release.
const ClaudeCLINPMURL = "https://registry.npmjs.org/@anthropic-ai/claude-code/latest"

const (
	claudeCLIProbe    = 3 * time.Second
	claudeCLIMaxBytes = 256 << 10
)

var (
	claudeCLIVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	claudeCLIHeaderRe  = regexp.MustCompile(`claude-cli/\d+\.\d+\.\d+`)
)

// ClaudeCLI is the claude-cli/x.y.z fingerprint the Claude OAuth call carries.
// The advertised version is the pin when one is set, else the higher of the
// floor and what npm last reported.
type ClaudeCLI struct {
	pin  string
	url  string
	live atomic.Pointer[string]
}

// NewClaudeCLI makes the version store. An empty pin reads npm; a pin must
// already be validated and is never lowered by the floor check.
func NewClaudeCLI(pin string) *ClaudeCLI { return &ClaudeCLI{pin: pin, url: ClaudeCLINPMURL} }

// Version is the current fingerprint value.
func (c *ClaudeCLI) Version() string {
	if c == nil {
		return ClaudeCLIFloor
	}
	if c.pin != "" {
		return c.pin
	}
	best := ClaudeCLIFloor
	if v := c.live.Load(); v != nil && *v != "" && compareVersions(*v, best) > 0 {
		best = *v
	}
	return best
}

// Pinned reports whether the lookup is switched off.
func (c *ClaudeCLI) Pinned() bool { return c == nil || c.pin != "" }

// Refresh reads the latest published version once. It reports whether the read
// produced a usable answer, which sets how long the caller waits before the
// next try. A pinned store never calls out; the runner is its only caller, and
// a second one would only race to store the same kind of answer.
func (c *ClaudeCLI) Refresh(ctx context.Context, client *http.Client) bool {
	if c == nil || c.Pinned() || client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, claudeCLIProbe)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, claudeCLIMaxBytes))
		return false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, claudeCLIMaxBytes+1))
	if err != nil || len(raw) > claudeCLIMaxBytes {
		return false
	}
	var body struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(raw, &body) != nil || !claudeCLIVersionRe.MatchString(body.Version) {
		return false
	}
	v := body.Version
	c.live.Store(&v)
	return true
}

// Rewrite replaces the version in every claude-cli/x.y.z header value.
func (c *ClaudeCLI) Rewrite(headers map[string]string) {
	if c == nil || len(headers) == 0 {
		return
	}
	v := c.Version()
	for k, val := range headers {
		if strings.Contains(val, "claude-cli/") {
			headers[k] = claudeCLIHeaderRe.ReplaceAllString(val, "claude-cli/"+v)
		}
	}
}

// compareVersions orders two x.y.z strings; a part that is not a number reads as
// zero, which can only happen on a value this package did not read itself.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		if d := partValue(pa, i) - partValue(pb, i); d != 0 {
			return d
		}
	}
	return 0
}

func partValue(parts []string, i int) int {
	if i >= len(parts) {
		return 0
	}
	n := 0
	for _, r := range parts[i] {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0
		}
	}
	return n
}
