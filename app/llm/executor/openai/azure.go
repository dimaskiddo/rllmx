package openai

import (
	"net/url"
	"strings"

	"context"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
)

// defaultAzureVersion is the api-version used when the account names none.
const defaultAzureVersion = "2024-10-01-preview"

// Azure serves Azure OpenAI: the address is the account's own endpoint and
// deployment, and the key travels in api-key. Nothing comes from the
// environment; an account that lacks its endpoint is refused.
type Azure struct {
	Default *executor.Default
}

// Execute builds the deployment address from the account's data and sends.
func (a *Azure) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	target, err := azureURL(req)
	if err != nil {
		return executor.Response{}, err
	}
	req.URL = target
	headers := map[string]string{}
	if org := executor.Field(req, "organization"); org != "" {
		if !executor.AccountToken.MatchString(org) {
			return executor.Response{}, executor.ErrBadAccount
		}
		headers["OpenAI-Organization"] = org
	}
	req.Auth = &upstream.Auth{Scheme: upstream.SchemeRaw, Header: "api-key", Headers: headers}
	return a.Default.Execute(ctx, req)
}

func azureURL(req executor.Request) (string, error) {
	endpoint, err := url.Parse(executor.Field(req, "azureEndpoint"))
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.Trim(endpoint.Path, "/") != "" {
		return "", executor.ErrBadAccount
	}
	deployment := executor.Field(req, "deployment")
	if deployment == "" {
		deployment = req.Model
	}
	version := executor.Field(req, "apiVersion")
	if version == "" {
		version = defaultAzureVersion
	}
	if !executor.AccountToken.MatchString(deployment) || !executor.AccountToken.MatchString(version) {
		return "", executor.ErrBadAccount
	}
	return endpoint.Scheme + "://" + endpoint.Host + "/openai/deployments/" + deployment + "/chat/completions?api-version=" + version, nil
}
