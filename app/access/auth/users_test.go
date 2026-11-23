package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
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
	if _, ok, _ := r.ByUsername(ctx, "dimas"); !ok {
		t.Fatal("a username lookup ignored case")
	}
	if _, ok, _ := r.ByEmail(ctx, "DIMAS@example.com"); !ok {
		t.Fatal("an email lookup ignored case")
	}
	if _, ok, _ := r.ByUsername(ctx, "nobody"); ok {
		t.Fatal("an unknown username was found")
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
	if got, _ := r.OwnerOf(ctx, "k1"); got != "u1" {
		t.Fatalf("owner %q", got)
	}
	keys, err := r.KeysOf(ctx, "u1")
	if err != nil || len(keys) != 2 || keys[0] != "k1" || keys[1] != "k2" {
		t.Fatalf("%v %v", keys, err)
	}
	if err := r.SetOwner(ctx, "k1", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.OwnerOf(ctx, "k1"); got != "" {
		t.Fatalf("a cleared assignment still names %q", got)
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
	if got, _ := r.OwnerOf(ctx, key.ID); got != "" {
		t.Fatalf("the key still belongs to %q", got)
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
	if users.Any(ctx) {
		t.Fatal("an empty table reports a user")
	}
	addUser(t, r, "u1", "Dimas", "dimas@example.com")
	users.Changed()
	if !users.Any(ctx) {
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
	if none.Any(ctx) || none.All(ctx) != nil {
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
