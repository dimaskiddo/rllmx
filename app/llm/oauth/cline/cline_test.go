package cline

import (
	"encoding/base64"
	"rllmx/app/llm/oauth"
	"strings"
	"testing"
	"time"
)

func clineCode(doc string) string { return base64.RawStdEncoding.EncodeToString([]byte(doc)) }

func TestClineCodeCarriesTheTokens(t *testing.T) {
	doc := `{"accessToken":"AT","refreshToken":"RT","email":"c@x.example","expiresAt":"2023-11-14T22:13:20Z","firstName":"C"}`
	now := time.Unix(1_700_000_000, 0)
	std := base64.StdEncoding.EncodeToString([]byte(doc))
	cases := map[string]string{
		"unpadded standard": clineCode(doc),
		"padded standard":   std,
		"url-safe":          strings.NewReplacer("+", "-", "/", "_").Replace(clineCode(doc)),
		"trailing junk":     clineCode(doc + "\x00\x00 junk"),
	}
	for name, code := range cases {
		tok, ok := clineTokens(code, now)
		if !ok || tok.Access != "AT" || tok.Refresh != "RT" || tok.Email != "c@x.example" || tok.ExpiresIn != 1 {
			t.Errorf("%s: %+v %v", name, tok, ok)
		}
	}
	tok, _ := clineTokens(clineCode(strings.Replace(doc, "2023-11-14T22:13:20Z", "2023-11-14T22:13:30Z", 1)), now)
	if tok.ExpiresIn != 10 {
		t.Fatalf("expiresIn %d, want 10", tok.ExpiresIn)
	}
}

func TestAClineCodeThatHoldsNoTokensIsNotOne(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for name, code := range map[string]string{
		"empty":           "",
		"not base64":      "!!!",
		"not json":        clineCode("plain text"),
		"no access token": clineCode(`{"refreshToken":"x"}`),
		"an array":        clineCode(`[1]`),
		"a control byte":  clineCode(`{"accessToken":"a\u0000b"}`),
		"too long":        clineCode(`{"accessToken":"` + strings.Repeat("a", maxClineCode) + `"}`),
	} {
		if _, ok := clineTokens(code, now); ok {
			t.Errorf("%s was read as tokens", name)
		}
	}
}

func FuzzClineTokens(f *testing.F) {
	for _, s := range []string{"", "e30", clineCode(`{"accessToken":"a"}`), clineCode(`{"accessToken":"a","expiresAt":"x"}`), "////", "a=b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		tok, ok := clineTokens(code, time.Unix(1_700_000_000, 0))
		if ok && (tok.Access == "" || !oauth.CredentialText(tok.Access) || tok.ExpiresIn < 0) {
			t.Fatalf("accepted %q as %+v", code, tok)
		}
	})
}
