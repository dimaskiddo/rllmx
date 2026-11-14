package catalog

import (
	"context"
	"net/http"
	neturl "net/url"
	"strings"

	"rllmx/app/llm/executor/cursor"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

// Fetchers builds the per-provider list readers from the registry and the one
// HTTP client every outbound call shares. A provider the map omits is always
// undetermined: its list is not read here.
func Fetchers(reg *upstream.Registry, client *http.Client) map[string]Fetcher {
	out := map[string]Fetcher{}
	if reg == nil || client == nil {
		return out
	}
	if e, ok := reg.ByID("kiro"); ok {
		out["kiro"] = kiroFetcher(e, client)
	}
	if e, ok := reg.ByID("kiro-social"); ok {
		out["kiro-social"] = kiroFetcher(e, client)
	}
	if e, ok := reg.ByID("codex"); ok {
		out["codex"] = codexFetcher(e, client)
	}
	if e, ok := reg.ByID("cursor"); ok {
		out["cursor"] = cursorFetcher(e, client)
	}
	if e, ok := reg.ByID("kimchi"); ok {
		out["kimchi"] = kimchiFetcher(e, client)
	}
	return out
}

// kiroFetcher reads CodeWhisperer's own list. The address and the identity are
// the ones a request uses, so the account is asked the way it is charged.
func kiroFetcher(e upstream.Entry, client *http.Client) Fetcher {
	return func(ctx context.Context, conn repository.Connection) ([]string, error) {
		token := credential(conn)
		if token == "" {
			return nil, ErrNoCatalog
		}
		profile := accountValue(conn, "profileArn")
		url := joinURL(e.Transport.BaseURL, "/ListAvailableModels?origin=AI_EDITOR")
		if profile != "" {
			url += "&profileArn=" + neturl.QueryEscape(profile)
		}
		raw, err := readGet(ctx, client, url, token, map[string]string{"x-amzn-kiro-agent-mode": "vibe"})
		if err != nil {
			return nil, err
		}
		if ids := idsOf(raw); len(ids) > 0 {
			return ids, nil
		}
		return stringsOf(raw), nil
	}
}

// codexFetcher reads the Codex backend's model list.
func codexFetcher(e upstream.Entry, client *http.Client) Fetcher {
	return func(ctx context.Context, conn repository.Connection) ([]string, error) {
		token := credential(conn)
		if token == "" {
			return nil, ErrNoCatalog
		}
		url := joinURL(e.Transport.BaseURL, "/models")
		raw, err := readGet(ctx, client, url, token, map[string]string{"originator": "codex_cli_rs"})
		if err != nil {
			return nil, err
		}
		return idsOf(raw), nil
	}
}

// cursorFetcher asks the agent service which models the account may use. The
// answer is the same one the executor already reads to resolve its alias.
func cursorFetcher(e upstream.Entry, client *http.Client) Fetcher {
	return func(ctx context.Context, conn repository.Connection) ([]string, error) {
		token := credential(conn)
		if token == "" {
			return nil, ErrNoCatalog
		}
		ids, ok := cursor.UsableModels(ctx, client, e, token, accountValue(conn, "machineId"))
		if !ok {
			return nil, ErrNoCatalog
		}
		return ids, nil
	}
}

// kimchiFetcher reads the gateway's model metadata, whose ids are the list.
func kimchiFetcher(e upstream.Entry, client *http.Client) Fetcher {
	return func(ctx context.Context, conn repository.Connection) ([]string, error) {
		token := credential(conn)
		if token == "" {
			return nil, ErrNoCatalog
		}
		url := joinURL(corpus(e), "/v1/models/metadata?include_in_cli=true")
		raw, err := readGet(ctx, client, url, token, map[string]string{"User-Agent": "kimchi/0.1.50"})
		if err != nil {
			return nil, err
		}
		return idsOf(raw), nil
	}
}

// corpus is the provider's origin, from its address.
func corpus(e upstream.Entry) string {
	u := e.Transport.BaseURL
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if j := strings.IndexByte(u, '/'); j >= 0 {
		u = u[:j]
	}
	return "https://" + u
}
