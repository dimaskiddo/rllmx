package auth

import (
	"strings"
	"testing"
	"time"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

func TestSignAndVerifyRoundTrip(t *testing.T) {
	token, err := Sign(testSecret, Claims{Authenticated: true, Method: MethodPassword, PV: "abcdef0123456789"}, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	claims, ok := Verify(testSecret, token, time.Now())
	if !ok {
		t.Fatal("a token it just signed did not verify")
	}
	if !claims.Authenticated || claims.Method != MethodPassword || claims.PV != "abcdef0123456789" {
		t.Fatalf("%+v", claims)
	}
	if claims.Iss != Issuer || claims.Exp == 0 || claims.Iat == 0 {
		t.Fatalf("iss/exp/iat not set: %+v", claims)
	}
}

func TestTheTokenIsThreeUnpaddedBase64URLSegments(t *testing.T) {
	token, err := Sign(testSecret, Claims{Authenticated: true}, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q", token)
	}
	for _, p := range parts {
		if strings.ContainsAny(p, "=+/") {
			t.Errorf("segment %q is not unpadded base64url", p)
		}
	}
}

func TestVerifyRejectsWhatIsNotASession(t *testing.T) {
	good, err := Sign(testSecret, Claims{Authenticated: true}, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(good, ".")

	tampered := parts[0] + "." + parts[1] + "." + flipLast(parts[2])
	expired, _ := Sign(testSecret, Claims{}, SessionTTL)
	expiredParts := strings.Split(expired, ".")

	cases := []struct {
		name   string
		secret []byte
		token  string
		now    time.Time
	}{
		{"empty", testSecret, "", time.Now()},
		{"no secret", nil, good, time.Now()},
		{"two segments", testSecret, parts[0] + "." + parts[1], time.Now()},
		{"four segments", testSecret, good + ".x", time.Now()},
		{"not base64", testSecret, "!!!.???.***", time.Now()},
		{"wrong secret", []byte("another-secret-another-secret-xx"), good, time.Now()},
		{"tampered signature", testSecret, tampered, time.Now()},
		{"expired", testSecret, strings.Join(expiredParts, "."), time.Now().Add(SessionTTL + 2*Leeway)},
	}
	for _, tc := range cases {
		if _, ok := Verify(tc.secret, tc.token, tc.now); ok {
			t.Errorf("%s verified", tc.name)
		}
	}
}

func TestVerifyPinsTheMethod(t *testing.T) {
	// A token whose header says "none" must not be accepted, whatever the body.
	head := b64([]byte(`{"alg":"none","typ":"JWT"}`))
	body := b64([]byte(`{"authenticated":true,"iss":"rllm","exp":9999999999}`))
	if _, ok := Verify(testSecret, head+"."+body+"."+string(sig(testSecret, head+"."+body)), time.Now()); ok {
		t.Fatal("an alg=none token verified")
	}
}

func TestVerifyRequiresTheAuthenticatedClaimAndIssuer(t *testing.T) {
	for _, body := range []string{
		`{"iss":"rllm","exp":9999999999}`,
		`{"authenticated":true,"iss":"other","exp":9999999999}`,
		`{"authenticated":false,"iss":"rllm","exp":9999999999}`,
	} {
		head := b64([]byte(`{"alg":"HS256","typ":"JWT"}`))
		payload := b64([]byte(body))
		token := head + "." + payload + "." + string(sig(testSecret, head+"."+payload))
		if _, ok := Verify(testSecret, token, time.Now()); ok {
			t.Errorf("%s verified", body)
		}
	}
}

func TestLeewayAllowsASmallClockSkew(t *testing.T) {
	token, err := Sign(testSecret, Claims{Authenticated: true}, SessionTTL)
	if err != nil {
		t.Fatal(err)
	}
	claims, _ := Verify(testSecret, token, time.Now())
	at := time.Unix(claims.Exp, 0)
	if _, ok := Verify(testSecret, token, at.Add(Leeway/2)); !ok {
		t.Fatal("a client slightly ahead was refused inside the leeway")
	}
	if _, ok := Verify(testSecret, token, at.Add(Leeway*2)); ok {
		t.Fatal("a token past its expiry and the leeway verified")
	}
}

func flipLast(s string) string {
	if s == "" {
		return "x"
	}
	if s[len(s)-1] == 'A' {
		return s[:len(s)-1] + "B"
	}
	return s[:len(s)-1] + "A"
}
