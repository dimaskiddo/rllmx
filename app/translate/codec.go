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
)

// GeminiFamily reports a format whose bodies and streams are Gemini's, with or
// without a wrapper.
func GeminiFamily(format string) bool {
	return format == FormatGemini || format == FormatCloudCode || format == FormatVertex
}

// ErrBadBody means a body is not the shape its endpoint promises. The message
// never repeats body content.
var ErrBadBody = errors.New("translate: the body is not valid for this format")

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
	out, err := dst.WriteRequest(req, c)
	return out, req, err
}
