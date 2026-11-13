package settings

import (
	"crypto/tls"
	"strings"
	"testing"

	"rllmx/tests"
)

func upstreamValues(curve string) tests.Config {
	return tests.Config{Values: map[string]any{
		"upstream.connect_timeout": "60s", "upstream.allow_private": "", "upstream.tls_curve": curve,
		"upstream.claude_cli_version": "",
	}}
}

func TestTheTLScurveIsX25519UnlessTheStandardListIsAskedFor(t *testing.T) {
	u, err := LoadUpstream(upstreamValues("x25519"))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Curves) != 1 || u.Curves[0] != tls.X25519 {
		t.Fatalf("x25519 gave %v", u.Curves)
	}
	u, err = LoadUpstream(upstreamValues("default"))
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Curves) != 0 {
		t.Fatalf("default gave %v, want the standard list", u.Curves)
	}
	if _, err := LoadUpstream(upstreamValues("secp256r1")); err == nil || !strings.Contains(err.Error(), "UPSTREAM_TLS_CURVE") {
		t.Fatalf("a curve this router does not offer was accepted: %v", err)
	}
}

func TestTheClaudeCLIVersionPin(t *testing.T) {
	values := func(v string) tests.Config {
		return tests.Config{Values: map[string]any{
			"upstream.connect_timeout": "60s", "upstream.allow_private": "", "upstream.tls_curve": "",
			"upstream.claude_cli_version": v,
		}}
	}
	if u, err := LoadUpstream(values("2.1.400")); err != nil || u.ClaudeCLIVersion != "2.1.400" {
		t.Fatalf("%+v %v", u, err)
	}
	for _, bad := range []string{"2.1", "2.1.4.0", "2.1.x", "v2.1.4", "2..4", "2.1.1234567"} {
		if _, err := LoadUpstream(values(bad)); err == nil || !strings.Contains(err.Error(), "UPSTREAM_CLAUDE_CLI_VERSION") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}
