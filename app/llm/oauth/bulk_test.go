package oauth_test

import (
	"context"
	"encoding/json"
	"rllmx/app/llm/oauth"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

// bulkJWT is an unsigned token whose payload carries the claims given.
func bulkJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return jwt(string(raw))
}

func bulkService(t *testing.T) (*oauth.Service, *fakeAccounts) {
	t.Helper()
	accounts := &fakeAccounts{}
	return &oauth.Service{
		Registry: registry(t), ClientID: func(p string) string { return "client-" + p },
		Sessions: oauth.NewSessions(), Tokens: &oauth.TokenClient{HTTP: nil},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }, Accounts: func() oauth.Accounts { return accounts },
	}, accounts
}

func TestBulkImportStoresEachAccount(t *testing.T) {
	svc, accounts := bulkService(t)
	access := bulkJWT(t, map[string]any{"email": "a@x.test", "sub": "u1"})
	id := bulkJWT(t, map[string]any{"email": "a@x.test", "account_id": "acct-1", "plan_type": "plus"})
	out, err := svc.BulkImport(context.Background(), "codex", []oauth.BulkItem{
		{Access: access, ID: id, Refresh: "r1", ExpiresIn: 3600},
		{Access: ""},
	})
	if err != nil {
		t.Fatalf("bulk: %v", err)
	}
	if len(out) != 2 || out[0].ID == "" || out[1].Failure != "invalid_token" {
		t.Fatalf("outcomes = %+v", out)
	}
	if len(accounts.made) != 1 {
		t.Fatalf("stored %d accounts", len(accounts.made))
	}
	made := accounts.made[0]
	if made.AuthType != "oauth" || made.Email != "a@x.test" {
		t.Fatalf("account = %+v", made)
	}
	if gjson.Get(made.PSD, "chatgptAccountId").String() != "acct-1" || gjson.Get(made.PSD, "chatgptPlanType").String() != "plus" {
		t.Fatalf("provider data = %s", made.PSD)
	}
	if made.Fields["refreshToken"] == "" || made.Fields["expiresAt"] == "" {
		t.Fatalf("fields = %v", made.Fields)
	}
}

func TestBulkImportReadsTheAccountFromTheAccessTokenAlone(t *testing.T) {
	svc, accounts := bulkService(t)
	access := bulkJWT(t, map[string]any{
		"email":                       "a@x.test",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-nested", "chatgpt_plan_type": "pro"},
	})
	out, err := svc.BulkImport(context.Background(), "codex", []oauth.BulkItem{{Access: access}})
	if err != nil || len(out) != 1 || out[0].ID == "" {
		t.Fatalf("outcomes = %+v %v", out, err)
	}
	made := accounts.made[0]
	if made.Email != "a@x.test" || gjson.Get(made.PSD, "chatgptAccountId").String() != "acct-nested" || gjson.Get(made.PSD, "chatgptPlanType").String() != "pro" {
		t.Fatalf("account = %+v, data = %s", made, made.PSD)
	}
}

func TestBulkImportStoresATokenThatNamesNoAccount(t *testing.T) {
	svc, accounts := bulkService(t)
	out, err := svc.BulkImport(context.Background(), "codex", []oauth.BulkItem{{Access: "an-access-token-that-is-not-a-jwt"}})
	if err != nil || len(out) != 1 || out[0].ID == "" {
		t.Fatalf("outcomes = %+v %v", out, err)
	}
	if made := accounts.made[0]; made.Email != "" || made.PSD != "" {
		t.Fatalf("an unreadable token was given an account: %+v", made)
	}
}

func TestBulkImportRecordsTheDeviceRenewalKind(t *testing.T) {
	svc, accounts := bulkService(t)
	access := bulkJWT(t, map[string]any{"email": "g@x.test"})
	out, err := svc.BulkImport(context.Background(), "grok-cli", []oauth.BulkItem{{Access: access}})
	if err != nil {
		t.Fatalf("bulk: %v", err)
	}
	if len(out) != 1 || out[0].ID == "" {
		t.Fatalf("outcomes = %+v", out)
	}
	if gjson.Get(accounts.made[0].PSD, "authMethod").String() != "device_code" {
		t.Fatalf("provider data = %s", accounts.made[0].PSD)
	}
}

func TestBulkImportRefusesWhatItCannotTake(t *testing.T) {
	svc, _ := bulkService(t)
	if _, err := svc.BulkImport(context.Background(), "kimi", []oauth.BulkItem{{Access: strings.Repeat("a", 20)}}); err != oauth.ErrBulk {
		t.Fatalf("a provider without a bulk login: %v, want ErrBulk", err)
	}
	items := make([]oauth.BulkItem, oauth.BulkLimit+1)
	for i := range items {
		items[i] = oauth.BulkItem{Access: strings.Repeat("a", 20)}
	}
	if _, err := svc.BulkImport(context.Background(), "codex", items); err != oauth.ErrBulk {
		t.Fatalf("too many items: %v, want ErrBulk", err)
	}
	if _, err := svc.BulkImport(context.Background(), "codex", nil); err != oauth.ErrBulk {
		t.Fatalf("no items: %v, want ErrBulk", err)
	}
}
