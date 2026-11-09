package executor

import (
	"net/url"
	"strings"

	"rllmx/app/upstream"
)

// Endpoint suffixes of the user-defined node types.
const (
	chatSuffix      = "/chat/completions"
	responsesSuffix = "/responses"
	messagesSuffix  = "/messages"
)

// NodeURL is the address of a user node's endpoint: its stored base plus the
// path of the API the node speaks. apiType is "chat" or "responses" for an
// OpenAI-compatible node and ignored for the others.
func NodeURL(nodeType, base, apiType string) string {
	base = strings.TrimRight(base, "/")
	switch nodeType {
	case upstream.NodeTypeAnthropic:
		return base + messagesSuffix
	case upstream.NodeTypeEmbedding:
		return base + "/embeddings"
	}
	if apiType == "responses" {
		return base + responsesSuffix
	}
	return base + chatSuffix
}

// RegistryURL is the address of a registry provider's endpoint. The data file
// holds the full address of its chat endpoint; a Gemini model is addressed by
// action and stream, never by the body.
func RegistryURL(e upstream.Entry, format, model string, stream bool) string {
	if format != upstream.FormatGemini {
		return withSuffix(formatAddress(e.Transport, format), e.Transport.URLSuffix)
	}
	base := strings.TrimRight(e.Transport.BaseURL, "/")
	if stream {
		return base + "/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	}
	return base + "/" + url.PathEscape(model) + ":generateContent"
}

// formatAddress is the endpoint a wire format is sent to: the provider's own
// address for it when its data names one, else the base address.
func formatAddress(t upstream.Transport, format string) string {
	switch {
	case format == upstream.FormatResponses && t.ResponsesURL != "":
		return t.ResponsesURL
	case format == upstream.FormatClaude && t.MessagesURL != "":
		return t.MessagesURL
	}
	return t.BaseURL
}

// withSuffix adds a provider's query string to an address that has none.
func withSuffix(addr, suffix string) string {
	if suffix == "" || strings.Contains(addr, "?") {
		return addr
	}
	return addr + suffix
}
