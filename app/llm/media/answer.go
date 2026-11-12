package media

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// WithModel is the client's body with the provider's model id in it.
func WithModel(body []byte, wire string) ([]byte, error) {
	if wire == "" {
		return body, nil
	}
	if !gjson.ValidBytes(body) {
		return nil, BadRequestf("the request body is not JSON")
	}
	out, err := sjson.SetBytes(body, "model", wire)
	if err != nil {
		return nil, BadRequestf("the request body cannot carry a model")
	}
	return out, nil
}

// UsageOf reads an embeddings answer's usage. The count is taken only when it
// is exact: the reference records nothing otherwise, and neither do we.
func UsageOf(body []byte) Usage {
	prompt := gjson.GetBytes(body, "usage.prompt_tokens")
	total := gjson.GetBytes(body, "usage.total_tokens")
	completion := gjson.GetBytes(body, "usage.completion_tokens")
	if prompt.Type == gjson.Number && total.Type == gjson.Number && prompt.Int() == total.Int() &&
		(completion.Type != gjson.Number || completion.Int() == 0) {
		return Usage{Prompt: prompt.Int(), Known: true}
	}
	return Usage{}
}

// ImageUsageOf reads an image answer's usage when the provider reports both
// counts as integers.
func ImageUsageOf(body []byte) Usage {
	in, out := gjson.GetBytes(body, "usage.input_tokens"), gjson.GetBytes(body, "usage.output_tokens")
	if in.Type == gjson.Number && out.Type == gjson.Number {
		return Usage{Prompt: in.Int(), Completion: out.Int(), Known: true}
	}
	return Usage{}
}

// PassJSON is the answer of an adapter whose provider already speaks the
// client's shape: a refusal becomes a provider error, a body must be JSON.
func PassJSON(status int, header http.Header, body []byte, refusal string) (Response, error) {
	if status < 200 || status > 299 {
		return Response{}, &ProviderError{Status: status, Text: refusal}
	}
	if !gjson.ValidBytes(body) {
		return Response{}, &ProviderError{Status: http.StatusBadGateway, Text: refusal}
	}
	h := map[string]string{}
	if ct := header.Get("Content-Type"); ct != "" {
		h["Content-Type"] = ct
	} else {
		h["Content-Type"] = "application/json"
	}
	return Response{Status: status, Header: h, Body: body, Usage: UsageOf(body)}, nil
}

// WithQuery appends the query values the provider takes in the address.
func WithQuery(base string, q url.Values) string {
	if len(q) == 0 {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + q.Encode()
}
