package cursor

import (
	"context"
	"io"
	"net/http"
	"strings"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
)

// UsableModels reads the ids the agent service says this account may use. It is
// the same answer the executor reads to resolve the picker's alias; the
// pipeline's catalogue guard asks it too, so one reading serves both.
func UsableModels(ctx context.Context, client *http.Client, e upstream.Entry, token, machine string) ([]string, bool) {
	if token == "" || machine == "" || client == nil {
		return nil, false
	}
	target := modelsURL(e)
	if target == "" {
		return nil, false
	}
	h := headers(token, machine, true)
	h["Content-Type"] = "application/proto"
	h["Accept"] = "application/proto"
	delete(h, "connect-protocol-version")
	delete(h, "connect-accept-encoding")
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target, http.NoBody)
	if err != nil {
		return nil, false
	}
	for k, v := range h {
		hr.Header.Set(k, v)
	}
	strict := *client
	strict.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := strict.Do(hr)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		executor.Drain(resp.Body)
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogue))
	if err != nil {
		return nil, false
	}
	if models := usableModels(raw); len(models) > 0 {
		return models, true
	}
	return nil, false
}

// modelsURL is the model-list address of a provider row, from its run address.
func modelsURL(e upstream.Entry) string {
	base := e.Transport.BaseURL
	if base == "" {
		base = "https://agent.api5.cursor.sh/agent.v1.AgentService/Run"
	}
	return strings.TrimSuffix(base, "/Run") + "/GetUsableModels"
}
