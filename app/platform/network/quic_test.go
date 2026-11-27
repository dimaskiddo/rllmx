package network

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func TestTheQUICGuardRefusesAPrivateAddressWithoutAListEntry(t *testing.T) {
	g := NewDialer(time.Second, nil)
	if _, err := quicTargets(context.Background(), g, "127.0.0.1:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err %v, want ErrBlocked", err)
	}
}

func TestTheQUICGuardRefusesAMetadataAddressEvenWhenListed(t *testing.T) {
	allow, err := ParseAllow("169.254.169.254")
	if err != nil {
		t.Fatal(err)
	}
	g := NewDialer(time.Second, allow)
	if _, err := quicTargets(context.Background(), g, "169.254.169.254:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err %v: the metadata address must be refused", err)
	}
}

func TestTheQUICGuardDialsTheCheckedLiteral(t *testing.T) {
	g := NewDialer(time.Second, nil)
	g.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
	}
	got, err := quicTargets(context.Background(), g, "provider.example:443")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IP.String() != "203.0.113.7" || got[0].Port != 443 {
		t.Fatalf("targets %v: the checked literal must be dialed", got)
	}
}

func TestTheQUICGuardRefusesAHostNameThatResolvesPrivate(t *testing.T) {
	g := NewDialer(time.Second, nil)
	g.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}
	if _, err := quicTargets(context.Background(), g, "idp.internal:443"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err %v, want ErrBlocked", err)
	}
}
