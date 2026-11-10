package stream

import (
	"mime"
	"regexp"
	"strings"

	"rllmx/app/text"
)

// Kind is what an upstream response body is.
type Kind int

const (
	// KindOther is neither a stream nor JSON, such as a gateway's HTML page.
	KindOther Kind = iota
	KindSSE
	KindNDJSON
	KindJSON
	// KindEvents is AWS EventStream, the binary framing Bedrock and
	// CodeWhisperer answer a streamed request with. NewFrames reads it.
	KindEvents
)

// KindOf classifies a Content-Type value. An empty or unknown type is
// KindOther, so the caller never treats a page as a stream.
func KindOf(contentType string) Kind {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return KindOther
	}
	switch strings.ToLower(mt) {
	case "text/event-stream":
		return KindSSE
	case "application/x-ndjson", "application/ndjson", "application/jsonl", "application/x-jsonlines":
		return KindNDJSON
	case "application/vnd.amazon.eventstream":
		return KindEvents
	case "application/json":
		return KindJSON
	}
	if strings.HasSuffix(strings.ToLower(mt), "+json") {
		return KindJSON
	}
	return KindOther
}

var titleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// PageTitle reads a short, markup-free line from an HTML page an upstream
// returned in place of an answer: the title, else the stripped body when it is
// short. It never returns more than 160 bytes, and what it returns has no tags.
func PageTitle(body []byte) string {
	s := string(body)
	if m := titleTag.FindStringSubmatch(s); m != nil {
		if t := clean(m[1]); t != "" {
			return t
		}
	}
	if len(s) < 200 {
		return clean(s)
	}
	return ""
}

func clean(s string) string { return text.StripMarkup(s, 160) }
