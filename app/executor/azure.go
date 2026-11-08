package executor

import (
	"net/url"
	"strings"

	"context"

	"rllmx/app/upstream"
)

// defaultAzureVersion is the api-version used when the account names none.
const defaultAzureVersion = "2024-10-01-preview"

// Azure serves Azure OpenAI: the address is the account's own endpoint and
// deployment, and the key travels in api-key. Nothing comes from the
// environment; an account that lacks its endpoint is refused.
type Azure struct {
	Default *Default
}

// Execute builds the deployment address from the account's data and sends.
func (a *Azure) Execute(ctx context.Context, req Request) (Response, error) {
	if req.Key == "" {
		return Response{}, ErrNoCredential
	}
	target, err := azureURL(req)
	if err != nil {
		return Response{}, err
	}
	req.URL = target
	headers := map[string]string{}
	if org := field(req, "organization"); org != "" {
		if !accountToken.MatchString(org) {
			return Response{}, ErrBadAccount
		}
		headers["OpenAI-Organization"] = org
	}
	req.Auth = &upstream.Auth{Scheme: upstream.SchemeRaw, Header: "api-key", Headers: headers}
	return a.Default.Execute(ctx, req)
}

func field(req Request, name string) string {
	if req.Account == nil {
		return ""
	}
	return strings.TrimSpace(req.Account(name))
}

func azureURL(req Request) (string, error) {
	endpoint, err := url.Parse(field(req, "azureEndpoint"))
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.Trim(endpoint.Path, "/") != "" {
		return "", ErrBadAccount
	}
	deployment := field(req, "deployment")
	if deployment == "" {
		deployment = req.Model
	}
	version := field(req, "apiVersion")
	if version == "" {
		version = defaultAzureVersion
	}
	if !accountToken.MatchString(deployment) || !accountToken.MatchString(version) {
		return "", ErrBadAccount
	}
	return endpoint.Scheme + "://" + endpoint.Host + "/openai/deployments/" + deployment + "/chat/completions?api-version=" + version, nil
}
