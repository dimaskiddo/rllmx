package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"testing"

	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

func newUsers(t *testing.T) (*UserRepo, *store.Store) {
	t.Helper()
	s := store.Migrated(t)
	db := func() *repository.Store { return s.Store }
	return NewUserRepo(db, 0), s
}

func addUser(t *testing.T, r *UserRepo, id, username, email string) User {
	t.Helper()
	u := User{ID: id, Username: username, Email: email, Hash: "hash-" + id}
	if err := r.Put(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAUserRoundTripsThroughItsTable(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	addUser(t, r, "u1", "Dimas", "dimas@example.com")

	got, ok, err := r.Get(ctx, "u1")
	if err != nil || !ok {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	if got.Username != "Dimas" || got.Email != "dimas@example.com" || got.Hash != "hash-u1" || got.CreatedAt == "" {
		t.Fatalf("%+v", got)
	}
}

func TestTheAdminNameIsReserved(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	for _, name := range []string{"admin", "Admin"} {
		if err := r.Put(ctx, User{ID: "u" + name, Username: name, Hash: "h"}); !errors.Is(err, ErrUserTaken) {
			t.Fatalf("%s: %v, want ErrUserTaken", name, err)
		}
	}
	r.Admin = "Operator"
	if err := r.Put(ctx, User{ID: "u2", Username: "admin", Hash: "h"}); err != nil {
		t.Fatalf("the configured name left the default free: %v", err)
	}
	if err := r.Put(ctx, User{ID: "u3", Username: "operator", Hash: "h"}); !errors.Is(err, ErrUserTaken) {
		t.Fatalf("the configured admin name was taken: %v", err)
	}
}

func TestAUsernameAndAnEmailAreUnique(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	addUser(t, r, "u1", "Dimas", "dimas@example.com")
	for _, u := range []User{
		{ID: "u2", Username: "dimas", Hash: "h"},
		{ID: "u3", Username: "Other", Email: "DIMAS@example.com", Hash: "h"},
	} {
		if err := r.Put(ctx, u); !errors.Is(err, ErrUserTaken) {
			t.Fatalf("%s: %v, want ErrUserTaken", u.Username, err)
		}
	}
	// The same user may be updated in place, and two users may have no address.
	u, _, _ := r.Get(ctx, "u1")
	u.Username = "Dimas R"
	if err := r.Put(ctx, u); err != nil {
		t.Fatalf("updating a user was refused: %v", err)
	}
	addUser(t, r, "u2", "First", "")
	addUser(t, r, "u3", "Second", "")
}

func TestABadUserIsRefused(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	cases := []struct {
		u    User
		want error
	}{
		{User{ID: "u1", Username: "", Hash: "h"}, ErrUserName},
		{User{ID: "u1", Username: "a/../b", Hash: "h"}, ErrUserName},
		{User{ID: "u1", Username: "a\nb", Hash: "h"}, ErrUserName},
		{User{ID: "u1", Username: "Dimas", Email: "two@at@example.com", Hash: "h"}, ErrUserEmail},
		{User{ID: "u1", Username: "Dimas", Email: "a b@example.com", Hash: "h"}, ErrUserEmail},
		{User{ID: "", Username: "Dimas", Hash: "h"}, ErrUserHash},
		{User{ID: "u1", Username: "Dimas"}, ErrUserHash},
	}
	for _, c := range cases {
		if err := r.Put(ctx, c.u); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.u, err, c.want)
		}
	}
	if list, _ := r.List(ctx); len(list) != 0 {
		t.Fatalf("%d users were stored by a refused write", len(list))
	}
}

func TestTheUserListIsCapped(t *testing.T) {
	r, db := newUsers(t)
	ctx := context.Background()
	r.limit = 2
	for i := range 2 {
		row := repository.UserRow{ID: fmt.Sprintf("seed%d", i), Username: fmt.Sprintf("Seed %d", i), Password: "h", CreatedAt: "2026-11-23T00:00:00Z"}
		if err := db.Store.Users().Create(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Put(ctx, User{ID: "new", Username: "New", Hash: "h"}); !errors.Is(err, ErrUserFull) {
		t.Fatalf("%v, want ErrUserFull", err)
	}
}

func TestAProvisionedUserKeepsItsFlag(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	u := User{ID: "u1", Username: "dimas@example.com", Email: "dimas@example.com", Hash: "random", Provisioned: true}
	if err := r.Put(ctx, u); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Get(ctx, "u1")
	if !got.Provisioned {
		t.Fatal("the provisioned flag was lost")
	}
	got.Provisioned = false
	if err := r.Put(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := r.Get(ctx, "u1"); again.Provisioned {
		t.Fatal("a cleared flag came back")
	}
}

func TestAKeyAssignmentIsCountedAndCleared(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	addUser(t, r, "u1", "Dimas", "")
	addUser(t, r, "u2", "Other", "")
	for _, key := range []string{"k2", "k1"} {
		if err := r.SetOwner(ctx, key, "u1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetOwner(ctx, "k3", "u2"); err != nil {
		t.Fatal(err)
	}
	if owners, _ := r.Owners(ctx); owners["k1"] != "u1" {
		t.Fatalf("owner %q", owners["k1"])
	}
	keys, err := r.KeysOf(ctx, "u1")
	if err != nil || len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Fatalf("%v %v", keys, err)
	}
	if err := r.SetOwner(ctx, "k1", ""); err != nil {
		t.Fatal(err)
	}
	if owners, _ := r.Owners(ctx); owners["k1"] != "" {
		t.Fatalf("a cleared assignment still names %q", owners["k1"])
	}
	if err := r.SetOwner(ctx, "k9", "nobody"); !errors.Is(err, ErrUserMissing) {
		t.Fatalf("%v, want ErrUserMissing", err)
	}
}

func TestDeletingAUserKeepsTheKeys(t *testing.T) {
	r, db := newUsers(t)
	ctx := context.Background()
	addUser(t, r, "u1", "Dimas", "")
	key, err := db.Store.APIKeys().Create(ctx, "laptop", "", "sk-live-aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetOwner(ctx, key.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.Get(ctx, "u1"); ok {
		t.Fatal("the user survived the delete")
	}
	if owners, _ := r.Owners(ctx); owners[key.ID] != "" {
		t.Fatalf("the key still belongs to %q", owners[key.ID])
	}
	if _, found, _ := db.Store.APIKeys().GetByID(ctx, key.ID); !found {
		t.Fatal("deleting a user deleted their key")
	}
	if err := r.Delete(ctx, "u1"); !errors.Is(err, ErrUserMissing) {
		t.Fatalf("%v, want ErrUserMissing", err)
	}
}

func TestTheUserSnapshotFollowsAWrite(t *testing.T) {
	r, _ := newUsers(t)
	users := NewUsers(r)
	ctx := context.Background()
	if len(users.All(ctx)) != 0 {
		t.Fatal("an empty table reports a user")
	}
	addUser(t, r, "u1", "Dimas", "dimas@example.com")
	users.Changed()
	if len(users.All(ctx)) != 1 {
		t.Fatal("the cache did not see the new user")
	}
	if u, ok := users.ByUsername(ctx, "DIMAS"); !ok || u.ID != "u1" {
		t.Fatalf("%+v %v", u, ok)
	}
	if u, ok := users.ByEmail(ctx, "dimas@example.com"); !ok || u.ID != "u1" {
		t.Fatalf("%+v %v", u, ok)
	}
	if _, ok := users.Get(ctx, "nobody"); ok {
		t.Fatal("an unknown id was found")
	}
	var none *Users
	if none.All(ctx) != nil {
		t.Fatal("a nil user list answered")
	}
}

func TestNewUserIDIsSixLowerCaseCharacters(t *testing.T) {
	shape := regexp.MustCompile(`^[a-z0-9]{6}$`)
	seen := map[string]bool{}
	for range 32 {
		id, err := NewUserID()
		if err != nil {
			t.Fatal(err)
		}
		if !shape.MatchString(id) {
			t.Fatalf("%q is not six lower-case characters", id)
		}
		if seen[id] {
			t.Fatalf("%q was drawn twice", id)
		}
		seen[id] = true
	}
}

func TestNewUserPasswordIsAStrongURLSafeSecret(t *testing.T) {
	shape := regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`)
	seen := map[string]bool{}
	for range 16 {
		secret, err := NewUserPassword()
		if err != nil {
			t.Fatal(err)
		}
		if !shape.MatchString(secret) {
			t.Fatalf("%q is not 32 URL-safe characters", secret)
		}
		if Judge(secret) != nil {
			t.Fatalf("%q does not pass the password rule", secret)
		}
		if seen[secret] {
			t.Fatalf("%q was drawn twice", secret)
		}
		seen[secret] = true
	}
}

func TestParallelFirstSignInsNeverExceedTheCap(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	r.limit = 3
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Put(ctx, User{ID: fmt.Sprintf("p%d", i), Username: fmt.Sprintf("Par %d", i), Hash: "h"})
		}()
	}
	wg.Wait()
	users, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) > 3 {
		t.Fatalf("%d users stored, the cap is 3", len(users))
	}
}

func TestANewUserNeverReplacesOneThatHoldsTheDrawnID(t *testing.T) {
	r, _ := newUsers(t)
	ctx := context.Background()
	kept := addUser(t, r, "abc123", "kept", "kept@example.com")
	err := r.Create(ctx, User{ID: "abc123", Username: "other", Email: "other@example.com", Hash: "h"})
	if !errors.Is(err, ErrUserIDTaken) {
		t.Fatalf("a taken id answered %v", err)
	}
	got, _, _ := r.Get(ctx, "abc123")
	if got.Username != kept.Username || got.Hash != kept.Hash {
		t.Fatalf("the existing user was overwritten: %+v", got)
	}
}

func TestAddDrawsAgainWhenAnIDIsTaken(t *testing.T) {
	r, _ := newUsers(t)
	users := NewUsers(r)
	taken := 0
	got, err := users.Add(context.Background(), func(id string) User {
		taken++
		if taken == 1 {
			addUser(t, r, id, "squatter", "")
		}
		return User{ID: id, Username: "fresh", Hash: "h"}
	})
	if err != nil || taken != 2 || got.Username != "fresh" {
		t.Fatalf("%+v after %d tries: %v", got, taken, err)
	}
}
