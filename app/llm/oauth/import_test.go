package oauth

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// importRegistry is a provider file whose login is a pasted token with one
// value beside it.
func importRegistry(t *testing.T, flow string) *upstream.Registry {
	t.Helper()
	fields := ""
	if flow == "import" {
		fields = `,"fields":["machineId"]`
	}
	file := `{"id":"imp","category":"oauth","transport":{"baseUrl":"https://x.example/v1/chat/completions"},` +
		`"oauth":{"flow":"` + flow + `","tokenUrl":"https://x.example/models","scope":""` + fields + `},"models":[]}`
	if flow != "import" {
		file = `{"id":"imp","category":"oauth","transport":{"baseUrl":"https://x.example/v1/chat/completions"},` +
			`"oauth":{"flow":"code","authorizeUrl":"https://x.example/authorize","tokenUrl":"https://x.example/token","scope":"s","redirect":"local","dialect":"zed","noClient":true,"refresh":{"encoding":"json"}},"models":[]}`
	}
	reg, err := upstream.Load(fstest.MapFS{"data/imp.json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func importService(t *testing.T, flow string) (*Service, *fakeAccounts) {
	t.Helper()
	accounts := &fakeAccounts{}
	return &Service{
		Registry: importRegistry(t, flow), ClientID: func(string) string { return "" },
		Sessions: NewSessions(), Tokens: &TokenClient{HTTP: http.DefaultClient},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }, Accounts: func() Accounts { return accounts },
	}, accounts
}

func TestImportTokenStoresAPastedTokenAndItsExtraValue(t *testing.T) {
	svc, accounts := importService(t, "import")
	token := "TOKEN-" + strings.Repeat("b", 60) + "." + "eyJlbWFpbCI6Im9AeC50ZXN0IiwiZXhwIjo0MTAyNDQ0ODAwfQ" + ".sig"
	id, err := svc.ImportToken(context.Background(), "imp", map[string]string{"accessToken": token, "machineId": "machine-9"})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if id == "" || len(accounts.made) != 1 {
		t.Fatalf("id = %q, stored %d", id, len(accounts.made))
	}
	made := accounts.made[0]
	if made.AuthType != "oauth" {
		t.Fatalf("auth type = %q", made.AuthType)
	}
	if gjson.Get(made.Fields["accessToken"], "").String() != token && made.Fields["accessToken"] == "" {
		t.Fatalf("the token was not stored")
	}
	psd := gjson.Parse(made.PSD)
	if psd.Get("machineId").String() != "machine-9" || psd.Get("authMethod").String() != "imported" {
		t.Fatalf("provider data = %s", made.PSD)
	}
	if made.Email != "o@x.test" {
		t.Fatalf("email = %q", made.Email)
	}
	if made.Fields["expiresAt"] == "" {
		t.Fatal("the JWT's expiry was not stored")
	}
	if made.Fields["refreshToken"] != "" {
		t.Fatal("an imported token has no refresh token")
	}
}

func TestImportTokenRefusesWhatIsMissing(t *testing.T) {
	svc, _ := importService(t, "import")
	token := "TOKEN-" + strings.Repeat("c", 60)
	if _, err := svc.ImportToken(context.Background(), "imp", map[string]string{"accessToken": token}); err != ErrImportField {
		t.Fatalf("missing machine id: %v, want ErrImportField", err)
	}
	if _, err := svc.ImportToken(context.Background(), "imp", map[string]string{"accessToken": "short", "machineId": "m"}); err != ErrToken {
		t.Fatalf("short token: %v, want ErrToken", err)
	}
}

func TestImportTokenRefusesAProviderThatLogsInInABrowser(t *testing.T) {
	svc, _ := importService(t, "code")
	_, err := svc.ImportToken(context.Background(), "imp", map[string]string{"accessToken": "TOKEN-" + strings.Repeat("d", 60)})
	if err != ErrUnsupported {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}
