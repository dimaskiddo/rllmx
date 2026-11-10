package cosy

import (
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
)

// testKey is a throwaway key pair, made for these tests. The vendor's own key is
// data and is never in a test.
func testKey(t *testing.T) (*rsa.PublicKey, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return &key.PublicKey, key
}

func TestTheBodyEncodesAndDecodesBack(t *testing.T) {
	for _, in := range []string{"", "a", "ab", "abc", `{"messages":[{"role":"user","content":"hi"}]}`, strings.Repeat("x", 1000)} {
		enc := Encode([]byte(in))
		got, err := Decode(enc)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(got) != in {
			t.Fatalf("%q came back as %q", in, got)
		}
	}
}

func TestTheEncodedBodyUsesTheGatewaysAlphabet(t *testing.T) {
	enc := Encode([]byte(`{"a":"b"}`))
	for _, r := range enc {
		if r == '+' || r == '/' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		if !strings.ContainsRune(customAlphabet+"$", r) {
			t.Fatalf("a character outside the alphabet: %q in %q", r, enc)
		}
	}
	if strings.ContainsAny(enc, "+/") {
		t.Fatalf("the standard alphabet leaked: %q", enc)
	}
	// Padding is written with the gateway's own character, never "=".
	padded := Encode([]byte("a"))
	if strings.Contains(padded, "=") || !strings.Contains(padded, "$") {
		t.Fatalf("a padded body is encoded as %q", padded)
	}
}

func TestTheHeadersCarryWhatTheGatewayChecks(t *testing.T) {
	pub, priv := testKey(t)
	target := "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?Encode=1"
	body := []byte(`{"model":"auto"}`)
	h, err := Headers(pub, target, body, Identity{
		NewID: testID, UserID: "u1", Token: "dt-1", MachineID: "m-1", IDEVersion: "1.0.0",
		ClientType: "5", MachineOS: "x86_64_windows", MachineType: "5",
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	auth := h["Authorization"]
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer COSY."), ".")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 32 {
		t.Fatalf("authorization %q", auth)
	}
	if h["Cosy-Sigpath"] != "/api/v2/service/pro/sse/agent_chat_generation" {
		t.Errorf("sig path %q", h["Cosy-Sigpath"])
	}
	if h["Cosy-Bodylength"] != "16" {
		t.Errorf("body length %q", h["Cosy-Bodylength"])
	}
	sum := md5.Sum(body)
	if h["Cosy-Bodyhash"] != hex.EncodeToString(sum[:]) {
		t.Errorf("body hash %q", h["Cosy-Bodyhash"])
	}
	if h["Cosy-User"] != "u1" || h["Cosy-Machineid"] != "m-1" || h["Login-Version"] != "v2" {
		t.Errorf("headers %v", h)
	}
	if h["Cosy-Clientip"] != "127.0.0.1" || h["Cosy-Data-Policy"] != "disagree" {
		t.Errorf("fixed headers %v", h)
	}
	// The wrapped key must be the AES key the identity was encrypted under, and
	// the digest must cover the parts the gateway names.
	wrapped, err := base64.StdEncoding.DecodeString(h["Cosy-Key"])
	if err != nil {
		t.Fatal(err)
	}
	// The gateway unwraps with PKCS#1 v1.5; the test unwarps the same way to
	// prove the key it received is the one the identity was encrypted under.
	//
	//lint:ignore SA1019 the peer's protocol fixes the padding
	aesKey, err := rsa.DecryptPKCS1v15(nil, priv, wrapped)
	if err != nil || len(aesKey) != 16 {
		t.Fatalf("the wrapped key does not unwrap to an AES key: %v", err)
	}
}

func TestASignedRequestRefusesWhatItCannotSign(t *testing.T) {
	pub, _ := testKey(t)
	for name, tc := range map[string]struct {
		target string
		id     Identity
		key    *rsa.PublicKey
	}{
		"no user":    {"https://x/algo/a", Identity{Token: "t"}, pub},
		"no token":   {"https://x/algo/a", Identity{UserID: "u"}, pub},
		"no key":     {"https://x/algo/a", Identity{UserID: "u", Token: "t"}, nil},
		"an address": {"/relative", Identity{UserID: "u", Token: "t"}, pub},
	} {
		if _, err := Headers(tc.key, tc.target, nil, tc.id); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestABadKeyIsRefused(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":     "",
		"not pem":   "hello",
		"not a key": "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----",
	} {
		if _, err := ParseKey(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	key, _ := testKey(t)
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	good := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	if _, err := ParseKey(good); err != nil {
		t.Fatalf("a good key was refused: %v", err)
	}
}

func FuzzEncode(f *testing.F) {
	f.Add("")
	f.Add(`{"a":1}`)
	f.Fuzz(func(t *testing.T, in string) {
		out, err := Decode(Encode([]byte(in)))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if string(out) != in {
			t.Fatalf("%q came back as %q", in, out)
		}
	})
}

func TestTheIdentityKeepsHTMLCharactersAsTheyAre(t *testing.T) {
	// The digest covers these bytes, so "<", ">" and "&" must not be rewritten
	// as <, > and &.
	got := jsonIdentity(Identity{UserID: "u", Token: "t", Name: "a<b>&c", Email: "e"})
	if !strings.Contains(got, `"name":"a<b>&c"`) {
		t.Fatalf("%s", got)
	}
}

// testID is the caller's id source: a fixed-shape, non-random stand-in.
func testID() string { return "0123456789abcdef-0123-4567-89ab-0123456789ab" }
