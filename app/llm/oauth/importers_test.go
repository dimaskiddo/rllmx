package oauth_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
)

// importerService builds a service for a provider whose login names an
// importer.
func importerService(t *testing.T, id, importer string) (*oauth.Service, *fakeAccounts) {
	t.Helper()
	file := `{"id":"` + id + `","category":"oauth","transport":{"baseUrl":"https://x.example/v1"},"models":[],` +
		`"oauth":{"flow":"device","deviceUrl":"https://x.example/device","tokenUrl":"https://x.example/token","scope":"s","noClient":true,"importer":"` + importer + `"}}`
	reg, err := upstream.Load(fstest.MapFS{"data/" + id + ".json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	accounts := &fakeAccounts{}
	return &oauth.Service{
		Registry: reg, ClientID: func(string) string { return "" }, Sessions: oauth.NewSessions(),
		Tokens: &oauth.TokenClient{HTTP: http.DefaultClient}, Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
		Accounts: func() oauth.Accounts { return accounts },
	}, accounts
}

func jwtOf(payload string) string {
	enc := base64.RawURLEncoding.EncodeToString
	return "TOKEN-" + strings.Repeat("a", 60) + "." + enc([]byte(payload)) + ".sig"
}

func TestACodexTokenIsImportedWithItsAccountAndPlan(t *testing.T) {
	svc, accounts := importerService(t, "codex", "codex-token")
	token := jwtOf(`{"email":"o@x.test","exp":4102444800,"https://api.openai.com/auth":{"chatgpt_account_id":"acc-9","chatgpt_plan_type":"plus"}}`)
	id, err := svc.ImportToken(context.Background(), "codex", map[string]string{"accessToken": token})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if id == "" || len(accounts.made) != 1 {
		t.Fatalf("stored %d", len(accounts.made))
	}
	made := accounts.made[0]
	psd := gjson.Parse(made.PSD)
	if made.Email != "o@x.test" || psd.Get("authMethod").String() != "access_token" ||
		psd.Get("chatgptAccountId").String() != "acc-9" || psd.Get("chatgptPlanType").String() != "plus" {
		t.Fatalf("account %+v psd %s", made, made.PSD)
	}
}

func TestAZedTokenIsImportedWithItsSystemID(t *testing.T) {
	svc, accounts := importerService(t, "zed", "zed")
	if _, err := svc.ImportToken(context.Background(), "zed", map[string]string{"accessToken": "TOKEN-" + strings.Repeat("c", 60)}); err != oauth.ErrToken {
		t.Fatalf("a token with no user id: %v", err)
	}
	_, err := svc.ImportToken(context.Background(), "zed", map[string]string{
		"accessToken": "TOKEN-" + strings.Repeat("c", 60), "userId": "u1", "systemId": "s1",
	})
	if err != nil || len(accounts.made) != 1 {
		t.Fatalf("%v stored %d", err, len(accounts.made))
	}
	psd := gjson.Parse(accounts.made[0].PSD)
	if psd.Get("userId").String() != "u1" || psd.Get("systemId").String() != "s1" {
		t.Fatalf("psd %s", accounts.made[0].PSD)
	}
}

func TestAKiroExternalIDPBodyIsImportedAsTokens(t *testing.T) {
	svc, accounts := importerService(t, "kiro", "kiro-external-idp")
	body := `{"auth_method":"external_idp","access_token":"AT-` + strings.Repeat("x", 50) + `","refresh_token":"RT","client_id":"cid","scopes":"sso:account:access","profile_arn":"arn:aws:x"}`
	if _, err := svc.ImportToken(context.Background(), "kiro", map[string]string{"json": body}); err != nil {
		t.Fatal(err)
	}
	psd := gjson.Parse(accounts.made[0].PSD)
	if psd.Get("authMethod").String() != "external_idp" || psd.Get("profileArn").String() != "arn:aws:x" || psd.Get("clientId").String() != "cid" {
		t.Fatalf("psd %s", accounts.made[0].PSD)
	}
	if _, err := svc.ImportToken(context.Background(), "kiro", map[string]string{"json": `{"auth_method":"other"}`}); err != oauth.ErrImportField {
		t.Fatalf("a foreign body: %v", err)
	}
}

func TestAnImporterThatIsNotBuiltAnswersUnsupported(t *testing.T) {
	for _, name := range []string{"kiro-apikey", "mimo-key"} {
		svc, _ := importerService(t, "imp", name)
		if _, err := svc.ImportToken(context.Background(), "imp", map[string]string{"accessToken": "TOKEN-" + strings.Repeat("d", 60)}); err != oauth.ErrUnsupported {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
