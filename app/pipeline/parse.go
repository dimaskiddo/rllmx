package pipeline

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/translate"
)

// parse reads the body through the client's codec. Routes that are not chat
// calls (token counting, other Gemini actions) are not served here.
func (c *call) parse() (Output, bool) {
	format, err := translate.Detect(c.in.Path)
	if err != nil || strings.HasSuffix(c.in.Path, "/count_tokens") {
		return c.fail(http.StatusNotFound, "not_found", "Not found"), false
	}
	codec, ok := translate.Lookup(format)
	if !ok {
		return c.fail(http.StatusNotFound, "not_found", "Not found"), false
	}
	c.format = format
	c.row.SourceFormat = format
	c.tctx = translate.NewCtx(format, format)

	req, err := codec.ReadRequest(c.in.Body, c.tctx)
	if err != nil {
		if errors.Is(err, translate.ErrBadBody) {
			return c.fail(http.StatusBadRequest, "invalid_request", "Invalid request body"), false
		}
		return c.fail(http.StatusBadRequest, "invalid_request", "Invalid request"), false
	}
	if format == translate.FormatGemini {
		model, action, ok := translate.GeminiAction(c.in.Path)
		if !ok || (action != "generateContent" && action != "streamGenerateContent") {
			return c.fail(http.StatusNotFound, "not_found", "Not found"), false
		}
		req.Model, req.Stream = model, action == "streamGenerateContent"
	}
	if strings.TrimSpace(req.Model) == "" {
		return c.fail(http.StatusBadRequest, "invalid_request", "Missing model"), false
	}
	if c.in.AcceptJSON && format != translate.FormatGemini && !gjson.GetBytes(c.in.Body, "stream").Exists() {
		req.Stream = false
	}
	c.req = req
	c.requested = req.Model
	c.clientStream = req.Stream
	c.row.Stream = req.Stream
	return Output{}, true
}
