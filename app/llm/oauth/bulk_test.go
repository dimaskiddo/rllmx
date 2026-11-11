package oauth

import (
	"context"
	"encoding/json"
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

func bulkService(t *testing.T) (*Service, *fakeAccounts) {
	t.Helper()
	accounts := &fakeAccounts{}
	return &Service{
		Registry: registry(t), ClientID: func(p string) string { return "client-" + p },
		Sessions: NewSessions(), Tokens: &TokenClient{HTTP: nil},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) }, Accounts: func() Accounts { return accounts },
	}, accounts
}

func TestBulkImportStoresEachAccount(t *testing.T) {
	svc, accounts := bulkService(t)
	access := bulkJWT(t, map[string]any{"email": "a@x.test", "sub": "u1"})
	id := bulkJWT(t, map[string]any{"email": "a@x.test", "account_id": "acct-1", "plan_type": "plus"})
	out, err := svc.BulkImport(context.Background(), "codex", []BulkItem{
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

func TestBulkImportRecordsTheDeviceRenewalKind(t *testing.T) {
	svc, accounts := bulkService(t)
	access := bulkJWT(t, map[string]any{"email": "g@x.test"})
	out, err := svc.BulkImport(context.Background(), "grok-cli", []BulkItem{{Access: access}})
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
	if _, err := svc.BulkImport(context.Background(), "kimi", []BulkItem{{Access: strings.Repeat("a", 20)}}); err != ErrBulk {
		t.Fatalf("a provider without a bulk login: %v, want ErrBulk", err)
	}
	items := make([]BulkItem, BulkLimit+1)
	for i := range items {
		items[i] = BulkItem{Access: strings.Repeat("a", 20)}
	}
	if _, err := svc.BulkImport(context.Background(), "codex", items); err != ErrBulk {
		t.Fatalf("too many items: %v, want ErrBulk", err)
	}
	if _, err := svc.BulkImport(context.Background(), "codex", nil); err != ErrBulk {
		t.Fatalf("no items: %v, want ErrBulk", err)
	}
}
