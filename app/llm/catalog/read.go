package catalog

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// maxList bounds an answer that carries a model list.
const maxList = 1 << 20

// readGet asks a JSON address with a bearer token and returns the body. It is
// the one shape every fetcher below shares: a list is small, a redirect must
// not carry the token on, and a refusal is an error, not an empty list.
func readGet(ctx context.Context, client *http.Client, url, token string, extra map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, ErrNoCatalog
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxList))
		return nil, ErrNoCatalog
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxList))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// accountValue reads one text field of a connection, its API key first.
func accountValue(conn repository.Connection, name string) string {
	d := conn.Doc()
	psd, _ := d.GetRaw("providerSpecificData")
	if v := gjson.Get(psd, name).String(); v != "" {
		return v
	}
	if name == "apiKey" {
		k, _ := d.GetString("apiKey")
		return k
	}
	if name == "accessToken" {
		k, _ := d.GetString("accessToken")
		return k
	}
	return ""
}

// credential is what a request would present: the account's key, else its
// access token.
func credential(conn repository.Connection) string {
	if k := accountValue(conn, "apiKey"); k != "" {
		return k
	}
	return accountValue(conn, "accessToken")
}

// idsOf reads a list of {"id": …} entries, whatever the envelope around them.
func idsOf(raw []byte) []string {
	var ids []string
	for _, path := range []string{"models", "data", "data.models", "data.biz_data.models"} {
		list := gjson.GetBytes(raw, path)
		if !list.IsArray() {
			continue
		}
		for _, entry := range list.Array() {
			if id := document.FirstString(entry.Get("id"), entry.Get("model"), entry.Get("name")); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			return ids
		}
	}
	return ids
}

// stringsOf reads a list that is already plain strings.
func stringsOf(raw []byte) []string {
	var out []string
	for _, entry := range gjson.ParseBytes(raw).Array() {
		if entry.Type == gjson.String && entry.String() != "" {
			out = append(out, entry.String())
		}
	}
	return out
}

// joinURL joins an origin and a path.
func joinURL(origin, path string) string {
	return strings.TrimSuffix(origin, "/") + path
}
