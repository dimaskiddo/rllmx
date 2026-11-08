package pipeline

import (
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/repository"
	"rllmx/app/upstream"
)

// betaList is the anthropic-beta value for one request: the provider's base
// flags, the heavy ones for the models that use them, minus the summary flag
// when the client asked for thinking summaries, then the client's own flags.
// Order is kept and nothing repeats.
func betaList(b *upstream.Beta, model string, body []byte, client []string) string {
	if b == nil {
		return strings.Join(dedupe(client), ",")
	}
	flags := make([]string, 0, len(b.Base)+len(b.Heavy)+len(client))
	summaries := gjson.GetBytes(body, "thinking.display").String() == "summarized"
	for _, f := range b.Base {
		if f == b.Summary && summaries {
			continue
		}
		flags = append(flags, f)
	}
	for _, p := range b.HeavyPrefixes {
		if strings.HasPrefix(model, p) {
			flags = append(flags, b.Heavy...)
			break
		}
	}
	return strings.Join(dedupe(append(flags, client...)), ",")
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	for _, f := range in {
		if f != "" && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// accountHeader reads the value of a login's account header from the first of
// the stored fields that holds one, in the connection's data or its provider
// specific data.
func accountHeader(conn repository.Connection, a *upstream.AccountHeader) string {
	if a == nil {
		return ""
	}
	d := conn.Doc()
	psd, _ := d.GetRaw("providerSpecificData")
	for _, field := range a.From {
		if v := gjson.Get(psd, field).String(); v != "" {
			return v
		}
		if v, _ := d.GetString(field); v != "" {
			return v
		}
	}
	return ""
}
