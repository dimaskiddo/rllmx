package translate

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// Format names.
const (
	FormatOpenAI    = "openai"
	FormatClaude    = "claude"
	FormatResponses = "openai-responses"
	FormatGemini    = "gemini"
	FormatOllama    = "ollama"
	// FormatCloudCode is a Gemini body inside the Cloud Code envelope, for Gemini
	// CLI and Antigravity; FormatVertex is a Gemini body for Vertex AI.
	FormatCloudCode = "gemini-cli"
	FormatVertex    = "vertex"
	// FormatKiro is CodeWhisperer's conversation body; FormatCommandCode is
	// CommandCode's agent wrapper around an Anthropic-shaped conversation;
	// FormatCursor is the flattened OpenAI body its agent service is given.
	FormatKiro        = "kiro"
	FormatCommandCode = "commandcode"
	FormatCursor      = "cursor"
)

// GeminiFamily reports a format whose bodies and streams are Gemini's, with or
// without a wrapper.
func GeminiFamily(format string) bool {
	return format == FormatGemini || format == FormatCloudCode || format == FormatVertex
}

// ErrBadBody means a body is not the shape its endpoint promises. The message
// never repeats body content.
var ErrBadBody = errors.New("translate: the body is not valid for this format")

// ErrImages means a request carries an image the destination cannot take. A
// writer returns it so the call is refused in the client's own words instead of
// the picture disappearing silently.
var ErrImages = errors.New("translate: the destination takes no images")

// Codec joins one wire format to the hub.
type Codec interface {
	Name() string
	ReadRequest(body []byte, c *Ctx) (*Request, error)
	WriteRequest(r *Request, c *Ctx) ([]byte, error)
	ReadResponse(body []byte, c *Ctx) (*Response, error)
	WriteResponse(r *Response, c *Ctx) ([]byte, error)
	NewStreamReader(c *Ctx) StreamReader
	NewStreamWriter(c *Ctx) StreamWriter
}

var (
	mu     sync.RWMutex
	codecs = map[string]Codec{}
)

// Register adds a codec under its name. A second codec of one name is a bug in
// the program, so it panics.
func Register(c Codec) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := codecs[c.Name()]; dup {
		panic(fmt.Sprintf("translate: codec %q registered twice", c.Name()))
	}
	codecs[c.Name()] = c
}

// Lookup returns the codec for a format.
func Lookup(format string) (Codec, bool) {
	mu.RLock()
	defer mu.RUnlock()
	c, ok := codecs[format]
	return c, ok
}

// WriteFormats lists the formats a client can be answered in, in name order. A
// format only an upstream speaks is left out: nothing asks this router to
// answer in it.
func WriteFormats() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(codecs))
	for n, c := range codecs {
		if ro, ok := c.(readOnly); ok && ro.ReadOnly() {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// readOnly is a codec whose format an upstream speaks and no client does.
type readOnly interface{ ReadOnly() bool }

// Formats lists the registered formats in name order.
func Formats() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(codecs))
	for n := range codecs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Convert reads a body in from and writes it in to, through the hub. A same
// format conversion still goes through the hub so the concerns apply.
func Convert(from, to string, body []byte, c *Ctx) ([]byte, *Request, error) {
	src, ok := Lookup(from)
	if !ok {
		return nil, nil, fmt.Errorf("translate: no codec for %q", from)
	}
	dst, ok := Lookup(to)
	if !ok {
		return nil, nil, fmt.Errorf("translate: no codec for %q", to)
	}
	req, err := src.ReadRequest(body, c)
	if err != nil {
		return nil, nil, err
	}
	if from != to {
		req.Extra = nil
	}
	out, err := dst.WriteRequest(req, c)
	return out, req, err
}
