package pipeline

import (
	"net/http"
	"regexp"
	"strings"
)

// Client tools, named by what the reference calls them.
const (
	ClientClaude   = "claude"
	ClientCodex    = "codex"
	ClientGemini   = "gemini-cli"
	ClientCopilot  = "github-copilot"
	ClientDeepSeek = "deepseek-tui"
)

// readHeaders are the only request headers the pipeline looks at. The controller
// copies these and nothing else into Input.Header, so no client header can
// reach an upstream, whatever a later change does with the input.
var readHeaders = []string{
	"User-Agent", "X-App", "Originator", "Anthropic-Beta", "Openai-Intent", "X-Initiator",
	"X-Claude-Code-Session-Id", "X-Session-Id", "Session-Id", "Session_id", "X-Amp-Thread-Id", "X-Client-Request-Id",
}

// PickHeaders copies the headers the pipeline reads.
func PickHeaders(h http.Header) http.Header {
	out := http.Header{}
	for _, name := range readHeaders {
		for _, v := range h.Values(name) {
			out.Add(name, v)
		}
	}
	return out
}

// clientTool says which command line tool sent a request, from its headers
// alone. It decides how the upstream body is shaped and is never used to grant
// access. The order is the reference's.
func clientTool(h http.Header, userAgent string) string {
	ua := strings.ToLower(userAgent)
	if ua == "" {
		ua = strings.ToLower(h.Get("User-Agent"))
	}
	switch {
	case strings.Contains(ua, "githubcopilotchat"),
		strings.EqualFold(strings.TrimSpace(h.Get("Openai-Intent")), "conversation-panel"),
		strings.EqualFold(strings.TrimSpace(h.Get("X-Initiator")), "user"):
		return ClientCopilot
	case strings.Contains(ua, "claude-cli"), strings.Contains(ua, "claude-code"),
		strings.EqualFold(strings.TrimSpace(h.Get("X-App")), "cli"):
		return ClientClaude
	case strings.Contains(ua, "gemini-cli"):
		return ClientGemini
	case strings.Contains(ua, "codex-tui"), strings.Contains(ua, "codex-cli"), strings.Contains(ua, "codex_cli_rs"),
		strings.Contains(ua, "codex desktop"),
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(h.Get("Originator"))), "codex_"):
		return ClientCodex
	case strings.Contains(ua, "deepseek-tui"):
		return ClientDeepSeek
	}
	return ""
}

const (
	maxClientBeta = 32
	maxBetaHeader = 2048
)

var betaFlagPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// clientFlags reads the beta flags a client sent. A value over 2 KiB, a flag
// that is not a plain token and more than 32 flags are dropped, so a client can
// neither inject a header line nor grow the list without bound.
func clientFlags(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Anthropic-Beta") {
		if len(v) > maxBetaHeader {
			continue
		}
		for _, f := range strings.Split(v, ",") {
			f = strings.TrimSpace(f)
			if betaFlagPattern.MatchString(f) {
				out = append(out, f)
			}
			if len(out) >= maxClientBeta {
				return out
			}
		}
	}
	return out
}
