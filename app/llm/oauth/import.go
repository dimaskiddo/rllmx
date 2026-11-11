package oauth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// ErrImportField means an imported credential is missing what a request to that
// provider needs beside the token.
var ErrImportField = errors.New("oauth: the pasted credential is incomplete")

// ImportToken stores a token a user pasted from the provider's own client, with
// the extra values that provider's data names. Nothing is called: the token is
// checked for shape, and a JWT's payload names the account and its expiry. Such
// an account has no refresh token; when the token ends, the user pastes another.
func (s *Service) ImportToken(ctx context.Context, provider string, values map[string]string) (string, error) {
	f, err := s.flow(provider)
	if err != nil {
		return "", err
	}
	if f.Flow != upstream.FlowImport {
		return "", ErrUnsupported
	}
	token := strings.TrimSpace(values["accessToken"])
	if !credentialText(token) || len(token) < minImportToken {
		return "", ErrToken
	}
	acc := Account{NoRefresh: true, PSD: importPSD(f, values)}
	if acc.PSD == "" && len(f.Fields) > 0 {
		return "", ErrImportField
	}
	if cl, ok := claims(token); ok {
		acc.Email = CleanEmail(claimText(cl, "email", "sub"))
		// A token that says when it ends is stored with that moment: a guess
		// either drops a working account or sends a dead one.
		if exp := cl.Get("exp"); exp.Type == gjson.Number && exp.Int() > 0 {
			acc.NoRefresh = false
			acc.ExpiresAt = time.Unix(exp.Int(), 0).UTC()
		}
	}
	caseEmail := strings.TrimSpace(values["email"])
	if acc.Email == "" && caseEmail != "" {
		acc.Email = CleanEmail(caseEmail)
	}
	return s.save(ctx, f, Tokens{Access: token}, acc)
}

// importPSD reads the extra values a provider's data names, as its provider
// data object. It reports "" when one is missing.
func importPSD(f Flow, values map[string]string) string {
	psd := `{"authMethod":"imported"`
	for _, name := range f.Fields {
		v := strings.TrimSpace(values[name])
		if v == "" || len(v) > maxProfileField {
			return ""
		}
		psd = withPSD(psd, name, v)
	}
	return psd + "}"
}

// claimText reads the first short text claim that holds one.
func claimText(cl gjson.Result, names ...string) string {
	for _, n := range names {
		if v := cl.Get(n); v.Type == gjson.String && v.String() != "" {
			return v.String()
		}
	}
	return ""
}

// minImportToken is the shortest a pasted provider token can be.
const minImportToken = 50
