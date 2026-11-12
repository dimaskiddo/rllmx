package imaging

import (
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
)

// inlineList is the answer for a provider that returns its images inline.
func imageList(images []string) media.Response {
	data := make([]map[string]string, 0, len(images))
	for _, b := range images {
		data = append(data, map[string]string{"b64_json": b})
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: jsonBody(map[string]any{"created": media.NowSec(), "data": data})}
}

// urlList is the answer for a provider that returns image addresses.
func urlList(urls []string) media.Response {
	data := make([]map[string]string, 0, len(urls))
	for _, u := range urls {
		data = append(data, map[string]string{"url": u})
	}
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: jsonBody(map[string]any{"created": media.NowSec(), "data": data})}
}

func jsonBody(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"created":0,"data":[]}`)
	}
	return raw
}

func firstText(body gjson.Result, names ...string) string {
	for _, n := range names {
		if v := body.Get(n); v.Type == gjson.String {
			return v.String()
		}
	}
	return ""
}

// intField reads an integer field of the client's body.
func intField(body gjson.Result, name string) (int, bool) {
	v := body.Get(name)
	if v.Type != gjson.Number {
		return 0, false
	}
	return int(v.Int()), true
}
