package network

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

// resolver answers from a fixed map, so a test never depends on DNS.
func resolver(byName map[string][]string) func(context.Context, string, string) ([]netip.Addr, error) {
	return func(_ context.Context, _, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, raw := range byName[host] {
			out = append(out, netip.MustParseAddr(raw))
		}
		return out, nil
	}
}

func TestCheckPublicRefusesWhatIsNotClearlyPublic(t *testing.T) {
	listed, err := ParseAllow("10.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	lookup := resolver(map[string][]string{
		"example.com":   {"93.184.216.34"},
		"intranet.test": {"10.1.2.3"},
		"mixed.test":    {"93.184.216.34", "10.1.2.3"},
	})
	cases := []struct {
		name  string
		url   string
		allow *Allow
		block bool
		bad   bool
	}{
		{"a public literal", "https://93.184.216.34/x", nil, false, false},
		{"a public name", "https://example.com/a", nil, false, false},
		{"loopback", "http://127.0.0.1:8888/search", nil, true, false},
		{"a private name", "https://intranet.test/a", nil, true, false},
		{"one private answer refuses the name", "https://mixed.test/a", nil, true, false},
		{"the operator's list lifts an internal answer", "https://intranet.test/a", listed, false, false},
		{"metadata is refused even when listed", "http://169.254.169.254/latest/meta-data", listed, true, false},
		{"raw scheme", "ftp://example.com/a", nil, false, true},
		{"user info", "https://user:pw@example.com/a", nil, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkPublic(context.Background(), c.url, c.allow, lookup)
			if c.block && !errors.Is(err, ErrBlocked) {
				t.Fatalf("err %v, want ErrBlocked", err)
			}
			if c.bad && (err == nil || errors.Is(err, ErrBlocked)) {
				t.Fatalf("err %v, want a refusal of the address", err)
			}
			if !c.block && !c.bad && err != nil {
				t.Fatalf("err %v", err)
			}
		})
	}
}

func TestWithPermitsTheHostItAdds(t *testing.T) {
	lookup := resolver(map[string][]string{"search.internal": {"10.0.0.5"}})
	if err := checkPublic(context.Background(), "http://search.internal/search", nil, lookup); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err %v, want ErrBlocked", err)
	}
	allow := (*Allow)(nil).With("Search.Internal")
	if err := checkPublic(context.Background(), "http://search.internal/search", allow, lookup); err != nil {
		t.Fatalf("err %v", err)
	}
}
