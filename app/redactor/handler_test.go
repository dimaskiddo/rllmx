package redactor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goravel/framework/contracts/log"

	"rllmx/tests"
)

func newEntry(over func(*tests.Entry)) tests.Entry {
	e := tests.Entry{LevelV: log.LevelInfo, MessageV: "m"}
	if over != nil {
		over(&e)
	}
	return e
}

func handle(t *testing.T, e tests.Entry) log.Entry {
	t.Helper()
	inner := &tests.Handler{}
	if err := NewHandler(inner).Handle(e); err != nil {
		t.Fatal(err)
	}
	if len(inner.Entries) != 1 {
		t.Fatalf("the inner handler got %d entries", len(inner.Entries))
	}
	return inner.Entries[0]
}

func TestHandlerScrubsTheMessage(t *testing.T) {
	got := handle(t, newEntry(func(e *tests.Entry) { e.MessageV = "hello sk-abcdefghij world" }))
	if strings.Contains(got.Message(), "sk-abcdefghij") || !strings.Contains(got.Message(), "hello") || !strings.Contains(got.Message(), "world") {
		t.Fatalf("message %q", got.Message())
	}
}

func TestHandlerScrubsEveryFieldThatCanCarryText(t *testing.T) {
	secret := "sk-abcdefghijkl"
	got := handle(t, newEntry(func(e *tests.Entry) {
		e.CodeV = "code " + secret
		e.DomainV = "domain " + secret
		e.HintV = "hint " + secret
		e.TagsV = []string{"ok", "tag " + secret}
		e.OwnerV = "owner " + secret
		e.UserV = "user " + secret
		e.RequestV = map[string]any{"header": "Bearer abc.def", "path": "/x?key=K1"}
		e.ResponseV = map[string]any{"body": secret}
		e.TraceV = map[string]any{"frame": "at " + secret}
		e.WithV = map[string]any{"password": "pw-1", "note": secret, "count": 3}
	}))
	var all []string
	all = append(all, got.Code(), got.Domain(), got.Hint(), got.Message())
	all = append(all, got.Tags()...)
	all = append(all, flatten(got.Owner())...)
	all = append(all, flatten(got.User())...)
	all = append(all, flatten(got.Request())...)
	all = append(all, flatten(got.Response())...)
	all = append(all, flatten(got.Trace())...)
	all = append(all, flatten(got.With())...)
	text := strings.Join(all, "|")
	for _, leak := range []string{secret, "abc.def", "K1", "pw-1"} {
		if strings.Contains(text, leak) {
			t.Errorf("%q leaked: %s", leak, text)
		}
	}
	for _, keep := range []string{"ok", "/x", "count", "3"} {
		if !strings.Contains(text, keep) {
			t.Errorf("%q was lost: %s", keep, text)
		}
	}
}

func TestHandlerScrubsDataAndTheContextValues(t *testing.T) {
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "sk-abcdefghijkl")
	got := handle(t, newEntry(func(e *tests.Entry) {
		e.Ctx = ctx
		e.DataV = log.Data{"authorization": "Bearer abc.def", "n": 1}
	}))
	for k, v := range got.Data() {
		if strings.Contains(strings.Join(flatten(v), ""), "abc.def") {
			t.Errorf("data %s leaked", k)
		}
	}
	if got.Context() != nil && got.Context().Value(ctxKey{}) != nil {
		t.Fatalf("a context value reached the file: %v", got.Context().Value(ctxKey{}))
	}
}

func TestHandlerKeepsLevelTimeAndPassesNilFieldsAsNil(t *testing.T) {
	got := handle(t, newEntry(func(e *tests.Entry) { e.LevelV = log.LevelWarning }))
	if got.Level() != log.LevelWarning {
		t.Errorf("level %v", got.Level())
	}
	if got.Request() != nil || got.Response() != nil || got.Trace() != nil || got.With() != nil || got.Owner() != nil || got.User() != nil {
		t.Errorf("empty fields must stay empty: %#v", got)
	}
}

func TestHandlerFollowsTheInnerHandlersLevel(t *testing.T) {
	inner := &tests.Handler{Min: log.LevelWarning}
	h := NewHandler(inner)
	if h.Enabled(log.LevelInfo) || !h.Enabled(log.LevelError) {
		t.Fatal("Enabled must ask the inner handler")
	}
}

func TestHandlerReturnsTheInnerError(t *testing.T) {
	boom := errors.New("disk full")
	inner := &tests.Handler{Err: boom}
	if err := NewHandler(inner).Handle(newEntry(nil)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestHandlerDoesNotChangeTheCallersEntry(t *testing.T) {
	with := map[string]any{"password": "pw-1"}
	e := newEntry(func(e *tests.Entry) { e.WithV = with; e.MessageV = "sk-abcdefghijkl" })
	handle(t, e)
	if with["password"] != "pw-1" || e.MessageV != "sk-abcdefghijkl" {
		t.Fatal("the caller's entry was rewritten in place: another handler would see the change")
	}
}
