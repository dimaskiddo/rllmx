package settings

import (
	"strings"
	"testing"

	"rllmx/tests"
)

func metricsValues(token, user, pass string) tests.Config {
	return tests.Config{Values: map[string]any{
		"metrics.token": token, "metrics.username": user, "metrics.password": pass,
	}}
}

func TestMetricsIsOffUntilACredentialIsSet(t *testing.T) {
	m, err := LoadMetrics(metricsValues("", "", ""))
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled() {
		t.Fatalf("%+v", m)
	}
}

func TestMetricsRefusesAShortOrHalfPairCredential(t *testing.T) {
	short := strings.Repeat("a", 31)
	for name, cfg := range map[string]tests.Config{
		"short token":     metricsValues(short, "", ""),
		"short password":  metricsValues("", "prom", strings.Repeat("p", 15)),
		"no password":     metricsValues("", "prom", ""),
		"no username":     metricsValues("", "", strings.Repeat("p", 16)),
		"colon in user":   metricsValues("", "pr:om", strings.Repeat("p", 16)),
		"control in pass": metricsValues("", "prom", strings.Repeat("p", 15)+"\x01"),
	} {
		if _, err := LoadMetrics(cfg); err == nil || !strings.Contains(err.Error(), "METRICS_") {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestMetricsAcceptsEitherCredential(t *testing.T) {
	if m, err := LoadMetrics(metricsValues(strings.Repeat("t", 32), "", "")); err != nil || !m.Enabled() {
		t.Fatalf("%+v %v", m, err)
	}
	if m, err := LoadMetrics(metricsValues("", "prom", strings.Repeat("p", 16))); err != nil || !m.Enabled() {
		t.Fatalf("%+v %v", m, err)
	}
}
