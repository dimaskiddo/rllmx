package oauth

import (
	"context"
	"errors"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// ErrImport means the pasted credential could not be turned into a usable
// account: it was refused, or it did not carry what the provider needs.
var ErrImport = errors.New("oauth: the pasted credential could not be imported")

// importByImporter stores a pasted credential whose shape a provider's data
// names. It answers ErrUnsupported for a provider with no importer, so the
// plain import can still take it.
func (s *Service) importByImporter(ctx context.Context, f Flow, values map[string]string) (string, error) {
	switch f.Importer {
	case "":
		return "", ErrUnsupported
	case "codex-token":
		return s.importCodexToken(ctx, f, values)
	case "zed":
		return s.importZedToken(ctx, f, values)
	case "kiro-refresh":
		return s.importKiroRefresh(ctx, f, values)
	case "kiro-external-idp":
		return s.importKiroExternalIDP(ctx, f, values)
	case "kiro-apikey", "mimo-key":
		// A Kiro API key is an AWS-signed key, and a MiMo session has no OAuth
		// block to reach this route through; both wait on their own connection
		// shape.
		return "", ErrUnsupported
	}
	return "", ErrUnsupported
}

// importCodexToken stores a pasted ChatGPT access token. Its payload names the
// account and the plan, which a request to Codex sends.
func (s *Service) importCodexToken(ctx context.Context, f Flow, values map[string]string) (string, error) {
	token := strings.TrimSpace(values["accessToken"])
	if !CredentialText(token) || len(token) < minImportToken {
		return "", ErrToken
	}
	claims, ok := Claims(token)
	if !ok {
		return "", ErrToken
	}
	// The claim's name holds dots, which a path would split, so it is read as a
	// key of the object.
	auth := claims.Map()["https://api.openai.com/auth"]
	acc := Account{
		Email:     CleanEmail(claimText(claims, "email", "preferred_username")),
		NoRefresh: true,
	}
	if acc.Email == "" {
		acc.Email = CleanEmail(strings.TrimSpace(values["email"]))
	}
	acc.PSD = WithPSD(acc.PSD, "authMethod", "access_token")
	if id := auth.Get("chatgpt_account_id").String(); id != "" {
		acc.PSD = WithPSD(acc.PSD, "chatgptAccountId", id)
	}
	if plan := auth.Get("chatgpt_plan_type").String(); plan != "" {
		acc.PSD = WithPSD(acc.PSD, "chatgptPlanType", plan)
	}
	applyExpiry(&acc, claims)
	return s.Save(ctx, f, Tokens{Access: token}, acc)
}

// importZedToken stores a Zed account's user token. The account record answers
// the organisation a request names.
func (s *Service) importZedToken(ctx context.Context, f Flow, values map[string]string) (string, error) {
	token := strings.TrimSpace(values["accessToken"])
	userID := strings.TrimSpace(values["userId"])
	if !CredentialText(token) || userID == "" {
		return "", ErrToken
	}
	systemID := strings.TrimSpace(values["systemId"])
	if systemID == "" {
		return "", ErrImport
	}
	acc := Account{NoRefresh: true}
	acc.PSD = WithPSD(acc.PSD, "authMethod", "imported")
	acc.PSD = WithPSD(acc.PSD, "userId", userID)
	acc.PSD = WithPSD(acc.PSD, "systemId", systemID)
	return s.Save(ctx, f, Tokens{Access: token}, acc)
}

// kiroIDCURL is the AWS SSO identity-provider address one region signs at.
func kiroIDCURL(region string) string {
	return "https://oidc." + region + ".amazonaws.com/token"
}

// importKiroRefresh trades a pasted Kiro refresh token for an account. A client
// pair and region name the AWS identity provider; without them the desktop
// endpoint is used, as the client's own paste does.
func (s *Service) importKiroRefresh(ctx context.Context, f Flow, values map[string]string) (string, error) {
	refresh := strings.TrimSpace(values["refreshToken"])
	if !CredentialText(refresh) || len(refresh) < minImportToken {
		return "", ErrToken
	}
	clientID := strings.TrimSpace(values["clientId"])
	secret := strings.TrimSpace(values["clientSecret"])
	region := strings.TrimSpace(values["region"])
	// The region lands in the host the refresh token and client secret are sent
	// to, so a value that is not a region name is refused before any call.
	if region != "" && !upstream.ValidRegion(region) {
		return "", ErrImportField
	}
	if clientID != "" && secret != "" {
		if !CredentialText(clientID) || !CredentialText(secret) {
			return "", ErrImportField
		}
		if region == "" {
			region = "us-east-1"
		}
		f.ClientID, f.ClientSecret, f.TokenURL = clientID, secret, kiroIDCURL(region)
	}
	dialect, ok := s.dials().Device[f.Dialect]
	if !ok || dialect.Refresh == nil {
		return "", ErrUnsupported
	}
	t, err := dialect.Refresh(ctx, s.Tokens, f, refresh, CallHeaders(f, ""))
	if err != nil {
		return "", ErrToken
	}
	acc := Account{}
	method, provider := "imported", "Imported"
	if clientID != "" && secret != "" {
		method, provider = "idc", "Enterprise"
	}
	acc.PSD = WithPSD(acc.PSD, "authMethod", method)
	acc.PSD = WithPSD(acc.PSD, "provider", provider)
	if arn := strings.TrimSpace(values["profileArn"]); arn != "" {
		if len(arn) > MaxProfileField || !CredentialText(arn) {
			return "", ErrImportField
		}
		acc.PSD = WithPSD(acc.PSD, "profileArn", arn)
	}
	if region != "" {
		acc.PSD = WithPSD(acc.PSD, "region", region)
	}
	if clientID != "" {
		acc.PSD = WithPSD(acc.PSD, "clientId", clientID)
	}
	if secret != "" {
		acc.PSD = WithPSD(acc.PSD, "clientSecret", secret)
	}
	return s.Save(ctx, f, t, acc)
}

// importKiroExternalIDP stores the JSON an external identity provider's helper
// prints: its access and refresh tokens, client id, scopes and profile.
func (s *Service) importKiroExternalIDP(ctx context.Context, f Flow, values map[string]string) (string, error) {
	raw := strings.TrimSpace(values["json"])
	if raw == "" {
		raw = strings.TrimSpace(values["body"])
	}
	if raw == "" {
		return "", ErrImportField
	}
	doc := gjson.Parse(raw)
	if doc.Get("auth_method").String() != "external_idp" {
		return "", ErrImportField
	}
	access := doc.Get("access_token").String()
	refresh := doc.Get("refresh_token").String()
	clientID := doc.Get("client_id").String()
	if !CredentialText(access) || clientID == "" {
		return "", ErrImportField
	}
	acc := Account{}
	acc.PSD = WithPSD(acc.PSD, "authMethod", "external_idp")
	acc.PSD = WithPSD(acc.PSD, "provider", "External IdP")
	acc.PSD = WithPSD(acc.PSD, "clientId", clientID)
	if arn := doc.Get("profile_arn").String(); arn != "" {
		acc.PSD = WithPSD(acc.PSD, "profileArn", arn)
	}
	if scopes := doc.Get("scopes").String(); scopes != "" {
		acc.PSD = WithPSD(acc.PSD, "scopes", scopes)
	}
	return s.Save(ctx, f, Tokens{Access: access, Refresh: refresh, ExpiresIn: 3600}, acc)
}
