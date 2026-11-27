package repository

import (
	"context"
	"errors"
	"testing"
)

func addUserRow(t *testing.T, r *UsersRepo, id, username, email string) UserRow {
	t.Helper()
	u := UserRow{ID: id, Username: username, Email: email, Password: "hash-" + id, CreatedAt: "2026-11-23T00:00:00Z"}
	if err := r.Create(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAUserRoundTripsThroughItsTable(t *testing.T) {
	s := newMigrated(t)
	r := s.Users()
	ctx := context.Background()

	addUserRow(t, r, "u1", "Dimas", "dimas@example.com")
	addUserRow(t, r, "u2", "Other", "")

	u, ok, err := r.Get(ctx, "u1")
	if err != nil || !ok {
		t.Fatalf("%+v %v %v", u, ok, err)
	}
	if u.Username != "Dimas" || u.Email != "dimas@example.com" || u.Password != "hash-u1" || u.Provisioned {
		t.Fatalf("%+v", u)
	}
	// An address with no case and an empty one.
	if got, ok, _ := r.Get(ctx, "u2"); !ok || got.Email != "" {
		t.Fatalf("%+v %v: an empty email is not empty", got, ok)
	}
	list, err := r.List(ctx)
	if err != nil || len(list) != 2 || list[0].ID != "u1" {
		t.Fatalf("%+v %v", list, err)
	}
}

func TestAFullNameAndTheLastSSONameRoundTrip(t *testing.T) {
	s := newMigrated(t)
	r := s.Users()
	ctx := context.Background()
	if err := r.Create(ctx, UserRow{
		ID: "u1", Username: "dimas", Password: "h", CreatedAt: "t",
		FullName: "Dimas Restu", SSOName: "D. Restu",
	}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.Get(ctx, "u1")
	if err != nil || !ok || got.FullName != "Dimas Restu" || got.SSOName != "D. Restu" {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	empty := ""
	got, err = r.Update(ctx, "u1", UserPatch{FullName: &empty})
	if err != nil || got.FullName != "" || got.SSOName != "D. Restu" {
		t.Fatalf("%+v %v", got, err)
	}
	restored := "Dimas Restu"
	if got, err := r.Update(ctx, "u1", UserPatch{FullName: &restored}); err != nil || got.FullName != restored {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestUsernameAndEmailAreUnique(t *testing.T) {
	s := newMigrated(t)
	r := s.Users()
	addUserRow(t, r, "u1", "Dimas", "dimas@example.com")
	// Two users without an address are fine; the column is NULL, not "".
	addUserRow(t, r, "u2", "Other", "")
	addUserRow(t, r, "u3", "Third", "")

	for _, u := range []UserRow{
		{ID: "x1", Username: "Dimas", Password: "h", CreatedAt: "t"},
		{ID: "x2", Username: "Another", Email: "DIMAS@example.com", Password: "h", CreatedAt: "t"},
	} {
		if err := r.Create(context.Background(), u); err == nil {
			t.Fatalf("%s: a duplicate was stored", u.Username)
		}
	}
}

func TestAUserUpdateLeavesTheRestAlone(t *testing.T) {
	s := newMigrated(t)
	r := s.Users()
	addUserRow(t, r, "u1", "Dimas", "dimas@example.com")
	ctx := context.Background()

	name, hash := "Dimas R", "hash-new"
	provisioned := false
	got, err := r.Update(ctx, "u1", UserPatch{Username: &name, Password: &hash, Provisioned: &provisioned})
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != name || got.Password != hash || got.Email != "dimas@example.com" || got.Provisioned {
		t.Fatalf("%+v", got)
	}
	empty := ""
	if got, err := r.Update(ctx, "u1", UserPatch{Email: &empty}); err != nil || got.Email != "" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := r.Update(ctx, "nope", UserPatch{Username: &name}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v, want ErrNotFound", err)
	}
}

func TestDeletingAUserKeepsTheKeys(t *testing.T) {
	s := newMigrated(t)
	r := s.Users()
	ctx := context.Background()
	addUserRow(t, r, "u1", "Dimas", "")
	if err := r.SetOwner(ctx, "k1", "u1"); err != nil {
		t.Fatal(err)
	}
	ok, err := r.Delete(ctx, "u1")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, found, _ := r.Get(ctx, "u1"); found {
		t.Fatal("the user survived the delete")
	}
	if owners, _ := r.Owners(ctx); owners["k1"] != "" {
		t.Fatalf("the assignment survived: %q", owners["k1"])
	}
	if ok, _ := r.Delete(ctx, "u1"); ok {
		t.Fatal("deleting a missing user reported one")
	}
}

func TestKeyAssignmentsAreUpsertedAndCleared(t *testing.T) {
	s := newMigrated(t)
	r := s.Users()
	ctx := context.Background()
	addUserRow(t, r, "u1", "Dimas", "")
	addUserRow(t, r, "u2", "Other", "")

	for _, key := range []string{"k2", "k1"} {
		if err := r.SetOwner(ctx, key, "u1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.SetOwner(ctx, "k3", "u2"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetOwner(ctx, "k1", "u2"); err != nil {
		t.Fatal(err)
	}
	owners, err := r.Owners(ctx)
	if err != nil || len(owners) != 3 || owners["k1"] != "u2" {
		t.Fatalf("%v %v", owners, err)
	}
	keys, err := r.KeysOf(ctx, "u1")
	if err != nil || len(keys) != 1 || keys[0] != "k2" {
		t.Fatalf("%v %v", keys, err)
	}
	if err := r.ClearOwner(ctx, "k3"); err != nil {
		t.Fatal(err)
	}
	if owners, _ := r.Owners(ctx); owners["k3"] != "" {
		t.Fatalf("a cleared assignment still names %q", owners["k3"])
	}
}
