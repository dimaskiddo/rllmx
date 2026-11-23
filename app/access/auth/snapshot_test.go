package auth

import (
	"context"
	"testing"
	"time"

	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

func newSettings(t *testing.T) (*Store, *store.Store) {
	t.Helper()
	s := store.Migrated(t)
	return NewStore(func() *repository.Store { return s.Store }), s
}

func TestSettingsDefaultsWhileNoStoreIsOpen(t *testing.T) {
	s := NewStore(func() *repository.Store { return nil })
	got := s.Get(context.Background())
	want := DefaultSettings()
	if got != want {
		t.Fatalf("%+v, want %+v", got, want)
	}
	if !got.RequireLogin || !got.RequireAPIKey {
		t.Fatal("an unreadable store must require login and key")
	}
	// The sign-in page draws its single sign-on buttons before it can read the
	// settings row, so the fallback must carry usable labels.
	if got.OIDCLoginLabel != "Sign in with OIDC" || got.SAMLLoginLabel != "Sign in with SAML SSO" {
		t.Fatalf("fallback labels %q, %q", got.OIDCLoginLabel, got.SAMLLoginLabel)
	}
}

// An operator who clears a label wants the page's own words, translated; the
// snapshot must hand the empty value on rather than put English back.
func TestAClearedLoginLabelReachesThePageEmpty(t *testing.T) {
	s, db := newSettings(t)
	if err := db.Settings().Patch(context.Background(), map[string]string{
		"oidcLoginLabel": `""`, "samlLoginLabel": `""`,
	}); err != nil {
		t.Fatal(err)
	}
	got := s.Get(context.Background())
	if got.OIDCLoginLabel != "" || got.SAMLLoginLabel != "" {
		t.Fatalf("labels %q, %q: an empty label was replaced", got.OIDCLoginLabel, got.SAMLLoginLabel)
	}
}

func TestSettingsReadsTheStoredValuesAndTheEpoch(t *testing.T) {
	s, db := newSettings(t)
	ctx := context.Background()
	if err := db.Settings().Patch(ctx, map[string]string{
		"requireLogin": "false", "authMode": `"both"`,
		"oidcLoginLabel": `"Company SSO"`, "samlLoginLabel": `"SAML"`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.KV().Set(ctx, ScopeSession, EpochKey, "7"); err != nil {
		t.Fatal(err)
	}
	got := s.Get(ctx)
	if got.RequireLogin || !got.RequireAPIKey || got.AuthMode != "both" || got.Epoch != "7" {
		t.Fatalf("%+v", got)
	}
	if got.OIDCLoginLabel != "Company SSO" || got.SAMLLoginLabel != "SAML" {
		t.Fatalf("labels %q, %q", got.OIDCLoginLabel, got.SAMLLoginLabel)
	}
}

func TestTheSnapshotIsCachedUntilChanged(t *testing.T) {
	s, db := newSettings(t)
	ctx := context.Background()
	s.Get(ctx)

	if err := db.Settings().Patch(ctx, map[string]string{"requireLogin": "false"}); err != nil {
		t.Fatal(err)
	}
	if !s.Get(ctx).RequireLogin {
		t.Fatal("the snapshot was re-read inside its TTL")
	}
	s.Changed()
	if s.Get(ctx).RequireLogin {
		t.Fatal("Changed did not drop the snapshot")
	}
}

func TestTheSnapshotExpires(t *testing.T) {
	s, db := newSettings(t)
	ctx := context.Background()
	now := time.Now()
	s.Now = func() time.Time { return now }
	s.Get(ctx)

	if err := db.Settings().Patch(ctx, map[string]string{"requireLogin": "false"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(snapshotTTL + time.Second)
	if s.Get(ctx).RequireLogin {
		t.Fatal("the snapshot outlived its TTL")
	}
}

func TestBumpEpochEndsEverySession(t *testing.T) {
	s, _ := newSettings(t)
	ctx := context.Background()
	before := s.Get(ctx).Epoch
	if before != "0" {
		t.Fatalf("a fresh database starts at epoch %q", before)
	}
	pvBefore := PV(testSecret, "", before)

	next, err := s.BumpEpoch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next == before {
		t.Fatal("the epoch did not move")
	}
	if SamePV(pvBefore, PV(testSecret, "", s.Get(ctx).Epoch)) {
		t.Fatal("the pv survived the bump, so old sessions still validate")
	}
}
