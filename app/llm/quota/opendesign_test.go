package quota

import (
	"context"
	"strings"
	"testing"
)

const (
	odHost      = "amr-api.open-design.ai"
	odWorkspace = "/api/v1/workspaces"
)

func odData(psd string) string { return `{"providerSpecificData":` + psd + `}` }

const odPreflight = `{"workspaceId":"ws1","balanceUsd":"1.1000","codingPlan":{"eligible":true,"tier":"go","windows":[
	{"durationSeconds":18000,"usedCredits":"32210","limitCredits":"50000","resetsAt":"2026-11-18T14:00:00Z"},
	{"durationSeconds":604800,"usedCredits":"74282","limitCredits":"150000","resetsAt":"2026-11-24T00:00:00Z"}]}}`

func odStub() *stub {
	return newStub().
		on(odHost, odWorkspace, `{"items":[{"workspaceId":"ws1"}]}`).
		on(odHost, odWorkspace+"/ws1/billing/preflight", odPreflight).
		on(odHost, odWorkspace+"/ws1/wallet/balance", `{"workspaceId":"ws1","balanceUsd":"1.1000"}`)
}

func readOD(t *testing.T, s *stub, psd string) Reading {
	t.Helper()
	conn := connOf("opendesign-go", "apikey", odData(psd))
	out, err := testReader(s, conn, "sk-amr-RUNTIME").Read(context.Background(), conn, true)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestOpenDesignReadsThePlanWindowsAndTheWallet(t *testing.T) {
	s := odStub()
	out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K"}`)
	if out.Plan != "OpenDesign Go" {
		t.Errorf("plan %q", out.Plan)
	}
	if q := out.Quotas["Session (5h)"]; q.Used != 32210 || q.Total != 50000 || q.RemainingPercentage != 35.58 || q.ResetAt == nil {
		t.Errorf("session %+v", q)
	}
	if q := out.Quotas["Weekly (7d)"]; q.Used != 74282 || q.Total != 150000 || q.RemainingPercentage != 50.48 {
		t.Errorf("weekly %+v", q)
	}
	if q := out.Quotas["Balance ($)"]; !q.Credit || q.Total != 1.1 || q.Currency != "USD" {
		t.Errorf("wallet %+v", q)
	}
	for _, path := range []string{odWorkspace, odWorkspace + "/ws1/billing/preflight", odWorkspace + "/ws1/wallet/balance"} {
		if got := s.header(odHost+path, "Authorization"); got != "Bearer vela_ctrl_K" {
			t.Errorf("%s: authorization %q: the control key must be what is presented, never the runtime key", path, got)
		}
	}
	if got := s.header(odHost+odWorkspace+"/ws1/billing/preflight", "x-vela-workspace-id"); got != "ws1" {
		t.Errorf("workspace header %q", got)
	}
}

func TestOpenDesignAsksForTheControlKeyAndNeverSendsTheRuntimeKeyToTheControlPlane(t *testing.T) {
	s := odStub()
	for name, psd := range map[string]string{"none": `{}`, "blank": `{"opendesignControlKey":"  "}`} {
		out := readOD(t, s, psd)
		if !strings.Contains(out.Message, "Control Key") || len(out.Quotas) != 0 {
			t.Errorf("%s: %+v", name, out)
		}
	}
	if len(s.calls) != 0 {
		t.Errorf("a call went out with no control key: %v", s.calls)
	}
}

func TestOpenDesignRefusesToGuessAmongSeveralWorkspaces(t *testing.T) {
	s := newStub().on(odHost, odWorkspace, `{"items":[{"workspaceId":"team"},{"workspaceId":"mine"}]}`)
	out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K"}`)
	if len(out.Quotas) != 0 || !strings.Contains(out.Message, "opendesignWorkspaceId") {
		t.Fatalf("%+v", out)
	}
	for _, c := range s.calls {
		if strings.Contains(c, "billing") || strings.Contains(c, "wallet") {
			t.Errorf("another workspace's numbers were asked for: %s", c)
		}
	}
}

func TestOpenDesignUsesThePinnedWorkspaceWithoutListing(t *testing.T) {
	s := newStub().
		on(odHost, odWorkspace+"/mine/billing/preflight", strings.ReplaceAll(odPreflight, "ws1", "mine")).
		on(odHost, odWorkspace+"/mine/wallet/balance", `{"workspaceId":"mine","balanceUsd":"2"}`)
	out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K","opendesignWorkspaceId":"mine"}`)
	if s.counted(odHost+odWorkspace) != 0 || out.Quotas["Balance ($)"].Total != 2 {
		t.Fatalf("listed %d times: %+v", s.counted(odHost+odWorkspace), out)
	}
}

func TestOpenDesignRejectsAnAnswerForAnotherWorkspace(t *testing.T) {
	s := odStub().
		on(odHost, odWorkspace+"/ws1/billing/preflight", strings.ReplaceAll(odPreflight, "ws1", "other")).
		on(odHost, odWorkspace+"/ws1/wallet/balance", `{"workspaceId":"other","balanceUsd":"9"}`)
	out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K"}`)
	if len(out.Quotas) != 0 || !strings.Contains(out.Message, "mismatch") {
		t.Fatalf("another workspace's numbers were shown: %+v", out)
	}
}

func TestOpenDesignOnlyAsksForAWorkspaceIDThatIsPlainText(t *testing.T) {
	for _, id := range []string{"a/../b", "a?x=1", "a b", "a#b", "", strings.Repeat("a", 129)} {
		s := newStub()
		out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K","opendesignWorkspaceId":"`+id+`"}`)
		if id == "" {
			continue
		}
		if len(s.calls) != 0 || out.Message == "" {
			t.Errorf("%q: %v %+v", id, s.calls, out)
		}
	}
	listed := newStub().on(odHost, odWorkspace, `{"items":[{"workspaceId":"../x"}]}`)
	if out := readOD(t, listed, `{"opendesignControlKey":"vela_ctrl_K"}`); len(listed.calls) != 1 || out.Message == "" {
		t.Errorf("an id the vendor listed was not checked: %v %+v", listed.calls, out)
	}
}

func TestOneOpenDesignSourceFailingKeepsTheOther(t *testing.T) {
	s := odStub().code(odHost, odWorkspace+"/ws1/wallet/balance", 500)
	out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K"}`)
	if _, ok := out.Quotas["Session (5h)"]; !ok {
		t.Errorf("the plan windows were lost with the wallet: %+v", out)
	}
	s = odStub().code(odHost, odWorkspace+"/ws1/billing/preflight", 500).code(odHost, odWorkspace+"/ws1/wallet/balance", 500)
	if out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K"}`); len(out.Quotas) != 0 || out.Message == "" {
		t.Errorf("both failed and nothing said so: %+v", out)
	}
}

func TestOpenDesignCreditPoolsBeyondAFloatKeepTheirPercent(t *testing.T) {
	big := `{"workspaceId":"ws1","codingPlan":{"eligible":true,"windows":[{"durationSeconds":18000,"usedCredits":"1","limitCredits":"9007199254740993"},{"durationSeconds":604800,"usedCredits":"9007199254740993","limitCredits":"9007199254740993"}]}}`
	s := odStub().on(odHost, odWorkspace+"/ws1/billing/preflight", big)
	out := readOD(t, s, `{"opendesignControlKey":"vela_ctrl_K"}`)
	if q := out.Quotas["Session (5h)"]; q.RemainingPercentage != 100 {
		t.Errorf("one credit used of 2^53+1 must still read as 100%% left: %+v", q)
	}
	if q := out.Quotas["Weekly (7d)"]; q.RemainingPercentage != 0 {
		t.Errorf("a spent pool must read as 0%% left: %+v", q)
	}
}

func TestOpenDesignSkipsWhatItCannotReadExactly(t *testing.T) {
	for name, window := range map[string]string{
		"fraction": `{"durationSeconds":18000,"usedCredits":"12.5","limitCredits":"100"}`,
		"negative": `{"durationSeconds":18000,"usedCredits":"-1","limitCredits":"100"}`,
		"zero cap": `{"durationSeconds":18000,"usedCredits":"0","limitCredits":"0"}`,
		"text":     `{"durationSeconds":18000,"usedCredits":"lots","limitCredits":"100"}`,
		"unnamed":  `{"durationSeconds":3600,"usedCredits":"1","limitCredits":"100"}`,
		"over cap": `{"durationSeconds":18000,"usedCredits":"500","limitCredits":"100"}`,
		"signed":   `{"durationSeconds":18000,"usedCredits":"+5","limitCredits":"100"}`,
		"grouped":  `{"durationSeconds":18000,"usedCredits":"1_0","limitCredits":"100"}`,
		"hex":      `{"durationSeconds":18000,"usedCredits":"0x10","limitCredits":"100"}`,
	} {
		body := `{"workspaceId":"ws1","codingPlan":{"eligible":true,"windows":[` + window + `]}}`
		out := readOD(t, odStub().on(odHost, odWorkspace+"/ws1/billing/preflight", body), `{"opendesignControlKey":"vela_ctrl_K"}`)
		q, ok := out.Quotas["Session (5h)"]
		if name == "over cap" {
			if !ok || q.RemainingPercentage != 0 {
				t.Errorf("%s: %+v", name, q)
			}
			continue
		}
		if ok {
			t.Errorf("%s: a row from numbers that cannot be read exactly: %+v", name, q)
		}
	}
}

func TestAnIneligibleOpenDesignPlanHasNoWindows(t *testing.T) {
	body := `{"workspaceId":"ws1","codingPlan":{"eligible":false,"windows":[{"durationSeconds":18000,"usedCredits":"1","limitCredits":"10"}]}}`
	out := readOD(t, odStub().on(odHost, odWorkspace+"/ws1/billing/preflight", body), `{"opendesignControlKey":"vela_ctrl_K"}`)
	if _, ok := out.Quotas["Session (5h)"]; ok {
		t.Errorf("%+v", out.Quotas)
	}
}

func TestARefusedOpenDesignControlKeyIsTheRetryableError(t *testing.T) {
	s := newStub().code(odHost, odWorkspace, 401)
	conn := connOf("opendesign-go", "apikey", odData(`{"opendesignControlKey":"vela_ctrl_K"}`))
	out, err := testReader(s, conn, "sk-amr-RUNTIME").Read(context.Background(), conn, true)
	if err != nil || strings.Contains(out.Message, "vela_ctrl_K") {
		t.Fatalf("%+v %v", out, err)
	}
	if out.Message == "" {
		t.Error("a refused control key said nothing")
	}
}

func TestOpenDesignAlsoReadsWholeNumbersSentAsJSONNumbers(t *testing.T) {
	body := `{"workspaceId":"ws1","codingPlan":{"eligible":true,"windows":[{"durationSeconds":18000,"usedCredits":25,"limitCredits":100}]}}`
	out := readOD(t, odStub().on(odHost, odWorkspace+"/ws1/billing/preflight", body), `{"opendesignControlKey":"vela_ctrl_K"}`)
	if q := out.Quotas["Session (5h)"]; q.Used != 25 || q.Total != 100 || q.RemainingPercentage != 75 {
		t.Errorf("%+v", q)
	}
}
