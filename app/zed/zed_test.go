package zed

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/cache"
	"rllmx/tests"
)

func parsePublic(s string) (*rsa.PublicKey, error) {
	der, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	return x509.ParsePKCS1PublicKey(der)
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestASignInMakesAnAddressAndReadsItsOwnAnswer(t *testing.T) {
	s, addr, err := NewSignIn(NativeAppPort)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !strings.HasPrefix(addr, WebURL+"/native_app_signin?") ||
		!strings.Contains(addr, "native_app_port=58443") || !strings.Contains(addr, "system_id=") {
		t.Fatalf("address %s", addr)
	}
	pub, err := parsePublic(s.Public)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	enc, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte("AT-123"), nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Decrypt(base64URL(enc))
	if err != nil || got != "AT-123" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestASignInRefusesWhatItCannotRead(t *testing.T) {
	s, _, err := NewSignIn(NativeAppPort)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := NewSignIn(NativeAppPort)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := parsePublic(other.Public)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pub, []byte("AT-123"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decrypt(base64URL(enc)); err == nil {
		t.Fatal("another login's answer was read")
	}
	if _, err := s.Decrypt("not base64!!"); err == nil {
		t.Fatal("garbage was accepted")
	}
	// A client that pads its base64 is read the same way.
	own, err := parsePublic(s.Public)
	if err != nil {
		t.Fatal(err)
	}
	mine, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, own, []byte("AT-123"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Decrypt(base64.URLEncoding.EncodeToString(mine)); err != nil || got != "AT-123" {
		t.Fatalf("padded: %q %v", got, err)
	}
}

func TestTheCallbackIsReadFromAnAddressAQueryOrJSON(t *testing.T) {
	for name, raw := range map[string]string{
		"the whole address":       `http://127.0.0.1:58443/?user_id=U1&access_token=ENC`,
		"a bare query":            `user_id=U1&access_token=ENC`,
		"a leading question mark": `?user_id=U1&access_token=ENC`,
		"the camel case names":    `userId=U1&accessToken=ENC`,
		"json":                    `{"user_id":"U1","access_token":"ENC"}`,
	} {
		cb, err := ParseCallback(raw)
		if err != nil || cb.UserID != "U1" || cb.Encrypted != "ENC" {
			t.Errorf("%s: %+v %v", name, cb, err)
		}
	}
	for name, raw := range map[string]string{"empty": "", "no token": "user_id=U1", "no user": "access_token=E"} {
		if _, err := ParseCallback(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheTokenIsMintedOnceAndCached(t *testing.T) {
	var mints int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mints++
		if r.URL.Path != "/client/llm_tokens" {
			t.Errorf("path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "U1 AT" {
			t.Errorf("authorization %q", got)
		}
		_, _ = w.Write([]byte(`{"token":["LLM-1"]}`))
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Cache: cache.New(tests.NewCache()), Base: srv.URL}
	a := Account{Access: "AT", UserID: "U1", OrganizationID: "org-1"}
	for range 3 {
		tok, err := c.Token(context.Background(), a, false)
		if err != nil || tok != "LLM-1" {
			t.Fatalf("%q %v", tok, err)
		}
	}
	if mints != 1 {
		t.Fatalf("%d mints for three reads", mints)
	}
	if _, err := c.Token(context.Background(), a, true); err != nil {
		t.Fatal(err)
	}
	if mints != 2 {
		t.Fatalf("%d mints after a forced one", mints)
	}
}

func TestTheOrganizationIsReadWhenTheAccountNamesNone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/client/users/me":
			_, _ = w.Write([]byte(`{"organizations":[{"id":"personal","is_personal":true}]}`))
		case "/client/llm_tokens":
			body, _ := io.ReadAll(r.Body)
			if got := gjson.GetBytes(body, "organization_id").String(); got != "personal" {
				t.Errorf("organization %q", got)
			}
			_, _ = w.Write([]byte(`{"token":"LLM"}`))
		}
	}))
	defer srv.Close()
	c := &Client{HTTP: srv.Client(), Cache: cache.New(tests.NewCache()), Base: srv.URL}
	if _, err := c.Token(context.Background(), Account{Access: "AT", UserID: "U1"}, false); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestAnAccountWithoutATokenAsksNothing(t *testing.T) {
	c := &Client{HTTP: http.DefaultClient}
	if _, err := c.Token(context.Background(), Account{}, false); err != ErrNoToken {
		t.Fatalf("%v", err)
	}
}
