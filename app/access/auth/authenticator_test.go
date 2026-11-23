package auth

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

func newUserAuth(t *testing.T) (*Authenticator, *store.Store) {
	t.Helper()
	s := store.Migrated(t)
	db := func() *repository.Store { return s.Store }
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := NewAuthenticator(NewStore(db), nil, bcryptHasher(), "", func() []byte { return secret }, true, false)
	a.Users = NewUsers(NewUserRepo(db, 0))
	return a, s
}

func withCookie(cookie string) *nethttp.Request {
	r := httptest.NewRequest(nethttp.MethodGet, "/", nil)
	r.AddCookie(&nethttp.Cookie{Name: CookieName, Value: cookie})
	return r
}

func addStandardUser(t *testing.T, a *Authenticator, id, name, email string) User {
	t.Helper()
	hash, err := a.Pass.Hash("a-user-password")
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: id, Username: name, Email: email, Hash: hash}
	if err := a.Users.Put(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAUserSessionNamesThatUser(t *testing.T) {
	a, _ := newUserAuth(t)
	ctx := context.Background()
	u := addStandardUser(t, a, "u1", "Dimas", "dimas@example.com")
	cookie, ok := a.IssueUser(ctx, u, MethodPassword, false)
	if !ok {
		t.Fatal("a user session was not issued")
	}
	info, ok := a.SessionInfo(withCookie(cookie.Value))
	if !ok {
		t.Fatal("the user session did not verify")
	}
	if info.Role != RoleUser || info.UserID != "u1" || info.Name != "Dimas" || info.Email != "dimas@example.com" {
		t.Fatalf("%+v", info)
	}
	if info.Admin() {
		t.Fatal("a standard user is reported as the admin")
	}
	if a.Session(withCookie(cookie.Value)) != true {
		t.Fatal("Session did not agree with SessionInfo")
	}
}

func TestAnAdminSessionStaysTheAdmin(t *testing.T) {
	a, _ := newUserAuth(t)
	ctx := context.Background()
	addStandardUser(t, a, "u1", "Dimas", "")
	cookie, ok := a.Issue(ctx, MethodPassword, "", "", false)
	if !ok {
		t.Fatal("the admin session was not issued")
	}
	info, ok := a.SessionInfo(withCookie(cookie.Value))
	if !ok || info.Role != RoleAdmin || info.UserID != "" {
		t.Fatalf("%+v %v", info, ok)
	}
}

func TestAUserPasswordChangeOrDeletionEndsTheirSession(t *testing.T) {
	a, _ := newUserAuth(t)
	ctx := context.Background()
	u := addStandardUser(t, a, "u1", "Dimas", "")
	other := addStandardUser(t, a, "u2", "Other", "")
	mine, _ := a.IssueUser(ctx, u, MethodPassword, false)
	theirs, _ := a.IssueUser(ctx, other, MethodPassword, false)

	// A reset changes only this user's hash.
	u.Hash, _ = a.Pass.Hash("a-new-password")
	if err := a.Users.Put(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.SessionInfo(withCookie(mine.Value)); ok {
		t.Fatal("a session survived its user's password change")
	}
	if _, ok := a.SessionInfo(withCookie(theirs.Value)); !ok {
		t.Fatal("another user's session ended with a password change")
	}

	// Deleting the account ends the sessions it holds.
	if err := a.Users.Delete(ctx, "u2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.SessionInfo(withCookie(theirs.Value)); ok {
		t.Fatal("a deleted user's session still verifies")
	}
}

func TestATokenForAnUnknownUserIsNotASession(t *testing.T) {
	a, _ := newUserAuth(t)
	addStandardUser(t, a, "u1", "Dimas", "")
	token, err := Sign([]byte("0123456789abcdef0123456789abcdef"), Claims{
		Authenticated: true, Method: MethodPassword, UID: "nope", PV: PV([]byte("0123456789abcdef0123456789abcdef"), "h", "0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.SessionInfo(withCookie(token)); ok {
		t.Fatal("a token naming no stored user was accepted")
	}
}

func enableOIDC(t *testing.T, a *Authenticator, s *store.Store) {
	t.Helper()
	if err := s.Store.Settings().Patch(context.Background(), map[string]string{
		"authMode": `"both"`, "oidcIssuerUrl": `"https://idp.example"`,
		"oidcClientId": `"client"`, "oidcClientSecret": `"secret"`,
	}); err != nil {
		t.Fatal(err)
	}
	a.Store.Changed()
	a.OIDCUsable = func(issuer, clientID, secret string) bool { return true }
	a.SSOAdmin = func(string, bool) bool { return false }
}

func TestSingleSignOnProvisionsAnUnlistedAddressAsAStandardUser(t *testing.T) {
	a, s := newUserAuth(t)
	ctx := context.Background()
	enableOIDC(t, a, s)

	cookie, ok := a.IssueSSO(ctx, MethodOIDC, "Anyone@Example.com", "Anyone", false, false)
	if !ok {
		t.Fatal("an unlisted address was refused")
	}
	info, ok := a.SessionInfo(withCookie(cookie.Value))
	if !ok || info.Role != RoleUser || info.UserID == "" {
		t.Fatalf("%+v %v", info, ok)
	}
	u, found := a.Users.ByEmail(ctx, "anyone@example.com")
	if !found || u.Username != "anyone@example.com" || !u.Provisioned || u.Hash == "" {
		t.Fatalf("%+v %v", u, found)
	}

	// The second sign-in reuses the row.
	again, ok := a.IssueSSO(ctx, MethodOIDC, "anyone@example.com", "Anyone", false, false)
	if !ok {
		t.Fatal("the second sign-in was refused")
	}
	if info, _ := a.SessionInfo(withCookie(again.Value)); info.UserID != u.ID {
		t.Fatalf("%+v", info)
	}
	if list := a.Users.All(ctx); len(list) != 1 {
		t.Fatalf("%d users after two sign-ins", len(list))
	}
}

func TestAnEditableClaimMustAlreadyBelongToAnAccount(t *testing.T) {
	a, s := newUserAuth(t)
	ctx := context.Background()
	enableOIDC(t, a, s)

	if _, ok := a.IssueSSO(ctx, MethodOIDC, "claim@example.com", "", true, false); ok {
		t.Fatal("a user-editable claim provisioned an account")
	}
	u := addStandardUser(t, a, "u1", "Claim", "claim@example.com")
	cookie, ok := a.IssueSSO(ctx, MethodOIDC, "CLAIM@example.com", "", true, false)
	if !ok {
		t.Fatal("an existing account was refused for an editable claim")
	}
	if info, _ := a.SessionInfo(withCookie(cookie.Value)); info.UserID != u.ID {
		t.Fatalf("%+v", info)
	}
}

func TestAListedAddressSignsInAsTheAdmin(t *testing.T) {
	a, s := newUserAuth(t)
	ctx := context.Background()
	enableOIDC(t, a, s)
	a.SSOAdmin = func(email string, exact bool) bool { return strings.EqualFold(email, "ops@example.com") }

	cookie, ok := a.IssueSSO(ctx, MethodOIDC, "Ops@Example.com", "Ops", false, false)
	if !ok {
		t.Fatal("a listed address was refused")
	}
	if info, _ := a.SessionInfo(withCookie(cookie.Value)); info.Role != RoleAdmin || info.UserID != "" {
		t.Fatalf("%+v", info)
	}
	if list := a.Users.All(ctx); len(list) != 0 {
		t.Fatalf("%d user rows for an admin sign-in", len(list))
	}

	// Any other address is still a standard user.
	other, ok := a.IssueSSO(ctx, MethodOIDC, "other@example.com", "Other", false, false)
	if !ok {
		t.Fatal("an unlisted address was refused")
	}
	if info, _ := a.SessionInfo(withCookie(other.Value)); info.Role != RoleUser {
		t.Fatalf("%+v", info)
	}
}

func TestProvisioningRespectsTheUserCap(t *testing.T) {
	a, s := newUserAuth(t)
	ctx := context.Background()
	enableOIDC(t, a, s)
	a.Users.repo.limit = 1
	if err := a.Users.Put(ctx, User{ID: "u1", Username: "seed", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.IssueSSO(ctx, MethodOIDC, "new@example.com", "", false, false); ok {
		t.Fatal("an account was provisioned past the cap")
	}
}
