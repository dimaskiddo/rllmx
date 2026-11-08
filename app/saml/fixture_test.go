package saml

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// The test identity provider: one RSA key, its certificate, and a signing
// context built the way a real provider builds one.
type testIDP struct {
	key    *rsa.PrivateKey
	cert   *x509.Certificate
	signer *dsig.SigningContext
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rllmx test idp"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := dsig.NewSigningContext(key, [][]byte{der})
	if err != nil {
		t.Fatal(err)
	}
	if err := signer.SetSignatureMethod(dsig.RSASHA256SignatureMethod); err != nil {
		t.Fatal(err)
	}
	return &testIDP{key: key, cert: cert, signer: signer}
}

// minted is what a test builds before signing.
type minted struct {
	responseID   string
	assertionID  string
	issuer       string
	inResponseTo string
	destination  string
	audience     string
	notBefore    time.Time
	notAfter     time.Time
	email        string
	name         string
	// extraAssertions adds unsigned siblings, for the wrapping test.
	extraAssertions []string
	// advice wraps the assertion inside Advice, the wrapping attack's hiding
	// place.
	advice bool
	// signAssertionOnly signs the Assertion rather than the Response.
	signAssertionOnly bool
	// confirmationMethod overrides the subject-confirmation method, so a test can
	// present a holder-of-key assertion.
	confirmationMethod string
	// unsigned leaves the document with no signature at all.
	unsigned bool
}

func defaultMinted(exp Expect) minted {
	now := time.Now()
	return minted{
		responseID:   "resp-1",
		assertionID:  "assert-1",
		issuer:       exp.Issuer,
		inResponseTo: exp.RequestID,
		destination:  exp.Destination,
		audience:     exp.Audience,
		notBefore:    now.Add(-time.Minute),
		notAfter:     now.Add(5 * time.Minute),
		email:        "ops@example.com",
		name:         "Ops",
	}
}

// build renders the document, signs it and returns the base64 form a POST body
// would carry.
func (p *testIDP) build(t *testing.T, m minted) string {
	t.Helper()
	xmlText := m.render(t, p)
	if m.unsigned {
		return base64.StdEncoding.EncodeToString([]byte(xmlText))
	}

	doc := etree.NewDocument()
	if err := doc.ReadFromString(xmlText); err != nil {
		t.Fatal(err)
	}
	target := doc.Root()
	if m.signAssertionOnly || m.advice {
		assertionEl := doc.Root().SelectElement("Assertion")
		if assertionEl == nil {
			t.Fatal("no assertion to sign")
		}
		target = assertionEl
	}
	signed, err := p.signer.SignEnveloped(target)
	if err != nil {
		t.Fatal(err)
	}
	target.Child = signed.Child

	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(out))
}

// signStandalone signs an assertion as its own document, which is the shape an
// identity provider produces when it signs assertions rather than responses.
func (p *testIDP) signStandalone(t *testing.T, assertionXML string) string {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(assertionXML); err != nil {
		t.Fatal(err)
	}
	signed, err := p.signer.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatal(err)
	}
	doc.Root().Child = signed.Child
	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return stripHeader(out)
}

// stripHeader drops the XML declaration, so the fragment embeds in the response.
func stripHeader(s string) string {
	if i := indexOf(s, "<saml:Assertion"); i > 0 {
		return s[i:]
	}
	return s
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// render produces the XML text of the response, signing the assertion on its own
// when the fixture asks for an assertion-level signature: an assertion signed
// while attached to a parent that declares other prefixes canonicalises
// differently once detached, which is a property of inclusive C14N and not of the
// verifier.
func (m minted) render(t *testing.T, p *testIDP) string {
	t.Helper()
	instant := m.notBefore.UTC().Format(time.RFC3339)
	assertion := m.assertionXML()

	if m.advice {
		// The real attack: the signed assertion is hidden inside Advice and a
		// forged one is presented as the sibling every consumer reads.
		forged := m.forgedAssertionXML()
		assertion = p.signStandalone(t, assertion)
		return fmt.Sprintf(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" Version="2.0" IssueInstant="%s" Destination="%s" InResponseTo="%s"><saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="%s"/></samlp:Status><saml:Advice>%s</saml:Advice>%s</samlp:Response>`,
			m.responseID, instant, m.destination, m.inResponseTo, m.issuer, statusSuccess, assertion, forged)
	}

	extras := ""
	for _, raw := range m.extraAssertions {
		extras += raw
	}
	if m.signAssertionOnly {
		assertion = p.signStandalone(t, assertion)
	}
	return fmt.Sprintf(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" Version="2.0" IssueInstant="%s" Destination="%s" InResponseTo="%s"><saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="%s"/></samlp:Status>%s%s</samlp:Response>`,
		m.responseID, instant, m.destination, m.inResponseTo, m.issuer, statusSuccess, assertion, extras)
}

// assertionXML is the honest assertion.
func (m minted) assertionXML() string {
	method := m.confirmationMethod
	if method == "" {
		method = "urn:oasis:names:tc:SAML:2.0:cm:bearer"
	}
	return fmt.Sprintf(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" Version="2.0" IssueInstant="%s"><saml:Issuer>%s</saml:Issuer><saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">%s</saml:NameID><saml:SubjectConfirmation Method="`+method+`"><saml:SubjectConfirmationData InResponseTo="%s" Recipient="%s" NotOnOrAfter="%s"/></saml:SubjectConfirmation></saml:Subject><saml:Conditions NotBefore="%s" NotOnOrAfter="%s"><saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction></saml:Conditions><saml:AuthnStatement AuthnInstant="%s" SessionIndex="s-1"/><saml:AttributeStatement><saml:Attribute Name="email"><saml:AttributeValue>%s</saml:AttributeValue></saml:Attribute><saml:Attribute Name="name"><saml:AttributeValue>%s</saml:AttributeValue></saml:Attribute></saml:AttributeStatement></saml:Assertion>`,
		m.assertionID, m.issuedAt(), m.issuer, m.email, m.inResponseTo, m.destination,
		m.notAfter.UTC().Format(time.RFC3339), m.notBefore.UTC().Format(time.RFC3339),
		m.notAfter.UTC().Format(time.RFC3339), m.audience, m.issuedAt(), m.email, m.name)
}

// forgedAssertionXML is what a wrapping attacker wants read instead.
func (m minted) forgedAssertionXML() string {
	forged := m
	forged.assertionID = "assert-forged"
	forged.email = "attacker@evil.example"
	return forged.assertionXML()
}

func (m minted) issuedAt() string { return m.notBefore.UTC().Format(time.RFC3339) }

// expectFor builds the Expect a route would pass for this response.
func (p *testIDP) expectFor(replay *Replay) Expect {
	return Expect{
		Issuer:      "https://idp.example.com",
		Audience:    "https://gw.example.com/api/auth/saml/metadata",
		Destination: "https://gw.example.com/api/auth/saml/acs",
		RequestID:   "req-1",
		Cert:        p.cert,
		Replay:      replay,
		Now:         time.Now(),
	}
}

// buildSignedResponse signs the Response element rather than the Assertion.
func (p *testIDP) buildSignedResponse(t *testing.T, m minted) string {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(m.render(t, p)); err != nil {
		t.Fatal(err)
	}
	signed, err := p.signer.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatal(err)
	}
	doc.Root().Child = signed.Child
	out, err := doc.WriteToString()
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString([]byte(out))
}

// encodeBody base64-encodes a raw XML body the way a POST field carries it.
func encodeBody(raw string) string {
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// rawIndex is strings.Index without importing strings into every test.
func rawIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return 0
}

// ReplayAt builds a replay set reading the given clock.
func ReplayAt(now time.Time) *Replay {
	r := NewReplay()
	r.Now = func() time.Time { return now }
	return r
}

// contains reports a substring without importing strings.
func contains(s, sub string) bool {
	return rawIndex(s, sub) > 0 || (len(sub) > 0 && len(s) >= len(sub) && s[:len(sub)] == sub)
}
