package quota

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func readKey(t *testing.T, s *stub, provider, data string) (Reading, error) {
	t.Helper()
	conn := connOf(provider, "apikey", data)
	return testReader(s, conn, "secret").Read(context.Background(), conn, true)
}

func TestSyntheticReadsItsRequestBucketsAndTheWeeklyPool(t *testing.T) {
	s := newStub().on("api.synthetic.new", "/v2/quotas", `{
		"subscription":{"limit":500,"requests":125,"renewsAt":"2026-11-20T00:00:00Z"},
		"search":{"hourly":{"limit":250,"requests":250,"renewsAt":"2026-11-18T10:00:00Z"}},
		"planWeeklyLimit":{"nextRegenAt":"2026-11-19T00:00:00Z","percentRemaining":84.5,"nextRegenPercent":2}}`)
	out, err := readKey(t, s, "synthetic", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Plan != "Synthetic" {
		t.Errorf("plan %q", out.Plan)
	}
	sub := out.Quotas["Subscription"]
	if sub.Used != 125 || sub.Total != 500 || sub.RemainingPercentage != 75 || sub.ResetAt == nil {
		t.Errorf("subscription %+v", sub)
	}
	if h := out.Quotas["Search (Hourly)"]; h.Used != 250 || h.Total != 250 || h.RemainingPercentage != 0 {
		t.Errorf("search %+v", h)
	}
	if w := out.Quotas["Weekly"]; w.Total != 100 || w.Used != 15.5 || w.RemainingPercentage != 84.5 || w.ResetAt == nil {
		t.Errorf("weekly %+v", w)
	}
	if got := s.header("api.synthetic.new/v2/quotas", "Authorization"); got != "Bearer secret" {
		t.Errorf("authorization %q", got)
	}
}

func TestSyntheticSkipsABucketItDoesNotHave(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		rows []string
	}{
		"no limit":          {`{"subscription":{"limit":0,"requests":0}}`, nil},
		"empty pool":        {`{"subscription":{"limit":10,"requests":1},"planWeeklyLimit":{}}`, []string{"Subscription"}},
		"null remaining":    {`{"planWeeklyLimit":{"percentRemaining":null}}`, nil},
		"requests over cap": {`{"subscription":{"limit":10,"requests":99}}`, []string{"Subscription"}},
	} {
		out, err := readKey(t, newStub().on("api.synthetic.new", "/v2/quotas", tc.body), "synthetic", "")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(out.Quotas) != len(tc.rows) {
			t.Errorf("%s: rows %v, want %v", name, out.Quotas, tc.rows)
		}
		for _, row := range tc.rows {
			if _, ok := out.Quotas[row]; !ok {
				t.Errorf("%s: no %q row", name, row)
			}
		}
		if len(tc.rows) == 0 && out.Message == "" {
			t.Errorf("%s: no rows and no message", name)
		}
	}
	out, _ := readKey(t, newStub().on("api.synthetic.new", "/v2/quotas", `{"subscription":{"limit":10,"requests":99}}`), "synthetic", "")
	if q := out.Quotas["Subscription"]; q.Used != 10 || q.RemainingPercentage != 0 {
		t.Errorf("a count above its cap was not held to it: %+v", q)
	}
}

func TestAGatewayReaderTurnsARefusedKeyIntoTheRetryableError(t *testing.T) {
	for provider, host := range map[string]string{"synthetic": "api.synthetic.new/v2/quotas", "deeprelay": "api.deeprelay.ai/v1/billing/balance"} {
		hostPart, path, _ := strings.Cut(host, "/")
		s := newStub().code(hostPart, "/"+path, 401)
		if _, err := readKey(t, s, provider, ""); !errors.Is(err, ErrAuth) {
			t.Errorf("%s: %v, want ErrAuth", provider, err)
		}
	}
}

func TestDeepRelayReadsTheWalletTheBurnRateAndTheSpend(t *testing.T) {
	s := newStub().
		on("api.deeprelay.ai", "/v1/billing/balance", `{"balance_cents":445,"balance_dollars":4.45,"burn_cents_per_hour":150}`).
		on("api.deeprelay.ai", "/v1/usage", `{"data":[{"cost_cents":120,"bucket_start":"2026-11-10T00:00:00Z"},{"cost_cents":80,"bucket_start":"2026-11-09T00:00:00Z"}]}`)
	out, err := readKey(t, s, "deeprelay", "")
	if err != nil {
		t.Fatal(err)
	}
	bal := out.Quotas["Balance ($)"]
	if !bal.Credit || bal.Total != 4.45 || bal.Currency != "USD" || bal.Unlimited {
		t.Errorf("balance %+v", bal)
	}
	if burn := out.Quotas["Burn rate ($/h)"]; burn.Used != 1.5 || !burn.Unlimited {
		t.Errorf("burn %+v", burn)
	}
	if spend := out.Quotas["Spend (30d)"]; spend.Used != 2 || !spend.Unlimited {
		t.Errorf("spend %+v", spend)
	}
	if got := s.header("api.deeprelay.ai/v1/billing/balance", "Authorization"); got != "Bearer secret" {
		t.Errorf("authorization %q", got)
	}
}

func TestDeepRelayFallsBackToCentsAndOmitsWhatIsEmpty(t *testing.T) {
	s := newStub().
		on("api.deeprelay.ai", "/v1/billing/balance", `{"balance_cents":250,"burn_cents_per_hour":0}`).
		on("api.deeprelay.ai", "/v1/usage", `{"data":[]}`)
	out, err := readKey(t, s, "deeprelay", "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Quotas["Balance ($)"].Total != 2.5 {
		t.Errorf("cents not read: %+v", out.Quotas)
	}
	if _, ok := out.Quotas["Burn rate ($/h)"]; ok {
		t.Error("a zero burn rate is a row of nothing")
	}
	if _, ok := out.Quotas["Spend (30d)"]; ok {
		t.Error("an empty history is a row of nothing")
	}
}

func TestDeepRelaySpendNeverFailsTheBalanceRow(t *testing.T) {
	s := newStub().
		on("api.deeprelay.ai", "/v1/billing/balance", `{"balance_dollars":1}`).
		code("api.deeprelay.ai", "/v1/usage", 500)
	out, err := readKey(t, s, "deeprelay", "")
	if err != nil || out.Quotas["Balance ($)"].Total != 1 || out.Message != "" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestDeepRelayShowsAnEmptyWalletAsSpentAndIgnoresAFailedHistory(t *testing.T) {
	s := newStub().
		on("api.deeprelay.ai", "/v1/billing/balance", `{"balance_cents":0}`).
		code("api.deeprelay.ai", "/v1/usage", 500).
		on("api.deeprelay.ai", "/v1/usage", `{"data":[{"cost_cents":900}]}`)
	out, err := readKey(t, s, "deeprelay", "")
	if err != nil {
		t.Fatal(err)
	}
	if w := out.Quotas["Balance ($)"]; w.Total != 0 || w.RemainingPercentage != 0 {
		t.Errorf("an empty wallet reads as money left: %+v", w)
	}
	if _, ok := out.Quotas["Spend (30d)"]; ok {
		t.Errorf("the body of a failed history call was read: %+v", out.Quotas)
	}
}

func TestDeepRelayNamesAKeyWithoutTheBillingScope(t *testing.T) {
	s := newStub().
		code("api.deeprelay.ai", "/v1/billing/balance", 403).
		on("api.deeprelay.ai", "/v1/billing/balance", `{"error":{"code":"insufficient_scope"}}`)
	out, err := readKey(t, s, "deeprelay", "")
	if err != nil {
		t.Fatalf("a scope refusal is a message, not a renewable failure: %v", err)
	}
	if !strings.Contains(out.Message, "billing:read") {
		t.Errorf("the message does not name the missing scope: %q", out.Message)
	}
}

func TestDeepRelayRefusesToCallWithoutAKey(t *testing.T) {
	conn := connOf("deeprelay", "apikey", "")
	s := newStub()
	if _, err := testReader(s, conn, "").Read(context.Background(), conn, true); err == nil {
		t.Error("an empty credential was read")
	}
	if len(s.calls) != 0 {
		t.Errorf("a call went out with no key: %v", s.calls)
	}
}
