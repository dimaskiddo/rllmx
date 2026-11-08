package http

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"net/url"
	"strconv"
	"time"

	"rllmx/app/network"
	"rllmx/app/settings"
)

const (
	healthPath   = "/api/health"
	probeTimeout = 3 * time.Second
	probeBodyCap = 4096
)

// HealthTarget is where the health check goes and how it verifies TLS.
type HealthTarget struct {
	URL string
	// TLS is set only when the server itself serves TLS; it always verifies.
	TLS *tls.Config
	// Allow permits the target's own address, which is loopback or a host the
	// operator configured. The guard would otherwise refuse it.
	Allow *network.Allow
}

// HealthTargetOf points at the local server named by the settings. A wildcard
// or empty host is dialled as loopback. With TLS the certificate is checked
// against the name in APP_URL when set, else against the host.
func HealthTargetOf(s *settings.Server) HealthTarget {
	host := s.Host
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::", "[::]":
		host = "::1"
	}
	t := HealthTarget{URL: "http://" + net.JoinHostPort(host, strconv.Itoa(s.Port)) + healthPath}
	// The target is this program's own listener, named in the server settings, so
	// its address is permitted by construction. The guard still refuses anything
	// a redirect would point at.
	t.Allow, _ = network.ParseAllow(host)
	if s.CertFile != "" {
		name := host
		if u, err := url.Parse(s.URL); err == nil && u.Hostname() != "" {
			name = u.Hostname()
		}
		t.URL = "https" + t.URL[len("http"):]
		t.TLS = &tls.Config{ServerName: name, MinVersion: tls.VersionTLS12}
	}
	return t
}

// probeClient is the shared guarded client with the probe's own knobs: a short
// timeout, at most three redirect hops, and a 4 KiB answer. It reads no proxy
// variable and verifies TLS, like every outbound call.
func probeClient(t HealthTarget) *nethttp.Client {
	allow := t.Allow
	if allow == nil {
		// The target comes from the operator's server settings, not from a
		// request, so its own host is permitted. A redirect is still checked
		// against this list, so the probe cannot be steered elsewhere.
		if u, err := url.Parse(t.URL); err == nil {
			allow, _ = network.ParseAllow(u.Hostname())
		}
	}
	return network.NewClient(network.Options{
		ConnectTimeout:    probeTimeout,
		MaxRedirects:      3,
		BodyCap:           probeBodyCap,
		Allow:             allow,
		TLS:               t.TLS,
		DisableKeepAlives: true,
	})
}

// Probe asks the health route and returns nil only for a 200 whose body says
// {"ok":true}. It waits at most three seconds, uses no proxy, follows at most
// three redirects inside the time it waits, and reads at most 4 KiB.
func Probe(ctx context.Context, t HealthTarget) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, t.URL, nil)
	if err != nil {
		return fmt.Errorf("http: health check: %w", err)
	}
	resp, err := probeClient(t).Do(req)
	if err != nil {
		return fmt.Errorf("http: health check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK {
		return fmt.Errorf("http: health check: status %d", resp.StatusCode)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, probeBodyCap)).Decode(&body); err != nil || !body.OK {
		return fmt.Errorf("http: health check: the answer is not {\"ok\":true}")
	}
	return nil
}
