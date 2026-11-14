package media

import (
	"net/url"
	"strings"

	"rllmx/app/llm/upstream"
)

// Auth puts the account's credential where the provider's config says it goes:
// a bearer header, a named header, or a query parameter. It returns the header
// name it used, "" when the credential is carried in the query.
func Auth(cfg *upstream.MediaConfig, credential string, header map[string]string, query url.Values) string {
	if cfg == nil || credential == "" || cfg.AuthType == "none" {
		return ""
	}
	role := cfg.AuthHeader
	if role == "" {
		role = "bearer"
	}
	switch role {
	case "bearer":
		header["Authorization"] = "Bearer " + credential
		return "Authorization"
	case "token":
		header["Authorization"] = "Token " + credential
		return "Authorization"
	case "key-auth":
		header["Authorization"] = "Key " + credential
		return "Authorization"
	case "basic":
		header["Authorization"] = "Basic " + credential
		return "Authorization"
	case "key", "api_key":
		query.Set(role, credential)
		return ""
	default:
		header[canonicalHeader(role)] = credential
		return canonicalHeader(role)
	}
}

// canonicalHeader spells a lower-case header name the way net/http does.
func canonicalHeader(name string) string {
	parts := strings.Split(strings.ToLower(name), "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "-")
}

// Headers is the provider's static headers plus the credential. query collects
// what the provider takes in the address.
func Headers(r Request, query url.Values) map[string]string {
	h := map[string]string{}
	for k, v := range staticHeaders(r.Config) {
		h[k] = v
	}
	Auth(r.Config, r.Credential, h, query)
	return h
}

func staticHeaders(cfg *upstream.MediaConfig) map[string]string {
	if cfg == nil {
		return nil
	}
	return cfg.Headers
}

// BaseURL is the service address with no trailing slash: the config's own, else
// the account's.
func BaseURL(r Request) string {
	if r.Config != nil && r.Config.BaseURL != "" {
		return strings.TrimRight(r.Config.BaseURL, "/")
	}
	return strings.TrimRight(r.Account["baseUrl"], "/")
}
