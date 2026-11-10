package repository

import (
	"context"
	"errors"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConnectionCreateAssignsPriorityAndWhitelistsFields(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "k1", Fields: map[string]string{"apiKey": `"sk-1"`, "backoffLevel": `5`, "modelLock_x": `"2026"`, "displayName": `"One"`}})
	b := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "k2", Fields: map[string]string{"apiKey": `"sk-2"`}})
	c := mkConn(t, s, ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@x.io", Fields: map[string]string{"accessToken": `"t"`}})
	if a.Priority != 1 || b.Priority != 2 || c.Priority != 1 {
		t.Fatalf("priorities = %d %d %d, want 1 2 1", a.Priority, b.Priority, c.Priority)
	}
	if !a.IsActive || a.CreatedAt == "" || a.CreatedAt != a.UpdatedAt || len(a.ID) != 36 {
		t.Fatalf("defaults: %+v", a)
	}
	d, _ := document.ParseDoc(a.Data)
	if d.Has("backoffLevel") || d.Has("modelLock_x") {
		t.Fatalf("a new row must drop non-whitelisted fields: %s", a.Data)
	}
	if v, _ := d.GetString("apiKey"); v != "sk-1" {
		t.Fatalf("whitelisted field lost: %s", a.Data)
	}
	if v, _ := d.GetString("displayName"); v != "One" {
		t.Fatalf("displayName lost: %s", a.Data)
	}
	if c.Name != "a@x.io" {
		t.Fatalf("an unnamed OAuth connection takes its email as name, got %q", c.Name)
	}
}

func TestConnectionCreateRejectsBadInput(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if _, err := s.Connections().Create(ctx, ConnInput{Provider: "p", AuthType: "apikey", Name: "x", PSD: `[1]`}, nil); err == nil {
		t.Error("a providerSpecificData that is not an object was accepted")
	}
	if _, err := s.Connections().Create(ctx, ConnInput{Provider: "p", AuthType: "apikey", Name: "x", Fields: map[string]string{"apiKey": `{bad`}}, nil); err == nil {
		t.Error("a field that is not JSON was accepted")
	}
	if n, _ := s.Connections().Count(ctx); n != 0 {
		t.Errorf("a refused create stored %d rows", n)
	}
}

func TestConnectionNamesForUnnamedLogins(t *testing.T) {
	s := newMigrated(t)
	first := mkConn(t, s, ConnInput{Provider: "x", AuthType: "access_token"})
	second := mkConn(t, s, ConnInput{Provider: "x", AuthType: "access_token"})
	if first.Name != "Account 1" || second.Name != "Account 2" {
		t.Fatalf("names = %q %q", first.Name, second.Name)
	}
	gh := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "e@x.io", PSD: `{"githubLogin":"octo"}`})
	if gh.Name != "octo" {
		t.Fatalf("github name = %q", gh.Name)
	}
	key := mkConn(t, s, ConnInput{Provider: "x", AuthType: "apikey"})
	if key.Name != "" {
		t.Fatalf("an unnamed API key stays unnamed, got %q", key.Name)
	}
	if n := s.one(t, `SELECT count(*) FROM "providerConnections" WHERE "id"=? AND "name" IS NULL`, key.ID); n != "1" {
		t.Fatal("an empty name must be stored as NULL")
	}
}

func TestConnectionListSortsByPriorityWithNullLast(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "a"})
	b := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "b"})
	c := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "c"})
	s.exec(t, `UPDATE "providerConnections" SET "priority"=NULL WHERE "id"=?`, a.ID)
	list, _ := s.Connections().List(ctx, ConnFilter{Provider: "p"})
	if list[0].ID != b.ID || list[1].ID != c.ID || list[2].ID != a.ID {
		t.Fatalf("order = %s %s %s", list[0].Name, list[1].Name, list[2].Name)
	}
	active, inactive := true, false
	if _, err := s.Connections().Update(ctx, b.ID, ConnPatch{IsActive: &inactive}); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Connections().List(ctx, ConnFilter{Provider: "p", IsActive: &active})
	if len(list) != 2 {
		t.Fatalf("active filter returned %d rows", len(list))
	}
	if all, _ := s.Connections().List(ctx, ConnFilter{}); len(all) != 3 {
		t.Fatalf("an empty filter returned %d rows", len(all))
	}
}

func TestConnectionColumnsOverrideDataKeys(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "col"})
	s.exec(t, `UPDATE "providerConnections" SET "data"='{"name":"from-data","priority":99,"custom":1}' WHERE "id"=?`, a.ID)
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if got.Name != "col" || got.Priority != 1 {
		t.Fatalf("columns must win over data: %+v", got)
	}
	if v, _ := got.Doc().GetRaw("custom"); v != "1" {
		t.Fatalf("custom data field lost")
	}
}

func TestOAuthDedupRules(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	first := mkConn(t, s, ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@x.io", Fields: map[string]string{"accessToken": `"old"`}})
	again, err := s.Connections().Create(ctx, ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@x.io", Fields: map[string]string{"accessToken": `"new"`}}, nil)
	if err != nil || again.ID != first.ID {
		t.Fatalf("same email must update the existing row: %v %+v", err, again)
	}
	if v, _ := again.Doc().GetString("accessToken"); v != "new" {
		t.Fatalf("overwrite did not apply: %s", again.Data)
	}
	if list, _ := s.Connections().List(ctx, ConnFilter{Provider: "claude"}); len(list) != 1 {
		t.Fatalf("%d rows after a dedup hit", len(list))
	}

	c1 := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "w@x.io", PSD: `{"chatgptAccountId":"ws1"}`})
	c2 := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "w@x.io", PSD: `{"chatgptAccountId":"ws2"}`})
	if c1.ID == c2.ID {
		t.Fatal("different workspaces must not collapse")
	}
	c3 := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "w@x.io", PSD: `{"chatgptAccountId":"ws1"}`})
	if c3.ID != c1.ID {
		t.Fatal("same workspace must update the existing row")
	}
	c4 := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "w@x.io"})
	if c4.ID == c1.ID || c4.ID == c2.ID {
		t.Fatal("a codex login without a workspace id must never collapse onto an existing row")
	}

	u1 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "u@x.io", PSD: `{"username":"alice"}`})
	u2 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "u@x.io", PSD: `{"username":"bob"}`})
	u3 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "u@x.io"})
	if u1.ID == u2.ID || u3.ID == u1.ID || u3.ID == u2.ID {
		t.Fatal("different usernames, or username on one side only, must stay distinct")
	}
	u4 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "u@x.io", PSD: `{"username":"alice"}`})
	if u4.ID != u1.ID {
		t.Fatal("same email and username must update")
	}
	n1 := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "n@x.io"})
	n2 := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "n@x.io"})
	if n1.ID != n2.ID {
		t.Fatal("same email with no username on either side must update")
	}
	w1 := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "z@x.io", PSD: `{"chatgptAccountId":"w1"}`})
	w2 := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "z@x.io", PSD: `{"chatgptAccountId":"w1"}`})
	w3 := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "z@x.io", PSD: `{"chatgptAccountId":"w2"}`})
	if w1.ID != w2.ID || w3.ID == w1.ID {
		t.Fatal("a workspace id decides the identity when both sides have one")
	}
	t1 := mkConn(t, s, ConnInput{Provider: "x", AuthType: "access_token", Name: "same"})
	t2 := mkConn(t, s, ConnInput{Provider: "x", AuthType: "access_token", Name: "same"})
	if t1.ID == t2.ID {
		t.Fatal("access_token logins must never be deduplicated")
	}
}

func TestAPIKeyDedupAndNameConflict(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "Key 1", Fields: map[string]string{"apiKey": `"one"`}})
	b, err := s.Connections().Create(ctx, ConnInput{Provider: "openai", AuthType: "apikey", Name: "Key 1", Fields: map[string]string{"apiKey": `"two"`}}, nil)
	if err != nil || b.ID != a.ID {
		t.Fatalf("a repeated name overwrites by default: %v %+v", err, b)
	}
	no := false
	_, err = s.Connections().Create(ctx, ConnInput{Provider: "openai", AuthType: "apikey", Name: "Key 1"}, &no)
	var nc *ErrNameConflict
	if !errors.As(err, &nc) || nc.ExistingID != a.ID || nc.ExistingName != "Key 1" {
		t.Fatalf("err = %v, want ErrNameConflict naming the existing row", err)
	}
	yes := true
	c, err := s.Connections().Create(ctx, ConnInput{Provider: "openai", AuthType: "apikey", Name: "Key 1"}, &yes)
	if err != nil || c.ID != a.ID {
		t.Fatalf("an explicit overwrite must update: %v %+v", err, c)
	}
	other := mkConn(t, s, ConnInput{Provider: "groq", AuthType: "apikey", Name: "Key 1"})
	if other.ID == a.ID {
		t.Fatal("names are unique per provider only")
	}
}

func TestCreateOverwriteCanSwitchTheAccountOff(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "K"})
	off := false
	got, err := s.Connections().Create(ctx, ConnInput{Provider: "openai", AuthType: "apikey", Name: "K", IsActive: &off}, nil)
	if err != nil || got.ID != a.ID || got.IsActive {
		t.Fatalf("%v %+v", err, got)
	}
	back, _, _ := s.Connections().Get(ctx, a.ID)
	if back.IsActive {
		t.Fatal("the stored row is still active")
	}
}

func TestUpdateMergesOnlyPatchedKeys(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "oauth", Email: "e@x.io", Fields: map[string]string{"accessToken": `"a"`, "refreshToken": `"r"`}, PSD: `{"k":1}`})
	setRaw(t, s, a.ID, map[string]string{"unknownField": `{"x":[1,2]}`})
	before, _, _ := s.Connections().Get(ctx, a.ID)
	time.Sleep(3 * time.Millisecond)
	upd, err := s.Connections().Update(ctx, a.ID, ConnPatch{Fields: map[string]string{"accessToken": `"b"`}})
	if err != nil {
		t.Fatal(err)
	}
	if upd.UpdatedAt <= before.UpdatedAt || upd.CreatedAt != before.CreatedAt {
		t.Fatalf("timestamps: created %s->%s updated %s->%s", before.CreatedAt, upd.CreatedAt, before.UpdatedAt, upd.UpdatedAt)
	}
	d := upd.Doc()
	if v, _ := d.GetString("accessToken"); v != "b" {
		t.Fatalf("patch not applied: %s", upd.Data)
	}
	if v, _ := d.GetString("refreshToken"); v != "r" {
		t.Fatalf("an unpatched key changed: %s", upd.Data)
	}
	if v, _ := d.GetRaw("unknownField"); v != `{"x":[1,2]}` {
		t.Fatalf("unknown field damaged: %s", upd.Data)
	}
	for _, k := range []string{"refreshToken", "providerSpecificData", "unknownField"} {
		a1, _ := before.Doc().GetRaw(k)
		a2, _ := d.GetRaw(k)
		if a1 != a2 {
			t.Fatalf("%s bytes changed: %q -> %q", k, a1, a2)
		}
	}
}

func TestAGitHubLoginWithNoEmailIsTheSameAccountByItsUserID(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", PSD: `{"githubUserId":"42","githubLogin":"octo"}`})
	again := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", PSD: `{"githubUserId":"42","githubLogin":"octo2"}`})
	if again.ID != a.ID {
		t.Fatalf("the same GitHub user got a second row: %s and %s", a.ID, again.ID)
	}
	if got := psdOf(again).Get("githubLogin").String(); got != "octo2" {
		t.Fatalf("the row was not updated: %s", again.Data)
	}
	other := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", PSD: `{"githubUserId":"43"}`})
	if other.ID == a.ID {
		t.Fatal("another GitHub user took the row")
	}
	// The user id decides; a shared email does not merge two users.
	e1 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "same@x.io", PSD: `{"githubUserId":"50"}`})
	e2 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "same@x.io", PSD: `{"githubUserId":"51"}`})
	if e1.ID == e2.ID {
		t.Fatal("two users with one email were merged")
	}
	// A user id of one provider never matches a row of another.
	gl := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", PSD: `{"githubUserId":"42"}`})
	if gl.ID == a.ID {
		t.Fatal("a row of another provider matched")
	}
	// Without a user id the old rule is untouched.
	n1 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "plain@x.io"})
	n2 := mkConn(t, s, ConnInput{Provider: "github", AuthType: "oauth", Email: "plain@x.io"})
	if n1.ID != n2.ID {
		t.Fatal("the email rule stopped working")
	}
}

func TestUpdateChangesTheColumnFields(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "old", Email: "o@x.io"})
	name, email, off := "new", "n@x.io", false
	got, err := s.Connections().Update(ctx, a.ID, ConnPatch{Name: &name, Email: &email, IsActive: &off})
	if err != nil || got.Name != "new" || got.Email != "n@x.io" || got.IsActive {
		t.Fatalf("%+v %v", got, err)
	}
	back, _, _ := s.Connections().Get(ctx, a.ID)
	if back.Name != "new" || back.Email != "n@x.io" || back.IsActive {
		t.Fatalf("stored = %+v", back)
	}
	empty := ""
	if _, err := s.Connections().Update(ctx, a.ID, ConnPatch{Email: &empty}); err != nil {
		t.Fatal(err)
	}
	if n := s.one(t, `SELECT count(*) FROM "providerConnections" WHERE "id"=? AND "email" IS NULL`, a.ID); n != "1" {
		t.Fatal("an emptied email must be stored as NULL")
	}
	if _, err := s.Connections().Update(ctx, "nope", ConnPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing row: %v", err)
	}
}

func TestActivationResetsHealthAndEveryLock(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	exp := time.Now().Add(time.Hour)
	if err := lockAt(s, a.ID, "google/gemma-4:free", exp, 429, "rate limited", 1); err != nil {
		t.Fatal(err)
	}
	if err := lockAt(s, a.ID, "", exp, 503, "down", 1); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if v, _ := d.GetString("modelLock_google/gemma-4:free"); v == "" {
		t.Fatalf("model lock missing: %s", got.Data)
	}
	if _, ok := d.GetString("modelLock___all"); !ok {
		t.Fatalf("account-wide lock missing: %s", got.Data)
	}
	upd, err := s.Connections().Update(ctx, a.ID, ConnPatch{Fields: map[string]string{"testStatus": `"active"`}})
	if err != nil {
		t.Fatal(err)
	}
	d = upd.Doc()
	if !d.IsNull("modelLock_google/gemma-4:free") || !d.IsNull("modelLock___all") {
		t.Fatalf("activation must null every lock: %s", upd.Data)
	}
	if v, _ := d.GetRaw("backoffLevel"); v != "0" {
		t.Fatalf("backoffLevel = %s", v)
	}
	if !d.IsNull("errorCode") || !d.IsNull("rateLimitedUntil") || !d.IsNull("lastError") || !d.IsNull("lastErrorAt") {
		t.Fatalf("error fields not cleared: %s", upd.Data)
	}
}

func TestActivationKeepsAnErrorTheCallerSetInTheSameCall(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	upd, err := s.Connections().Update(context.Background(), a.ID, ConnPatch{Fields: map[string]string{
		"testStatus": `"active"`, "lastError": `"probe note"`, "lastErrorAt": `"2026-01-01T00:00:00.000Z"`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := upd.Doc().GetString("lastError"); v != "probe note" {
		t.Fatalf("activation erased a field set in the same call: %s", upd.Data)
	}
}

func TestPriorityRenumberingOnUpdateAndDelete(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var ids []string
	for _, n := range []string{"a", "b", "c", "d"} {
		ids = append(ids, mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: n}).ID)
	}
	names := func() string {
		list, _ := s.Connections().List(ctx, ConnFilter{Provider: "p"})
		var b []string
		for _, c := range list {
			b = append(b, c.Name+":"+strconv.FormatInt(c.Priority, 10))
		}
		return strings.Join(b, ",")
	}
	if names() != "a:1,b:2,c:3,d:4" {
		t.Fatalf("initial %s", names())
	}
	two := int64(2)
	if _, err := s.Connections().Update(ctx, ids[3], ConnPatch{Priority: &two}); err != nil {
		t.Fatal(err)
	}
	if names() != "a:1,d:2,b:3,c:4" {
		t.Fatalf("after move: %s", names())
	}
	if ok, err := s.Connections().Delete(ctx, ids[0]); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if names() != "d:1,b:2,c:3" {
		t.Fatalf("after delete: %s", names())
	}
	if ok, _ := s.Connections().Delete(ctx, "nope"); ok {
		t.Fatal("deleting a missing row reported success")
	}
	mkConn(t, s, ConnInput{Provider: "q", AuthType: "apikey", Name: "other"})
	if list, _ := s.Connections().List(ctx, ConnFilter{Provider: "q"}); list[0].Priority != 1 {
		t.Fatal("another provider's priorities changed")
	}
}

func TestMarkUnavailableReadsBackoffInsideTheTransaction(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := lockAt(s, a.ID, "m", time.Now().Add(time.Minute), 429, "r", 1); err != nil {
				t.Errorf("mark: %v", err)
			}
		}()
	}
	wg.Wait()
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetRaw("backoffLevel"); v != "30" {
		t.Fatalf("backoffLevel = %s, want 30: concurrent failures computed the same level from a stale read", v)
	}
	if v, _ := got.Doc().GetString("testStatus"); v != "unavailable" {
		t.Fatalf("testStatus = %q", v)
	}
	if v, _ := got.Doc().GetRaw("errorCode"); v != "429" {
		t.Fatalf("errorCode = %s (must be a number)", v)
	}
}

func TestMarkUnavailableTruncatesTheReason(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	mustLock(t, s, a.ID, "m", time.Now().Add(time.Minute), 500, strings.Repeat("é", 500), 1)
	got, _, _ := s.Connections().Get(context.Background(), a.ID)
	reason, _ := got.Doc().GetString("lastError")
	if n := len([]rune(reason)); n != 200 {
		t.Fatalf("lastError has %d characters, want 200", n)
	}
}

func TestClearLocksKeepsALockThatAppearedMeanwhile(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	now := time.Now()
	mustLock(t, s, a.ID, "ok-model", now.Add(-time.Minute), 429, "r", 1)
	mustLock(t, s, a.ID, "busy-model", now.Add(time.Hour), 429, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "ok-model", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if !lockCleared(d, "modelLock_ok-model") {
		t.Fatalf("the succeeded model's lock must be cleared: %s", got.Data)
	}
	if v, _ := d.GetString("modelLock_busy-model"); v == "" {
		t.Fatalf("an active lock for another model must survive: %s", got.Data)
	}
	if v, _ := d.GetString("testStatus"); v != "unavailable" {
		t.Fatalf("the account must stay unavailable while a lock is active, testStatus=%q", v)
	}
	if err := s.Connections().ClearLocks(ctx, a.ID, "busy-model", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetString("testStatus"); v != "active" {
		t.Fatalf("testStatus = %q after the last lock cleared", v)
	}
	if v, _ := got.Doc().GetRaw("backoffLevel"); v != "0" {
		t.Fatalf("backoffLevel = %s", v)
	}
}

func TestTouchUsage(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	var wg sync.WaitGroup
	for range 25 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Connections().TouchUsageAt(ctx, a.ID, true, time.Now()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetRaw("consecutiveUseCount"); v != "25" {
		t.Fatalf("consecutiveUseCount = %s, want 25: increments were lost", v)
	}
	if v, _ := got.Doc().GetString("lastUsedAt"); v == "" {
		t.Fatal("lastUsedAt missing")
	}
	if err := s.Connections().TouchUsageAt(ctx, a.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetRaw("consecutiveUseCount"); v != "1" {
		t.Fatalf("a switch to another account restarts the count at 1, got %s", v)
	}
}

func TestCorruptRowsAreFlaggedAndNeverWritten(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "bad"})
	b := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "good"})
	s.exec(t, `UPDATE "providerConnections" SET "data"='{"a":1,"a":2}' WHERE "id"=?`, a.ID)

	list, err := s.Connections().List(ctx, ConnFilter{Provider: "p"})
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	var corrupt int
	for _, c := range list {
		if c.Corrupt {
			corrupt++
			if c.ID != a.ID || c.Data != `{"a":1,"a":2}` {
				t.Fatalf("flagged the wrong row or lost the raw text: %+v", c)
			}
		}
	}
	if corrupt != 1 {
		t.Fatalf("%d rows flagged, want 1", corrupt)
	}
	if _, err := s.Connections().Update(ctx, a.ID, ConnPatch{Fields: map[string]string{"x": `1`}}); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("update of a corrupt row: %v", err)
	}
	if err := s.Connections().TouchUsageAt(ctx, a.ID, true, time.Now()); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("TouchUsage on a corrupt row: %v", err)
	}
	if err := lockAt(s, a.ID, "m", time.Now(), 500, "r", 1); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("MarkUnavailable on a corrupt row: %v", err)
	}
	if raw := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id"=?`, a.ID); raw != `{"a":1,"a":2}` {
		t.Fatalf("the corrupt row was rewritten: %s", raw)
	}
	if _, err := s.Connections().Update(ctx, b.ID, ConnPatch{Fields: map[string]string{"x": `1`}}); err != nil {
		t.Fatalf("a healthy neighbour must still be writable: %v", err)
	}
}

func TestClearLocksAlsoClearsTheAccountLockAndExpiredLocks(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	now := time.Now()
	mustLock(t, s, a.ID, "ok-model", now.Add(time.Hour), 429, "r", 1)
	mustLock(t, s, a.ID, "", now.Add(time.Hour), 503, "r", 1)
	mustLock(t, s, a.ID, "old-model", now.Add(-time.Minute), 429, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "ok-model", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	for _, k := range []string{"modelLock_ok-model", "modelLock___all", "modelLock_old-model"} {
		if !d.IsNull(k) {
			t.Errorf("%s must be cleared after a success: %s", k, got.Data)
		}
	}
	if v, _ := d.GetString("testStatus"); v != "active" {
		t.Errorf("testStatus = %q, want active once no lock is left", v)
	}
}

func TestClearLocksKeepsAnActiveLockOfAnotherModelWhenItClearsTheAccountLock(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	now := time.Now()
	mustLock(t, s, a.ID, "", now.Add(time.Hour), 503, "r", 1)
	mustLock(t, s, a.ID, "busy-model", now.Add(time.Hour), 429, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "ok-model", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if !d.IsNull("modelLock___all") {
		t.Errorf("the account lock must be cleared by a success: %s", got.Data)
	}
	if v, _ := d.GetString("modelLock_busy-model"); v == "" {
		t.Errorf("another model's active lock must survive: %s", got.Data)
	}
	if v, _ := d.GetString("testStatus"); v != "unavailable" {
		t.Errorf("testStatus = %q, want unavailable while a lock is active", v)
	}
}

func TestClearLocksForAnEmptyModelOnlyDropsExpiredLocks(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	now := time.Now()
	mustLock(t, s, a.ID, "busy-model", now.Add(time.Hour), 429, "r", 1)
	mustLock(t, s, a.ID, "old-model", now.Add(-time.Minute), 429, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if v, _ := d.GetString("modelLock_busy-model"); v == "" {
		t.Errorf("an active lock was cleared: %s", got.Data)
	}
	if !d.IsNull("modelLock_old-model") {
		t.Errorf("an expired lock was kept: %s", got.Data)
	}
}

func TestClearLocksForAnEmptyModelKeepsAnActiveAccountLock(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	now := time.Now()
	mustLock(t, s, a.ID, "", now.Add(time.Hour), 503, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetString("modelLock___all"); v == "" {
		t.Fatalf("a success without a model cleared the account lock: %s", got.Data)
	}
	if v, _ := got.Doc().GetString("testStatus"); v != "unavailable" {
		t.Fatalf("testStatus = %q", v)
	}
}

func TestAClearLocksBoundaryLockExpiringExactlyNowIsExpired(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	at := time.Now().UTC().Truncate(time.Millisecond)
	mustLock(t, s, a.ID, "x", at.Add(time.Hour), 429, "r", 1)
	setRaw(t, s, a.ID, map[string]string{
		"modelLock_edge-model":  `"` + dialect.FormatTimestamp(at) + `"`,
		"modelLock_later-model": `"` + dialect.FormatTimestamp(at.Add(time.Millisecond)) + `"`,
	})
	if err := s.Connections().ClearLocks(ctx, a.ID, "other", at, time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if !lockCleared(d, "modelLock_edge-model") {
		t.Errorf("a lock that ends exactly now must count as expired: %s", got.Data)
	}
	if v, _ := d.GetString("modelLock_later-model"); v == "" {
		t.Errorf("a lock one millisecond in the future must stay: %s", got.Data)
	}
}

func TestClearLocksTreatsAnUnreadableLockAsExpired(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	mustLock(t, s, a.ID, "m", time.Now().Add(time.Hour), 429, "r", 1)
	setRaw(t, s, a.ID, map[string]string{"modelLock_weird": `"not a time"`, "modelLock_num": `5`})
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if !d.IsNull("modelLock_weird") || !d.IsNull("modelLock_num") {
		t.Errorf("unreadable locks must not keep the account locked: %s", got.Data)
	}
	if v, _ := d.GetString("testStatus"); v != "active" {
		t.Errorf("testStatus = %q", v)
	}
}

func TestClearLocksWritesNothingWhenThereIsNothingToClear(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	mustLock(t, s, a.ID, "m", time.Now().Add(time.Hour), 429, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	first, _, _ := s.Connections().Get(ctx, a.ID)
	time.Sleep(5 * time.Millisecond)
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	second, _, _ := s.Connections().Get(ctx, a.ID)
	if first.UpdatedAt != second.UpdatedAt || first.Data != second.Data {
		t.Fatalf("a repeated success rewrote a healthy row:\n%s\n%s", first.Data, second.Data)
	}
}

func TestClearLocksResetsAnUnavailableAccountThatHasNoLocksLeft(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	setRaw(t, s, a.ID, map[string]string{
		"testStatus": `"unavailable"`, "lastError": `"stale"`, "errorCode": `500`, "backoffLevel": `4`,
	})
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if v, _ := d.GetString("testStatus"); v != "active" || !d.IsNull("lastError") || !d.IsNull("errorCode") {
		t.Fatalf("a stale error state was kept after a success: %s", got.Data)
	}
	if v, _ := d.GetRaw("backoffLevel"); v != "0" {
		t.Fatalf("backoffLevel = %s", v)
	}
}

func TestClearLocksLeavesAHealthyAccountWithStaleFieldsAlone(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	setRaw(t, s, a.ID, map[string]string{"testStatus": `"active"`, "lastError": `"kept"`})
	before, _, _ := s.Connections().Get(ctx, a.ID)
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.Connections().Get(ctx, a.ID)
	if before.Data != after.Data {
		t.Fatalf("a healthy row was rewritten:\n%s\n%s", before.Data, after.Data)
	}
}

func TestLockFromLevelComputesTheLockInsideTheTransaction(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	base := time.Now().UTC().Truncate(time.Millisecond)
	var seen []int
	decide := func(level int) (time.Time, int, bool) {
		seen = append(seen, level)
		return base.Add(time.Duration(level+1) * time.Minute), level + 1, true
	}
	for range 3 {
		locked, err := s.Connections().LockFromLevel(ctx, a.ID, "m", 429, "r", decide)
		if err != nil || !locked {
			t.Fatalf("%v %v", locked, err)
		}
	}
	if len(seen) != 3 || seen[0] != 0 || seen[1] != 1 || seen[2] != 2 {
		t.Fatalf("the function must see the stored level each time: %v", seen)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	want := dialect.FormatTimestamp(base.Add(3 * time.Minute))
	if v, _ := d.GetString("modelLock_m"); v != want {
		t.Errorf("lock %q, want %q", v, want)
	}
	if v, _ := d.GetInt("backoffLevel"); v != 3 {
		t.Errorf("level %d", v)
	}
	if v, _ := d.GetInt("errorCode"); v != 429 {
		t.Errorf("errorCode %d", v)
	}
	if v, _ := d.GetString("testStatus"); v != "unavailable" {
		t.Errorf("testStatus %q", v)
	}
}

func TestLockFromLevelCanDeclineAndThenWritesNothing(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	before, _, _ := s.Connections().Get(ctx, a.ID)
	locked, err := s.Connections().LockFromLevel(ctx, a.ID, "m", 400, "r", func(int) (time.Time, int, bool) { return time.Time{}, 0, false })
	if err != nil || locked {
		t.Fatalf("%v %v", locked, err)
	}
	after, _, _ := s.Connections().Get(ctx, a.ID)
	if before.Data != after.Data || before.UpdatedAt != after.UpdatedAt {
		t.Fatalf("a declined lock wrote the row:\n%s\n%s", before.Data, after.Data)
	}
}

func TestLockFromLevelFailsOnAMissingRow(t *testing.T) {
	s := newMigrated(t)
	_, err := s.Connections().LockFromLevel(context.Background(), "nope", "m", 500, "r", func(int) (time.Time, int, bool) { return time.Now(), 1, true })
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestLockFromLevelAccountLockAndReasonCut(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	_, err := s.Connections().LockFromLevel(ctx, a.ID, "", 503, strings.Repeat("é", 500), func(int) (time.Time, int, bool) {
		return time.Now().Add(time.Hour), 1, true
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetString("modelLock___all"); v == "" {
		t.Errorf("no account lock: %s", got.Data)
	}
	if v, _ := got.Doc().GetString("lastError"); len([]rune(v)) != 200 {
		t.Errorf("reason has %d characters", len([]rune(v)))
	}
}

func TestLockKeyAndPrefixAreTheStoredFieldNames(t *testing.T) {
	if LockPrefix != "modelLock_" || LockKey("") != "modelLock___all" || LockKey("a/b:c") != "modelLock_a/b:c" {
		t.Fatalf("the lock field names are part of the stored format: %q %q %q", LockPrefix, LockKey(""), LockKey("a/b:c"))
	}
	if StatusExpired != "expired" {
		t.Fatalf("StatusExpired = %q", StatusExpired)
	}
}

func TestTouchUsageAtWritesTheGivenTime(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	at := time.Date(2026, 3, 10, 12, 0, 0, 123_000_000, time.UTC)
	if err := s.Connections().TouchUsageAt(ctx, a.ID, false, at); err != nil {
		t.Fatal(err)
	}
	if err := s.Connections().TouchUsageAt(ctx, a.ID, true, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetString("lastUsedAt"); v != "2026-03-10T12:00:01.123Z" {
		t.Errorf("lastUsedAt %q", v)
	}
	if v, _ := got.Doc().GetInt("consecutiveUseCount"); v != 2 {
		t.Errorf("count %d", v)
	}
	if err := s.Connections().TouchUsageAt(ctx, a.ID, false, at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetInt("consecutiveUseCount"); v != 1 {
		t.Errorf("a non-sticky touch must restart the count, got %d", v)
	}
}

func TestTouchUsageAtFailsOnAMissingRow(t *testing.T) {
	s := newMigrated(t)
	if err := s.Connections().TouchUsageAt(context.Background(), "nope", true, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestALockWriteDropsLocksThatAlreadyEnded(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	now := time.Now()
	mustLock(t, s, a.ID, "old-model", now.Add(-time.Minute), 429, "r", 1)
	mustLock(t, s, a.ID, "cleared-model", now.Add(time.Hour), 429, "r", 1)
	if err := s.Connections().ClearLocks(ctx, a.ID, "cleared-model", now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	mustLock(t, s, a.ID, "live-model", now.Add(time.Hour), 429, "r", 1)
	if _, err := s.Connections().LockFromLevel(ctx, a.ID, "new-model", 429, "r", func(int) (time.Time, int, bool) { return now.Add(time.Minute), 1, true }); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	for _, k := range []string{"modelLock_old-model", "modelLock_cleared-model"} {
		if d.Has(k) {
			t.Errorf("%s should be gone: %s", k, got.Data)
		}
	}
	for _, k := range []string{"modelLock_live-model", "modelLock_new-model"} {
		if v, _ := d.GetString(k); v == "" {
			t.Errorf("%s must stay: %s", k, got.Data)
		}
	}
}

func TestClearLocksSkipsAnAccountThatFailedAfterTheRequestStarted(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	started := time.Now().Add(-time.Minute)
	if err := lockAt(s, a.ID, "m", time.Now().Add(time.Hour), 429, "r", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), started); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if v, _ := d.GetString("modelLock_m"); v == "" {
		t.Fatalf("a failure newer than the request must keep its lock: %s", got.Data)
	}
	if v, _ := d.GetInt("backoffLevel"); v != 1 {
		t.Fatalf("backoff level reset by a success that started earlier: %d", v)
	}
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Connections().Get(ctx, a.ID)
	if !lockCleared(got.Doc(), "modelLock_m") {
		t.Fatalf("a failure older than the request is cleared: %s", got.Data)
	}
}

func TestSwapCredentialsWritesOnlyWhileTheRefreshTokenIsUnchanged(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c := mkConn(t, s, ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@x.io",
		Fields: map[string]string{"accessToken": `"at1"`, "refreshToken": `"rt1"`, "expiresAt": `"2026-01-01T00:00:00.000Z"`}})
	repo := s.Connections()

	got, swapped, err := repo.SwapCredentials(ctx, c.ID, "rt1", map[string]string{"accessToken": `"at2"`, "refreshToken": `"rt2"`, "testStatus": `"active"`})
	if err != nil || !swapped {
		t.Fatalf("swap: swapped %v, err %v", swapped, err)
	}
	if v, _ := got.Doc().GetString("accessToken"); v != "at2" {
		t.Fatalf("access token = %q", v)
	}

	got, swapped, err = repo.SwapCredentials(ctx, c.ID, "rt1", map[string]string{"accessToken": `"stale"`, "refreshToken": `"stale"`})
	if err != nil || swapped {
		t.Fatalf("a stale caller must not write: swapped %v, err %v", swapped, err)
	}
	if v, _ := got.Doc().GetString("refreshToken"); v != "rt2" {
		t.Fatalf("the current row must come back, got refresh token %q", v)
	}
	again, _, _ := repo.Get(ctx, c.ID)
	if v, _ := again.Doc().GetString("accessToken"); v != "at2" {
		t.Fatalf("stored access token changed to %q", v)
	}
}

func TestSwapCredentialsLetsExactlyOneConcurrentCallerWin(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c := mkConn(t, s, ConnInput{Provider: "claude", AuthType: "oauth", Email: "b@x.io",
		Fields: map[string]string{"accessToken": `"a"`, "refreshToken": `"rt1"`}})
	var mu sync.Mutex
	wins := 0
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, swapped, err := s.Connections().SwapCredentials(ctx, c.ID, "rt1", map[string]string{"refreshToken": `"next` + strconv.Itoa(i) + `"`})
			if err != nil {
				t.Errorf("swap: %v", err)
			}
			if swapped {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d callers swapped, want 1", wins)
	}
}

func TestSwapCredentialsFailsOnAMissingRow(t *testing.T) {
	s := newMigrated(t)
	if _, _, err := s.Connections().SwapCredentials(context.Background(), "nope", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestConnectionsCountIncludesInactiveAndCorruptRows(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if n, err := s.Connections().Count(ctx); err != nil || n != 0 {
		t.Fatalf("empty: %d %v", n, err)
	}
	a := mkConn(t, s, ConnInput{Provider: "a", AuthType: "apikey", Name: "1"})
	no := false
	if _, err := s.Connections().Update(ctx, a.ID, ConnPatch{IsActive: &no}); err != nil {
		t.Fatal(err)
	}
	b := mkConn(t, s, ConnInput{Provider: "b", AuthType: "apikey", Name: "2"})
	s.exec(t, `UPDATE "providerConnections" SET "data"='{broken' WHERE "id"=?`, b.ID)
	if n, _ := s.Connections().Count(ctx); n != 2 {
		t.Fatalf("count = %d: an account that is switched off or unreadable is still an account the operator has set up", n)
	}
}

func TestCodexLoginsWithoutAWorkspaceNeverCollapse(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "w@x.io"})
	b := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "w@x.io"})
	if a.ID == b.ID {
		t.Fatal("two codex logins that name no workspace must stay two accounts")
	}
}

func TestAWorkspaceOnOneSideOnlyKeepsAccountsApartForEveryProvider(t *testing.T) {
	s := newMigrated(t)
	with := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "q@x.io", PSD: `{"chatgptAccountId":"w1"}`})
	without := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "q@x.io"})
	if with.ID == without.ID {
		t.Fatal("a workspace id on one side only must not collapse the accounts")
	}
	reverse := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "r@x.io"})
	other := mkConn(t, s, ConnInput{Provider: "gitlab", AuthType: "oauth", Email: "r@x.io", PSD: `{"chatgptAccountId":"w2"}`})
	if reverse.ID == other.ID {
		t.Fatal("a workspace id on the incoming side only must not collapse the accounts")
	}
}

func TestANewConnectionTakesTheNextPriorityAfterTheHighestOne(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "a"})
	s.exec(t, `UPDATE "providerConnections" SET "priority"=7 WHERE "id"=?`, a.ID)
	b := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "b"})
	if b.Priority != 8 {
		t.Fatalf("priority = %d, want 8: the next number after the highest, not after the count", b.Priority)
	}
}

func TestANullFieldIsNotStoredOnANewConnection(t *testing.T) {
	s := newMigrated(t)
	c := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "a", Fields: map[string]string{"apiKey": `null`, "displayName": `"D"`}})
	d := mustDoc(t, c.Data)
	if d.Has("apiKey") {
		t.Fatalf("a null field was stored: %s", c.Data)
	}
	if v, _ := d.GetString("displayName"); v != "D" {
		t.Fatalf("displayName lost: %s", c.Data)
	}
}

func TestACorruptRowIsNeverTheTargetOfADedupHit(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	bad := mkConn(t, s, ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@x.io"})
	s.exec(t, `UPDATE "providerConnections" SET "data"='{broken' WHERE "id"=?`, bad.ID)
	fresh, err := s.Connections().Create(ctx, ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@x.io", Fields: map[string]string{"accessToken": `"t"`}}, nil)
	if err != nil {
		t.Fatalf("a login next to a corrupt row must still work: %v", err)
	}
	if fresh.ID == bad.ID {
		t.Fatal("the login was merged into a corrupt row")
	}
	if raw := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id"=?`, bad.ID); raw != "{broken" {
		t.Fatalf("the corrupt row was rewritten: %q", raw)
	}
}

func TestTheReasonIsCutAtExactly200Characters(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	for n, want := range map[int]int{199: 199, 200: 200, 201: 200} {
		mustLock(t, s, a.ID, "m", time.Now().Add(time.Minute), 500, strings.Repeat("a", n), 1)
		got, _, _ := s.Connections().Get(ctx, a.ID)
		if v, _ := got.Doc().GetString("lastError"); len(v) != want {
			t.Errorf("a reason of %d characters is stored with %d, want %d", n, len(v), want)
		}
	}
}

func TestDropEndedLocksTreatsALockEndingNowAsEnded(t *testing.T) {
	at := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	d := mustDoc(t, `{"modelLock_now":"`+dialect.FormatTimestamp(at)+`","modelLock_past":"`+dialect.FormatTimestamp(at.Add(-time.Millisecond))+`","modelLock_next":"`+dialect.FormatTimestamp(at.Add(time.Millisecond))+`","modelLock_null":null,"keep":1}`)
	if err := dropEndedLocks(&d, at); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"modelLock_now", "modelLock_past", "modelLock_null"} {
		if d.Has(gone) {
			t.Errorf("%s should have been dropped: %s", gone, d.String())
		}
	}
	for _, kept := range []string{"modelLock_next", "keep"} {
		if !d.Has(kept) {
			t.Errorf("%s must stay: %s", kept, d.String())
		}
	}
}

func TestAFailureExactlyAtTheStartOfTheRequestDoesNotBlockItsSuccess(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	since := time.Now().UTC().Truncate(time.Millisecond)
	setRaw(t, s, a.ID, map[string]string{
		"testStatus": `"unavailable"`, "lastErrorAt": `"` + dialect.FormatTimestamp(since) + `"`, "modelLock_m": `"` + dialect.FormatTimestamp(since.Add(-time.Hour)) + `"`,
	})
	if err := s.Connections().ClearLocks(ctx, a.ID, "m", time.Now(), since); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetString("testStatus"); v != "active" {
		t.Fatalf("a failure recorded at the same instant the request began is not newer than it: testStatus=%q", v)
	}
}

func TestRecordTestWritesTheOutcomeAndLeavesTheCooldownAlone(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	until := time.Now().Add(time.Hour)
	mustLock(t, s, a.ID, "m", until, 429, "slow down", 3)
	before, _, _ := s.Connections().Get(ctx, a.ID)
	lockBefore, _ := before.Doc().GetString("modelLock_m")
	at := time.Now().UTC().Truncate(time.Millisecond)

	if err := s.Connections().RecordTest(ctx, a.ID, false, "Invalid API key", at); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	d := got.Doc()
	if v, _ := d.GetString("testStatus"); v != "error" {
		t.Errorf("testStatus %q", v)
	}
	if v, _ := d.GetString("lastError"); v != "Invalid API key" {
		t.Errorf("lastError %q", v)
	}
	if v, _ := d.GetString("lastErrorAt"); v != dialect.FormatTimestamp(at) {
		t.Errorf("lastErrorAt %q", v)
	}
	if v, _ := d.GetString("lastTested"); v != dialect.FormatTimestamp(at) {
		t.Errorf("lastTested %q", v)
	}
	for field, want := range map[string]string{"modelLock_m": lockBefore} {
		if v, _ := d.GetString(field); v != want {
			t.Errorf("%s changed to %q", field, v)
		}
	}
	if lvl, _ := d.GetInt("backoffLevel"); lvl != 3 {
		t.Errorf("backoffLevel %d: a check changed the backoff", lvl)
	}
	if code, _ := d.GetInt("errorCode"); code != 429 {
		t.Errorf("errorCode %d: a check overwrote the real error code", code)
	}

	if err := s.Connections().RecordTest(ctx, a.ID, true, "", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Connections().Get(ctx, a.ID)
	d = got.Doc()
	if v, _ := d.GetString("testStatus"); v != "active" {
		t.Errorf("after a pass: testStatus %q", v)
	}
	if !d.IsNull("lastError") || !d.IsNull("lastErrorAt") {
		t.Errorf("a pass left an error: %s", got.Data)
	}
	if v, _ := d.GetString("modelLock_m"); v != lockBefore {
		t.Errorf("a pass erased a real cooldown: %q", v)
	}
	if lvl, _ := d.GetInt("backoffLevel"); lvl != 3 {
		t.Errorf("a pass reset the backoff to %d", lvl)
	}
}

func TestRecordTestKeepsAnExpiredConnectionExpired(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x", Fields: map[string]string{"testStatus": `"expired"`}})
	for _, ok := range []bool{false, true} {
		if err := s.Connections().RecordTest(ctx, a.ID, ok, "Invalid API key", time.Now()); err != nil {
			t.Fatal(err)
		}
		got, _, _ := s.Connections().Get(ctx, a.ID)
		if v, _ := got.Doc().GetString("testStatus"); v != StatusExpired {
			t.Fatalf("ok=%v turned an expired connection into %q", ok, v)
		}
	}
}

func TestRecordTestCutsALongMessageAndRefusesBadRows(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "x"})
	if err := s.Connections().RecordTest(ctx, a.ID, false, strings.Repeat("é", 500), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if v, _ := got.Doc().GetString("lastError"); len([]rune(v)) != 200 {
		t.Errorf("lastError has %d characters", len([]rune(v)))
	}
	if err := s.Connections().RecordTest(ctx, "no-such-id", true, "", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing row: %v", err)
	}
	b := mkConn(t, s, ConnInput{Provider: "p", AuthType: "apikey", Name: "bad"})
	s.exec(t, `UPDATE "providerConnections" SET "data"='{"a":1,"a":2}' WHERE "id"=?`, b.ID)
	if err := s.Connections().RecordTest(ctx, b.ID, true, "", time.Now()); !errors.Is(err, document.ErrCorruptRow) {
		t.Errorf("a corrupt row: %v", err)
	}
	if raw := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id"=?`, b.ID); raw != `{"a":1,"a":2}` {
		t.Errorf("the corrupt row was rewritten: %s", raw)
	}
}

func TestMarkExpiredWritesOnlyWhileTheTokenIsTheOneThatDied(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c := mkConn(t, s, ConnInput{Provider: "claude", AuthType: "oauth", Email: "e@x.io",
		Fields: map[string]string{"accessToken": `"a"`, "refreshToken": `"rt1"`, "testStatus": `"active"`}})
	at := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

	marked, err := s.Connections().MarkExpired(ctx, c.ID, "other", "needs to be reconnected", at)
	if err != nil || marked {
		t.Fatalf("a stale token was marked: %v %v", marked, err)
	}
	got, _, _ := s.Connections().Get(ctx, c.ID)
	if st, _ := got.Doc().GetString("testStatus"); st != "active" {
		t.Fatalf("status = %q", st)
	}

	marked, err = s.Connections().MarkExpired(ctx, c.ID, "rt1", "needs to be reconnected", at)
	if err != nil || !marked {
		t.Fatalf("marked %v, err %v", marked, err)
	}
	got, _, _ = s.Connections().Get(ctx, c.ID)
	d := got.Doc()
	if st, _ := d.GetString("testStatus"); st != StatusExpired {
		t.Fatalf("status = %q", st)
	}
	if msg, _ := d.GetString("lastError"); msg != "needs to be reconnected" {
		t.Fatalf("lastError = %q", msg)
	}
	if when, _ := d.GetString("lastErrorAt"); when != dialect.FormatTimestamp(at) {
		t.Fatalf("lastErrorAt = %q", when)
	}
	if tok, _ := d.GetString("refreshToken"); tok != "rt1" {
		t.Fatalf("the refresh token changed: %q", tok)
	}
}

func TestAMarkNeverLandsOnARotatedTokenWhateverTheOrder(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c := mkConn(t, s, ConnInput{Provider: "codex", AuthType: "oauth", Email: "f@x.io",
		Fields: map[string]string{"accessToken": `"a"`, "refreshToken": `"rt1"`}})
	var wg sync.WaitGroup
	var mu sync.Mutex
	swaps := 0
	for i := range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if i%2 == 0 {
				_, won, err := s.Connections().SwapCredentials(ctx, c.ID, "rt1", map[string]string{"refreshToken": `"next` + strconv.Itoa(i) + `"`})
				if err != nil {
					t.Errorf("swap %d: %v", i, err)
				}
				if won {
					mu.Lock()
					swaps++
					mu.Unlock()
				}
				return
			}
			if _, err := s.Connections().MarkExpired(ctx, c.ID, "rt1", "needs to be reconnected", time.Now()); err != nil {
				t.Errorf("mark %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if swaps != 1 {
		t.Fatalf("%d swaps won, want exactly 1: one spent token is rotated once", swaps)
	}
	got, _, _ := s.Connections().Get(ctx, c.ID)
	if rt, _ := got.Doc().GetString("refreshToken"); !strings.HasPrefix(rt, "next") {
		t.Fatalf("the refresh token is %q", rt)
	}
	// Once the token is rotated a mark for the old one writes nothing, so it
	// cannot declare the new token dead.
	again, _ := s.Connections().MarkExpired(ctx, c.ID, "rt1", "x", time.Now())
	if again {
		t.Fatal("a mark landed on a rotated token")
	}
}

func TestMarkExpiredFailsOnAMissingRow(t *testing.T) {
	s := newMigrated(t)
	if _, err := s.Connections().MarkExpired(context.Background(), "nope", "", "m", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
