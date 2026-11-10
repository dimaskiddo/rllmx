package oauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/tests"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func saKey(pemText string) upstream.CloudKey {
	return upstream.CloudKey{Kind: upstream.KeyServiceAccount, Project: "my-proj-123", Account: "sa@my-proj-123.iam.gserviceaccount.com", PrivateKey: pemText}
}

func TestAServiceAccountAssertionIsASignedRS256JWTForTheTokenAddress(t *testing.T) {
	priv, pemText := testKey(t)
	now := time.Unix(1_700_000_000, 0)
	jwt, err := signAssertion(saKey(pemText), "https://tok.example/token", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("%d parts", len(parts))
	}
	dec := func(s string) []byte { b, _ := base64.RawURLEncoding.DecodeString(s); return b }
	if h := dec(parts[0]); gjson.GetBytes(h, "alg").String() != "RS256" || gjson.GetBytes(h, "typ").String() != "JWT" {
		t.Fatalf("%s", h)
	}
	c := dec(parts[1])
	if gjson.GetBytes(c, "iss").String() != "sa@my-proj-123.iam.gserviceaccount.com" || gjson.GetBytes(c, "aud").String() != "https://tok.example/token" ||
		gjson.GetBytes(c, "scope").String() != vertexScope || gjson.GetBytes(c, "iat").Int() != now.Unix() || gjson.GetBytes(c, "exp").Int() != now.Add(time.Hour).Unix() {
		t.Fatalf("%s", c)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&priv.PublicKey, crypto.SHA256, digest[:], dec(parts[2])); err != nil {
		t.Fatalf("the signature does not verify: %v", err)
	}
}

func TestAKeyThatIsNotAnRSAPEMIsRefusedWithoutEchoingIt(t *testing.T) {
	for name, pk := range map[string]string{
		"empty": "", "not pem": "SECRETKEYTEXT", "bad der": "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
		"escaped newlines": strings.ReplaceAll("-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n", "\n", `\n`),
	} {
		_, err := signAssertion(saKey(pk), "https://tok.example", time.Now())
		if err == nil || strings.Contains(err.Error(), "SECRETKEYTEXT") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAServiceAccountKeyWithEscapedNewlinesStillSigns(t *testing.T) {
	_, pemText := testKey(t)
	escaped := strings.ReplaceAll(pemText, "\n", `\n`)
	if _, err := signAssertion(saKey(escaped), "https://tok.example", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func minterRig(t *testing.T) (*GoogleMinter, *fake) {
	t.Helper()
	f := newFake(t)
	now := time.Unix(1_700_000_000, 0)
	return &GoogleMinter{
		Tokens: &TokenClient{HTTP: f.Client(), Now: func() time.Time { return now }},
		Cache:  cache.New(tests.NewCache()), Now: func() time.Time { return now }, URL: f.URL + "/token",
	}, f
}

func TestAServiceAccountTokenIsMintedOnceForABurst(t *testing.T) {
	_, pemText := testKey(t)
	m, f := minterRig(t)
	f.on("/token", ok(`{"access_token":"VTOK","expires_in":3600}`))
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tok, err := m.Token(context.Background(), saKey(pemText)); err != nil || tok != "VTOK" {
				t.Errorf("%q %v", tok, err)
			}
		}()
	}
	wg.Wait()
	if f.count("/token") != 1 {
		t.Fatalf("%d mints", f.count("/token"))
	}
	v, _ := url.ParseQuery(f.last("/token").Body)
	if v.Get("grant_type") != jwtBearer || strings.Count(v.Get("assertion"), ".") != 2 {
		t.Fatalf("%v", v)
	}
}

func TestUserCredentialsRefreshWithTheirOwnClient(t *testing.T) {
	m, f := minterRig(t)
	f.on("/token", ok(`{"access_token":"UTOK","expires_in":3600}`))
	key := upstream.CloudKey{Kind: upstream.KeyADC, ClientID: "cid", ClientSecret: "csec", RefreshToken: "rt"}
	tok, err := m.Token(context.Background(), key)
	if err != nil || tok != "UTOK" {
		t.Fatalf("%q %v", tok, err)
	}
	v, _ := url.ParseQuery(f.last("/token").Body)
	if v.Get("grant_type") != "refresh_token" || v.Get("client_id") != "cid" || v.Get("client_secret") != "csec" || v.Get("refresh_token") != "rt" {
		t.Fatalf("%v", v)
	}
}

func TestARawKeyNeedsNoTokenAndNoCall(t *testing.T) {
	m, f := minterRig(t)
	tok, err := m.Token(context.Background(), upstream.CloudKey{Kind: upstream.KeyRaw})
	if err != nil || tok != "" || f.count("/token") != 0 {
		t.Fatalf("%q %v", tok, err)
	}
}

func TestAMintThatFailsGivesAFixedClassAndNoProviderText(t *testing.T) {
	_, pemText := testKey(t)
	for name, reply := range map[string]fakeReply{
		"a refusal":      {Status: 400, Body: `{"error":"invalid_grant","error_description":"SECRET"}`},
		"a server error": {Status: 503, Body: "SECRET"},
		"no token":       ok(`{}`),
		"a bad token":    ok("{\"access_token\":\"a\\u0000b\"}"),
	} {
		m, f := minterRig(t)
		f.on("/token", reply)
		_, err := m.Token(context.Background(), saKey(pemText))
		if err == nil || strings.Contains(err.Error(), "SECRET") || ClassOf(err) == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
	m, _ := minterRig(t)
	if _, err := m.Token(context.Background(), upstream.CloudKey{Kind: upstream.KeyServiceAccount, PrivateKey: "x"}); err != ErrBadKey {
		t.Fatalf("%v", err)
	}
}

func TestTheCloudKeyParserSeparatesKindsAndRefusesHalfFiles(t *testing.T) {
	sa, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "my-proj-123", "client_email": "a@b", "private_key": "K"})
	for name, tc := range map[string]struct {
		text string
		kind string
		ok   bool
	}{
		"raw key":           {"AIzaSyABC", upstream.KeyRaw, true},
		"service account":   {string(sa), upstream.KeyServiceAccount, true},
		"adc":               {`{"type":"authorized_user","client_id":"i","client_secret":"s","refresh_token":"r","quota_project_id":"my-proj-123"}`, upstream.KeyADC, true},
		"sa no private key": {`{"type":"service_account","project_id":"my-proj-123","client_email":"a@b"}`, "", false},
		"sa bad project":    {`{"type":"service_account","project_id":"../x","client_email":"a@b","private_key":"K"}`, "", false},
		"adc no token":      {`{"type":"authorized_user","client_id":"i","client_secret":"s"}`, "", false},
		"unknown type":      {`{"type":"external_account"}`, "", false},
		"broken json":       {`{"type":`, "", false},
		"empty":             {"  ", "", false},
		"over the size cap": {strings.Repeat("a", 17<<10), "", false},
	} {
		k, ok := upstream.ParseCloudKey(tc.text)
		if ok != tc.ok || (ok && k.Kind != tc.kind) {
			t.Errorf("%s: %+v %v", name, k, ok)
		}
	}
	k, _ := upstream.ParseCloudKey(`{"type":"authorized_user","client_id":"i","client_secret":"s","refresh_token":"r","quota_project_id":"bad project"}`)
	if k.Project != "" {
		t.Fatalf("a bad quota project was kept: %q", k.Project)
	}
}

func FuzzParseCloudKey(f *testing.F) {
	for _, s := range []string{"", "AIza", `{}`, `{"type":"service_account"}`, `{"type":"authorized_user","client_id":"i","client_secret":"s","refresh_token":"r"}`, "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		k, ok := upstream.ParseCloudKey(s)
		if ok && k.Project != "" && !upstream.ValidProject(k.Project) {
			t.Fatalf("accepted project %q", k.Project)
		}
		if !ok && k.Kind != "" {
			t.Fatalf("a refused key has a kind: %+v", k)
		}
	})
}
