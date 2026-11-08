package saml

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestAuthnRequestIsAnUnsignedRedirect(t *testing.T) {
	target, err := AuthnRequest("https://idp.example.com/sso", "https://gw.example.com/api/auth/saml/metadata",
		"https://gw.example.com/api/auth/saml/acs", "rllmx-1", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "idp.example.com" {
		t.Fatalf("host %q", u.Host)
	}
	state := u.Query().Get("RelayState")
	if state != "rllmx-1" {
		t.Fatalf("relay state %q", state)
	}

	raw, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLRequest"))
	if err != nil {
		t.Fatal(err)
	}
	inflated, err := io.ReadAll(flate.NewReader(bytes.NewReader(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"urn:oasis:names:tc:SAML:2.0:protocol",
		`ID="rllmx-1"`,
		"https://idp.example.com/sso",
		"https://gw.example.com/api/auth/saml/acs",
		"urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST",
	} {
		if !strings.Contains(string(inflated), want) {
			t.Errorf("%q is missing from %s", want, inflated)
		}
	}
	if strings.Contains(string(inflated), "Signature") {
		t.Fatal("the request carries a signature; signed requests are not built")
	}
}

func TestAuthnRequestKeepsAnExistingQuery(t *testing.T) {
	target, err := AuthnRequest("https://idp.example.com/sso?tenant=a", "e", "https://gw/acs", "rllmx-1", "now")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(target, "tenant=a") || !strings.Contains(target, "SAMLRequest=") {
		t.Fatalf("%q", target)
	}
}

func TestAuthnRequestRefusesMissingParts(t *testing.T) {
	for _, tc := range []struct{ entry, entity, acs, id string }{
		{"", "e", "a", "i"},
		{"https://idp.example.com/sso", "", "a", "i"},
		{"https://idp.example.com/sso", "e", "", "i"},
		{"https://idp.example.com/sso", "e", "a", ""},
	} {
		if _, err := AuthnRequest(tc.entry, tc.entity, tc.acs, tc.id, "now"); err == nil {
			t.Errorf("%+v accepted", tc)
		}
	}
}

func TestNewRequestIDIsAnNCName(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		id, err := newRequestID()
		if err != nil {
			t.Fatal(err)
		}
		if id[0] < 'a' || id[0] > 'z' {
			t.Fatalf("%q does not start with a letter", id)
		}
		for _, c := range id {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				t.Fatalf("%q holds %q", id, c)
			}
		}
		if seen[id] {
			t.Fatalf("a request id repeated: %q", id)
		}
		seen[id] = true
	}
}

func TestMetadataDeclaresOnePostACS(t *testing.T) {
	raw, err := Metadata("https://gw.example.com/api/auth/saml/metadata", "https://gw.example.com/api/auth/saml/acs")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		XMLName  xml.Name `xml:"EntityDescriptor"`
		EntityID string   `xml:"entityID,attr"`
		SP       struct {
			WantSigned string `xml:"WantAssertionsSigned,attr"`
			Signed     string `xml:"AuthnRequestsSigned,attr"`
			ACS        []struct {
				Binding  string `xml:"Binding,attr"`
				Location string `xml:"Location,attr"`
			} `xml:"AssertionConsumerService"`
		} `xml:"SPSSODescriptor"`
	}
	if err := xml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	if doc.EntityID != "https://gw.example.com/api/auth/saml/metadata" {
		t.Fatalf("entity id %q", doc.EntityID)
	}
	if doc.SP.WantSigned != "true" || doc.SP.Signed != "false" {
		t.Fatalf("want signed %q, requests signed %q", doc.SP.WantSigned, doc.SP.Signed)
	}
	if len(doc.SP.ACS) != 1 || doc.SP.ACS[0].Binding != "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" {
		t.Fatalf("acs %+v", doc.SP.ACS)
	}
	if doc.SP.ACS[0].Location != "https://gw.example.com/api/auth/saml/acs" {
		t.Fatalf("location %q", doc.SP.ACS[0].Location)
	}
}

func TestMetadataRefusesMissingParts(t *testing.T) {
	if _, err := Metadata("", "https://gw/acs"); err == nil {
		t.Fatal("an empty entity id was accepted")
	}
	if _, err := Metadata("e", ""); err == nil {
		t.Fatal("an empty ACS was accepted")
	}
}

func TestReplayKeepsAnIDUntilTheAssertionExpires(t *testing.T) {
	now := time.Now()
	r := NewReplay()
	if !r.Take("a", now.Add(time.Minute), now) {
		t.Fatal("the first use was refused")
	}
	if r.Take("a", now.Add(time.Minute), now) {
		t.Fatal("the second use was accepted")
	}
	if !r.Take("a", now.Add(time.Minute), now.Add(2*time.Minute)) {
		t.Fatal("an expired id stayed in the set")
	}
}

func TestAnAssertionCannotOutliveTheReplaySet(t *testing.T) {
	// A provider asking for a 72-hour window would outlive the replay entry, so
	// the window is refused rather than silently leaving a replay gap.
	p := newTestIDP(t)
	exp := p.expectFor(NewReplay())
	m := defaultMinted(exp)
	m.notBefore = time.Now().Add(-time.Minute)
	m.notAfter = time.Now().Add(72 * time.Hour)
	if _, err := Verify(p.build(t, m), exp); !errors.Is(err, ErrExpired) {
		t.Fatalf("err %v", err)
	}
}

func TestReplayRefusesPastItsCap(t *testing.T) {
	now := time.Now()
	r := NewReplay()
	for i := range maxReplay {
		r.Take(strings.Repeat("x", 8)+string(rune('a'+i%26))+itoa(i), now.Add(time.Hour), now)
	}
	if r.Take("a-new-id", now.Add(time.Hour), now) {
		t.Fatal("the set accepted an entry past its cap")
	}
}

// itoa avoids importing strconv for one call.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
