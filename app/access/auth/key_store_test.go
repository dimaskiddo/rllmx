package auth

import (
	"context"
	"testing"

	"rllmx/app/access/identity"
	"rllmx/app/platform/cache"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

func newKeys(t *testing.T) (*Keys, *store.Store) {
	t.Helper()
	s := store.Migrated(t)
	db := func() *repository.Store { return s.Store }
	return NewKeys(db, cache.New(tests.NewCache(), cache.WithoutShared()), identity.NewKeys("")), s
}

func TestLookupAnswersNoneForNoKey(t *testing.T) {
	k, _ := newKeys(t)
	if got := k.Lookup(context.Background(), ""); got.Status != identity.KeyNone {
		t.Fatalf("%+v", got)
	}
}

func TestLookupRefusesAMalformedKeyBeforeAnyQuery(t *testing.T) {
	k, db := newKeys(t)
	// A database that is nil proves the query never ran.
	noDB := NewKeys(func() *repository.Store { return nil }, nil, identity.NewKeys(""))
	if got := noDB.Lookup(context.Background(), "not-a-key"); got.Status != identity.KeyInvalid {
		t.Fatalf("%+v", got)
	}
	if got := k.Lookup(context.Background(), "sk-has a space"); got.Status != identity.KeyInvalid {
		t.Fatalf("%+v", got)
	}
	if rows, _ := db.APIKeys().List(context.Background()); len(rows) != 0 {
		t.Fatal("the lookup wrote a row")
	}
}

func TestLookupFindsAnActiveKey(t *testing.T) {
	k, db := newKeys(t)
	ctx := context.Background()
	key, err := identity.NewKeys("").New("0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.APIKeys().Create(ctx, "ci", "0123456789abcdef", key)
	if err != nil {
		t.Fatal(err)
	}
	got := k.Lookup(ctx, key)
	if got.Status != identity.KeyValid || got.Caller.ID != row.ID || got.Caller.Name != "ci" {
		t.Fatalf("%+v", got)
	}
}

func TestLookupRefusesARevokedKey(t *testing.T) {
	k, db := newKeys(t)
	ctx := context.Background()
	key, _ := identity.NewKeys("").New("0123456789abcdef")
	row, err := db.APIKeys().Create(ctx, "ci", "0123456789abcdef", key)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := db.APIKeys().Update(ctx, row.ID, repository.APIKeyPatch{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	if got := k.Lookup(ctx, key); got.Status != identity.KeyInvalid {
		t.Fatalf("%+v", got)
	}
}

func TestARevokedKeyStopsWorkingAsSoonAsTheCacheIsDropped(t *testing.T) {
	k, db := newKeys(t)
	ctx := context.Background()
	key, _ := identity.NewKeys("").New("0123456789abcdef")
	row, err := db.APIKeys().Create(ctx, "ci", "0123456789abcdef", key)
	if err != nil {
		t.Fatal(err)
	}
	if got := k.Lookup(ctx, key); got.Status != identity.KeyValid {
		t.Fatal("the key did not validate")
	}
	off := false
	if _, err := db.APIKeys().Update(ctx, row.ID, repository.APIKeyPatch{IsActive: &off}); err != nil {
		t.Fatal(err)
	}
	if got := k.Lookup(ctx, key); got.Status != identity.KeyValid {
		t.Fatal("the cache answered a revoked key inside its TTL; that is what Changed is for")
	}
	k.Changed()
	if got := k.Lookup(ctx, key); got.Status != identity.KeyInvalid {
		t.Fatal("the revoked key still validates after Changed")
	}
}

func TestLookupCarriesTheAccessList(t *testing.T) {
	k, db := newKeys(t)
	ctx := context.Background()
	key, _ := identity.NewKeys("").New("0123456789abcdef")
	row, err := db.APIKeys().Create(ctx, "ci", "0123456789abcdef", key)
	if err != nil {
		t.Fatal(err)
	}
	access := repository.KeyAccess{Restricted: true, Allow: []string{"openai/gpt-5", "my-combo"}}
	if _, err := db.APIKeys().Update(ctx, row.ID, repository.APIKeyPatch{Access: &access}); err != nil {
		t.Fatal(err)
	}
	k.Changed()
	got := k.Lookup(ctx, key)
	if !got.Caller.Restricted || len(got.Caller.Allow) != 2 || got.Caller.Allow[0] != "openai/gpt-5" {
		t.Fatalf("%+v", got.Caller)
	}
}

func TestTheCacheKeyIsNotTheKey(t *testing.T) {
	key := "sk-0123456789abcdef-abc123-3b4c47b0"
	if got := cacheKey(key); got == key || len(got) != 64 {
		t.Fatalf("cache key %q", got)
	}
	var _ = cache.APIKeys
}
