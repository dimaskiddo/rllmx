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
}

func TestSettingsReadsTheStoredValuesAndTheEpoch(t *testing.T) {
	s, db := newSettings(t)
	ctx := context.Background()
	if err := db.Settings().Patch(ctx, map[string]string{"requireLogin": "false", "authMode": `"both"`}); err != nil {
		t.Fatal(err)
	}
	if err := db.KV().Set(ctx, ScopeSession, EpochKey, "7"); err != nil {
		t.Fatal(err)
	}
	got := s.Get(ctx)
	if got.RequireLogin || !got.RequireAPIKey || got.AuthMode != "both" || got.Epoch != "7" {
		t.Fatalf("%+v", got)
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
