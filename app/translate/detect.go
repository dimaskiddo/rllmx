package translate

import (
	"errors"
	"regexp"
	"strings"
)

// ErrUnknownEndpoint means a path names no client format.
var ErrUnknownEndpoint = errors.New("translate: the endpoint names no client format")

var geminiModel = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Detect names the client format of a request by its endpoint alone. A body of
// another shape is the codec's error to report, in the format the client chose;
// it is never read as a different format.
func Detect(path string) (string, error) {
	switch {
	case path == "/v1/chat/completions":
		return FormatOpenAI, nil
	case path == "/v1/messages", path == "/v1/messages/count_tokens":
		return FormatClaude, nil
	case path == "/v1/responses", path == "/v1/responses/compact":
		return FormatResponses, nil
	case path == "/v1/api/chat":
		return FormatOllama, nil
	case strings.HasPrefix(path, "/v1beta/models/"):
		return FormatGemini, nil
	}
	return "", ErrUnknownEndpoint
}

// GeminiAction splits `/v1beta/models/{model}:{action}` at the last colon. The
// model is one or two plain segments (`gemini-2.5-pro` or `provider/model`), so
// a crafted segment can never change the path the upstream sees.
func GeminiAction(path string) (model, action string, ok bool) {
	rest, found := strings.CutPrefix(path, "/v1beta/models/")
	if !found {
		return "", "", false
	}
	i := strings.LastIndexByte(rest, ':')
	if i < 0 || i == len(rest)-1 {
		return "", "", false
	}
	model, action = rest[:i], rest[i+1:]
	segs := strings.Split(model, "/")
	if len(segs) > 2 {
		return "", "", false
	}
	for _, s := range segs {
		if !geminiModel.MatchString(s) || s == "." || strings.Contains(s, "..") {
			return "", "", false
		}
	}
	return model, action, true
}
