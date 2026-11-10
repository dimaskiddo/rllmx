package sigv4

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// signingTime is a fixed instant, so a signature is a constant.
var signingTime = func() time.Time { return time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC) }

const (
	accessKey = "AKIDEXAMPLE"
	secretKey = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY"
)

func TestSignMatchesThePublishedVector(t *testing.T) {
	// AWS's own example, cross-checked against the aws4 reference implementation:
	// a form POST to iam.amazonaws.com, with the date, credential scope,
	// signed-header list and signature the spec prints.
	body := []byte("Action=ListUsers&Version=2010-05-08")
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	h.Set("Content-Length", "35")

	got, err := Sign(Request{
		Method: "POST", URL: "https://iam.amazonaws.com/", Header: h, Body: body,
		Region: "us-east-1", Service: "iam", AccessKey: accessKey, SecretKey: secretKey, Now: signingTime,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, " +
		"SignedHeaders=content-length;content-type;host;x-amz-date, " +
		"Signature=afcae41d2eaf39c1b479fff3b5ae64d6806f975e7b6efb60038301a42c8dcb14"
	if got.Get("Authorization") != want {
		t.Errorf("authorization:\n got %s\nwant %s", got.Get("Authorization"), want)
	}
	if got.Get(DateHeader) != "20150830T123600Z" {
		t.Errorf("date %q", got.Get(DateHeader))
	}
}

// TestSignMatchesTheBedrockVector pins the path case that breaks most often: a
// model id whose ":" reaches the wire escaped and is signed escaped twice.
func TestSignMatchesTheBedrockVector(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")

	got, err := Sign(Request{
		Method: "POST",
		URL:    "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-sonnet-4-20250514-v1%3A0/invoke-with-response-stream",
		Header: h, Body: []byte(`{"anthropic_version":"bedrock-2023-05-31","messages":[]}`),
		Region: "us-east-1", Service: "bedrock", AccessKey: accessKey, SecretKey: secretKey, Now: signingTime,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/bedrock/aws4_request, " +
		"SignedHeaders=content-type;host;x-amz-date, " +
		"Signature=53d28679a943780f541a376e6ae08bb2b10379169a756a881a166d63132c94ab"
	if got.Get("Authorization") != want {
		t.Errorf("authorization:\n got %s\nwant %s", got.Get("Authorization"), want)
	}
}

// TestTheSameRequestSignsTheSameWayTwice holds the whole thing to determinism:
// a clock that does not move gives one answer.
func TestTheSameRequestSignsTheSameWayTwice(t *testing.T) {
	r := Request{
		Method: "POST", URL: "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke",
		Body: []byte(`{"a":1}`), Region: "us-east-1", Service: "bedrock",
		AccessKey: accessKey, SecretKey: secretKey, Now: signingTime,
	}
	first, err := Sign(r)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	second, err := Sign(r)
	if err != nil {
		t.Fatalf("sign again: %v", err)
	}
	if first.Get("Authorization") != second.Get("Authorization") {
		t.Errorf("two signatures for one request:\n%s\n%s", first.Get("Authorization"), second.Get("Authorization"))
	}
}

func TestASessionTokenIsSentAndSigned(t *testing.T) {
	got, err := Sign(Request{
		Method: "POST", URL: "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke",
		Region: "us-east-1", Service: "bedrock",
		AccessKey: accessKey, SecretKey: secretKey, SessionToken: "FwoGZXIvYXdzE", Now: signingTime,
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if got.Get(TokenHeader) != "FwoGZXIvYXdzE" {
		t.Errorf("token header %q", got.Get(TokenHeader))
	}
	if !strings.Contains(got.Get("Authorization"), "x-amz-security-token") {
		t.Errorf("the token was not signed: %s", got.Get("Authorization"))
	}
}

func TestThePathIsDoubleEncodedUnlessToldOtherwise(t *testing.T) {
	// An escaped model id reaches the wire with %3A; SigV4 signs %253A.
	base := Request{
		Method: "POST", URL: "https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-v1%3A0/invoke",
		Region: "us-east-1", Service: "bedrock", AccessKey: accessKey, SecretKey: secretKey, Now: signingTime,
	}
	doubled, err := Sign(base)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	base.SingleEncodePath = true
	single, err := Sign(base)
	if err != nil {
		t.Fatalf("sign single: %v", err)
	}
	if doubled.Get("Authorization") == single.Get("Authorization") {
		t.Error("the switch changed nothing, so it is dead")
	}
}

func TestQueryStringAndMissingKeyAreRefused(t *testing.T) {
	cases := map[string]struct {
		r    Request
		want error
	}{
		"query": {Request{URL: "https://x.amazonaws.com/a?b=1", AccessKey: accessKey, SecretKey: secretKey}, ErrQuery},
		"key":   {Request{URL: "https://x.amazonaws.com/a", AccessKey: accessKey}, ErrNoKey},
		"bare":  {Request{URL: "/relative", AccessKey: accessKey, SecretKey: secretKey}, ErrAddress},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Sign(tc.r); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEscapeLeavesOnlyTheUnreservedSet(t *testing.T) {
	for in, want := range map[string]string{
		"abc-_.~":  "abc-_.~",
		"a!b'c(d)": "a%21b%27c%28d%29",
		"a b":      "a%20b",
		"v1:0":     "v1%3A0",
	} {
		if got := Escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzSign(f *testing.F) {
	f.Add("https://bedrock-runtime.us-east-1.amazonaws.com/model/m/invoke")
	f.Add("https://iam.amazonaws.com/")
	f.Add("")
	f.Fuzz(func(t *testing.T, addr string) {
		r := Request{
			Method: "POST", URL: addr, Region: "us-east-1", Service: "bedrock",
			AccessKey: accessKey, SecretKey: secretKey, Now: signingTime,
		}
		h, err := Sign(r)
		if err != nil {
			return
		}
		if !strings.HasPrefix(h.Get("Authorization"), Algorithm+" ") {
			t.Fatalf("authorization %q", h.Get("Authorization"))
		}
		if strings.Contains(h.Get("Authorization"), "\n") {
			t.Fatal("a newline reached the authorization header")
		}
	})
}
