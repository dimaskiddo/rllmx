package google

import (
	"context"
	"net/url"
	"rllmx/app/llm/oauth"
	"rllmx/tests/login"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

const googleBlock = `{"flow":"code-pkce","noPkce":true,"slowRefresh":true,"authorizeUrl":"https://accounts.example/auth","tokenUrl":"@/token","scope":"s1 s2",` +
	`"redirect":"local","exchange":"form","hook":"HOOK","params":[{"name":"access_type","value":"offline"},{"name":"prompt","value":"consent"}],` +
	`"endpoints":{"user":"@/userinfo","loadCodeAssist":"@/load","onboardUser":"@/onboard"},` +
	`"refresh":{"encoding":"form","leadSeconds":300}}`

func googleRig(t *testing.T, hook string) (*login.Login, oauth.Flow) {
	t.Helper()
	block := strings.Replace(googleBlock, "HOOK", hook, 1)
	r := login.NewLogin(t, "gg", block, oauth.Dialects{Hooks: map[string]oauth.Hook{"google": Account, "antigravity": AntigravityAccount}})
	r.Service.ClientSecret = func(string) string { return "SECRET-1" }
	f, err := oauth.FlowFor(r.Reg, r.Service.ClientID, r.Service.ClientSecret, "gg")
	if err != nil {
		t.Fatal(err)
	}
	return r, f
}

func TestAGoogleLoginSendsNoChallengeAndTheSecretOnTheExchange(t *testing.T) {
	r, f := googleRig(t, "google")
	u := f.URL("http://localhost:1/callback", "ST", "CH", "NO")
	for _, bad := range []string{"code_challenge", "CH"} {
		if strings.Contains(u, bad) {
			t.Fatalf("a challenge was sent: %s", u)
		}
	}
	want := "https://accounts.example/auth?response_type=code&client_id=client-gg&redirect_uri=http%3A%2F%2Flocalhost%3A1%2Fcallback&scope=s1%20s2&access_type=offline&prompt=consent&state=ST"
	if u != want {
		t.Fatalf("\n got %s\nwant %s", u, want)
	}
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	if _, err := r.Service.Tokens.Exchange(context.Background(), f, "CODE", "http://localhost:1/callback", "", "ST"); err != nil {
		t.Fatal(err)
	}
	v, _ := url.ParseQuery(r.F.Last("/token").Body)
	if v.Get("client_secret") != "SECRET-1" || v.Get("client_id") != "client-gg" || v.Has("code_verifier") || v.Get("code") != "CODE" {
		t.Fatalf("%v", v)
	}
}

func TestAGoogleRenewalCarriesTheSecret(t *testing.T) {
	r, f := googleRig(t, "google")
	r.F.On("/token", login.OK(`{"access_token":"AT2","expires_in":3600}`))
	tok, err := r.Service.Tokens.Refresh(context.Background(), f, "RT", nil)
	if err != nil || tok.Access != "AT2" || tok.Refresh != "RT" {
		t.Fatalf("%+v %v", tok, err)
	}
	v, _ := url.ParseQuery(r.F.Last("/token").Body)
	if v.Get("client_secret") != "SECRET-1" || v.Get("grant_type") != "refresh_token" {
		t.Fatalf("%v", v)
	}
}

func TestAPkceLoginKeepsItsChallengeUnlessTheDataTurnsItOff(t *testing.T) {
	f := flowFor(t, "claude")
	if u := f.URL("http://localhost:1/cb", "ST", "CH", "NO"); !strings.Contains(u, "code_challenge=CH") {
		t.Fatal(u)
	}
}

func TestTheGoogleHookStoresTheProjectFromEitherShape(t *testing.T) {
	for name, tc := range map[string]struct{ load, want string }{
		"a string":      {`{"cloudaicompanionProject":"my-proj-123"}`, "my-proj-123"},
		"an object":     {`{"cloudaicompanionProject":{"id":"my-proj-123"}}`, "my-proj-123"},
		"a bad id":      {`{"cloudaicompanionProject":"../etc"}`, ""},
		"an upper case": {`{"cloudaicompanionProject":"My-Proj"}`, ""},
		"none":          {`{}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, f := googleRig(t, "google")
			r.F.On("/userinfo", login.OK(`{"email":"g@x.test"}`))
			r.F.On("/load", login.OK(tc.load))
			acc, err := Account(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
			if err != nil || acc.ProjectID != tc.want || acc.Email != "g@x.test" {
				t.Fatalf("%+v %v", acc, err)
			}
			load := r.F.Last("/load")
			if gjson.Get(load.Body, "mode").Int() != 1 || gjson.Get(load.Body, "metadata.ideType").Int() != 9 || load.Header.Get("Authorization") != "Bearer AT" {
				t.Fatalf("%+v", load)
			}
			if r.F.Last("/userinfo").Query != "alt=json" {
				t.Fatalf("%+v", r.F.Last("/userinfo"))
			}
		})
	}
}

func TestAGoogleLoginStillConnectsWhenItsCallsFail(t *testing.T) {
	r, f := googleRig(t, "google")
	r.F.On("/userinfo", login.Reply{Status: 500, Body: "SECRET"})
	r.F.On("/load", login.Reply{Status: 403, Body: "SECRET"})
	acc, err := Account(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
	if err != nil || acc.Email != "" || acc.ProjectID != "" {
		t.Fatalf("%+v %v", acc, err)
	}
}

func TestAntigravityOnboardsInsideTheLoginAndTakesTheProjectItNames(t *testing.T) {
	r, f := googleRig(t, "antigravity")
	r.F.On("/userinfo", login.OK(`{"email":"a@x.test"}`))
	r.F.On("/load", login.OK(`{"cloudaicompanionProject":"first-proj-1","allowedTiers":[{"id":"free-tier"},{"id":" standard-tier ","isDefault":true}]}`))
	r.F.On("/onboard", login.OK(`{"done":false}`), login.OK(`{"done":true,"response":{"cloudaicompanionProject":{"id":"final-proj-2"}}}`))
	acc, err := AntigravityAccount(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
	if err != nil || acc.ProjectID != "final-proj-2" || acc.Email != "a@x.test" {
		t.Fatalf("%+v %v", acc, err)
	}
	if got := gjson.Get(r.F.Last("/onboard").Body, "tierId").String(); got != "standard-tier" {
		t.Fatalf("tier %q", got)
	}
	if gjson.Get(r.F.Last("/load").Body, "mode").Exists() {
		t.Fatal("antigravity sent the Gemini CLI mode")
	}
	if r.F.Count("/onboard") != 2 || len(r.Waits) != 1 || r.Waits[0] != onboardPause {
		t.Fatalf("%d calls, waits %v", r.F.Count("/onboard"), r.Waits)
	}
}

func TestOnboardingIsBoundedAndNeverBlocksTheLogin(t *testing.T) {
	r, f := googleRig(t, "antigravity")
	r.F.On("/load", login.OK(`{"cloudaicompanionProject":"first-proj-1"}`))
	r.F.On("/onboard", login.OK(`{"done":false}`))
	acc, err := AntigravityAccount(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
	if err != nil || acc.ProjectID != "first-proj-1" {
		t.Fatalf("the first project was lost: %+v %v", acc, err)
	}
	if r.F.Count("/onboard") != onboardTries || len(r.Waits) != onboardTries-1 {
		t.Fatalf("%d calls, %d waits", r.F.Count("/onboard"), len(r.Waits))
	}
	if tier := gjson.Get(r.F.Last("/onboard").Body, "tierId").String(); tier != defaultTier {
		t.Fatalf("tier %q", tier)
	}
}

func TestOnboardingIsSkippedWithoutAProjectAndStopsWithTheRequest(t *testing.T) {
	r, f := googleRig(t, "antigravity")
	r.F.On("/load", login.OK(`{}`))
	acc, _ := AntigravityAccount(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
	if acc.ProjectID != "" || r.F.Count("/onboard") != 0 {
		t.Fatalf("%+v, %d onboard calls", acc, r.F.Count("/onboard"))
	}
	r.F.On("/load", login.OK(`{"cloudaicompanionProject":"first-proj-1"}`))
	r.F.On("/onboard", login.OK(`{"done":false}`))
	r.Service.Tokens.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	acc, _ = AntigravityAccount(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
	if acc.ProjectID != "first-proj-1" || r.F.Count("/onboard") != 1 {
		t.Fatalf("%+v, %d onboard calls after cancel", acc, r.F.Count("/onboard"))
	}
}

func TestAProjectIsStoredAsTheConnectionsOwnField(t *testing.T) {
	r, f := googleRig(t, "google")
	r.F.On("/userinfo", login.OK(`{"email":"g@x.test"}`))
	r.F.On("/load", login.OK(`{"cloudaicompanionProject":"my-proj-123"}`))
	r.F.On("/token", login.OK(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	acc, _ := Account(context.Background(), r.Service.Tokens, f, oauth.Tokens{Access: "AT"}, nil)
	if _, err := r.Service.Save(context.Background(), f, oauth.Tokens{Access: "AT", Refresh: "RT"}, acc); err != nil {
		t.Fatal(err)
	}
	in := r.Accounts.Made(t)
	if in.Fields["projectId"] != `"my-proj-123"` || strings.Contains(in.PSD, "my-proj") {
		t.Fatalf("%+v", in)
	}
}

func FuzzProjectID(f *testing.F) {
	for _, s := range []string{"my-proj-123", "../x", "A", "", "a-b-c-d-e", strings.Repeat("a", 40), "a b", "é-proj-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidProject(s) && (strings.ContainsAny(s, "/?#%\\ .:") || len(s) < 6 || len(s) > 30) {
			t.Fatalf("accepted %q", s)
		}
	})
}

func TestGoogleDataIsValidatedAtLoad(t *testing.T) {
	file := func(extra string) fstest.MapFS {
		return fstest.MapFS{"data/x.json": {Data: []byte(`{"id":"x","category":"oauth","transport":{"baseUrl":"https://x.example/v1/chat/completions"},"models":[],"oauth":` +
			`{"flow":"` + extra + `","authorizeUrl":"https://a.example/a","tokenUrl":"https://a.example/t","scope":"s","redirect":"local","exchange":"form","noPkce":true,"refresh":{"encoding":"form","leadSeconds":1}}}`)}}
	}
	if _, err := upstream.Load(file("code-pkce"), "data"); err != nil {
		t.Fatal(err)
	}
	if _, err := upstream.Load(file("device"), "data"); err == nil {
		t.Fatal("noPkce was accepted on a device login")
	}
}

type projectSpy struct {
	mu    sync.Mutex
	saved map[string]string
}

func (s *projectSpy) SetProjectID(_ context.Context, id, project string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saved == nil {
		s.saved = map[string]string{}
	}
	s.saved[id] = project
	return true, nil
}

func resolverRig(t *testing.T, hook string) (*ProjectResolver, *login.Login, *projectSpy) {
	t.Helper()
	r, _ := googleRig(t, hook)
	spy := &projectSpy{}
	return &ProjectResolver{
		Registry: r.Reg, Tokens: r.Service.Tokens, Cache: cache.New(tests.NewCache()),
		Store: func() ProjectStore { return spy },
	}, r, spy
}

func connWith(id, project string) repository.Connection {
	data := `{"accessToken":"AT"`
	if project != "" {
		data += `,"projectId":"` + project + `"`
	}
	return repository.Connection{ID: id, Provider: "gg", AuthType: "oauth", Data: data + `}`}
}

func TestAStoredProjectIsUsedWithoutACall(t *testing.T) {
	p, r, _ := resolverRig(t, "google")
	got, err := p.Project(context.Background(), connWith("c1", "stored-proj-1"), "AT")
	if err != nil || got != "stored-proj-1" || r.F.Count("/load") != 0 {
		t.Fatalf("%q %v, %d calls", got, err, r.F.Count("/load"))
	}
}

func TestAMissingProjectIsFoundStoredAndKeptForABurst(t *testing.T) {
	p, r, spy := resolverRig(t, "google")
	r.F.On("/load", login.OK(`{"cloudaicompanionProject":{"id":"found-proj-1"}}`))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := p.Project(context.Background(), connWith("c1", ""), "AT"); err != nil || got != "found-proj-1" {
				t.Errorf("%q %v", got, err)
			}
		}()
	}
	wg.Wait()
	if r.F.Count("/load") != 1 {
		t.Fatalf("%d lookups for one account", r.F.Count("/load"))
	}
	if spy.saved["c1"] != "found-proj-1" {
		t.Fatalf("%v", spy.saved)
	}
}

func TestALookupThatFindsNothingOnboardsBrieflyAndNeverInventsAProject(t *testing.T) {
	p, r, spy := resolverRig(t, "antigravity")
	r.F.On("/load", login.OK(`{}`))
	r.F.On("/onboard", login.OK(`{"done":false}`))
	got, err := p.Project(context.Background(), connWith("c1", ""), "AT")
	if err == nil || got != "" || len(spy.saved) != 0 {
		t.Fatalf("%q %v %v", got, err, spy.saved)
	}
	if oauth.ClassOf(err) != oauth.ClassRejected {
		t.Fatalf("%v", err)
	}
	if r.F.Count("/onboard") != lookupTries || len(r.Waits) != lookupTries-1 || r.Waits[0] != lookupPause {
		t.Fatalf("%d calls, waits %v", r.F.Count("/onboard"), r.Waits)
	}
}

func TestALookupTakesTheProjectOnboardingNames(t *testing.T) {
	p, r, spy := resolverRig(t, "antigravity")
	r.F.On("/load", login.OK(`{"allowedTiers":[{"id":"t1","isDefault":true}]}`))
	r.F.On("/onboard", login.OK(`{"done":true,"response":{"cloudaicompanionProject":"onb-proj-1"}}`))
	got, err := p.Project(context.Background(), connWith("c1", ""), "AT")
	if err != nil || got != "onb-proj-1" || spy.saved["c1"] != "onb-proj-1" {
		t.Fatalf("%q %v %v", got, err, spy.saved)
	}
	if gjson.Get(r.F.Last("/onboard").Body, "tierId").String() != "t1" {
		t.Fatalf("%s", r.F.Last("/onboard").Body)
	}
}

func TestALookupWithoutATokenOrAnEndpointFailsWithoutACall(t *testing.T) {
	p, r, _ := resolverRig(t, "google")
	if _, err := p.Project(context.Background(), connWith("c1", ""), ""); err != oauth.ErrNoToken {
		t.Fatalf("%v", err)
	}
	other := connWith("c2", "")
	other.Provider = "nope"
	if _, err := p.Project(context.Background(), other, "AT"); oauth.ClassOf(err) != oauth.ClassRejected || r.F.Count("/load") != 0 {
		t.Fatalf("%v", err)
	}
}

func TestOnlyAPlainProjectIdIsAccepted(t *testing.T) {
	for id, want := range map[string]bool{
		"my-proj-123": true, "abcde1": true, "a-b-c-d-e": true, "a" + strings.Repeat("b", 28) + "c": true,
		"../etc": false, "my/proj-123": false, "my.proj.123": false, "My-Proj-1": false, "1abcde": false,
		"abcd": false, "a" + strings.Repeat("b", 29) + "c": false, "abcde-": false, "my proj 1": false, "é-proj-12": false, "": false,
	} {
		if ValidProject(id) != want {
			t.Errorf("%q: %v, want %v", id, ValidProject(id), want)
		}
	}
}

func TestTheLoginsOnboardingStaysUnderTheBound(t *testing.T) {
	if onboardTries > 6 || onboardPause > 5*time.Second || onboardTries*int(onboardPause/time.Second) > 30 {
		t.Fatalf("a login can wait %d x %v on onboarding", onboardTries, onboardPause)
	}
}

func flowFor(t *testing.T, id string) oauth.Flow {
	t.Helper()
	f, err := oauth.FlowFor(login.Registry(t), func(string) string { return "client-" + id }, nil, id)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
