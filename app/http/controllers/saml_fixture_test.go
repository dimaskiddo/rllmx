package controllers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// samlTestIDP is a signing identity provider: one key, its certificate, and the
// values a settings row would carry.
type samlTestIDP struct {
	key          *rsa.PrivateKey
	cert         *x509.Certificate
	certPEM      string
	entryPoint   string
	entityID     string
	signer       *dsig.SigningContext
	lastResponse string
}

func newSAMLTestIDP(t *testing.T) *samlTestIDP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rllmx saml test idp"},
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
	return &samlTestIDP{
		key: key, cert: cert,
		certPEM:    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		entryPoint: "https://idp.example.com/sso",
		entityID:   "https://idp.example.com/metadata",
		signer:     signer,
	}
}

// response builds a signed response that answers relay, addressed to baseURL.
func (p *samlTestIDP) response(t *testing.T, relay, baseURL string) string {
	t.Helper()
	now := time.Now()
	acs := baseURL + "/api/auth/saml/acs"
	assertion := fmt.Sprintf(`<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="assert-1" Version="2.0" IssueInstant="%s"><saml:Issuer>%s</saml:Issuer><saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">ops@example.com</saml:NameID><saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData InResponseTo="%s" Recipient="%s" NotOnOrAfter="%s"/></saml:SubjectConfirmation></saml:Subject><saml:Conditions NotBefore="%s" NotOnOrAfter="%s"><saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction></saml:Conditions><saml:AuthnStatement AuthnInstant="%s" SessionIndex="s-1"/><saml:AttributeStatement><saml:Attribute Name="mail"><saml:AttributeValue>ops@example.com</saml:AttributeValue></saml:Attribute><saml:Attribute Name="cn"><saml:AttributeValue>Ops</saml:AttributeValue></saml:Attribute></saml:AttributeStatement></saml:Assertion>`,
		now.Add(-time.Minute).UTC().Format(time.RFC3339), p.entityID, relay, acs,
		now.Add(5*time.Minute).UTC().Format(time.RFC3339),
		now.Add(-time.Minute).UTC().Format(time.RFC3339),
		now.Add(5*time.Minute).UTC().Format(time.RFC3339),
		baseURL+"/api/auth/saml/metadata", now.UTC().Format(time.RFC3339))

	// The assertion is signed as its own document, which is the shape an
	// identity provider produces when it signs assertions: inclusive C14N would
	// otherwise pull the parent's declarations into the digest.
	signedAssertion := p.signStandalone(t, assertion)
	xmlText := fmt.Sprintf(`<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="resp-1" Version="2.0" IssueInstant="%s" Destination="%s" InResponseTo="%s"><saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status>%s</samlp:Response>`,
		now.UTC().Format(time.RFC3339), acs, relay, p.entityID, signedAssertion)

	out := xmlText
	p.lastResponse = base64.StdEncoding.EncodeToString([]byte(out))
	return p.lastResponse
}

// signStandalone signs an assertion as its own document.
func (p *samlTestIDP) signStandalone(t *testing.T, assertionXML string) string {
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
	if i := strings.Index(out, "<saml:Assertion"); i > 0 {
		return out[i:]
	}
	return out
}
