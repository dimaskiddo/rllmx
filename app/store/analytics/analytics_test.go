package analytics

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenCreatesTheSchema(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, filepath.Join(dir, "db", "analytics.sqlite"))
	for _, tb := range []string{"requests", "rollupHourly", "rollupDaily", "meta"} {
		var n []int64
		err := s.db.Select(&n, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, tb)
		if err != nil {
			t.Fatal(err)
		}
		if n[0] != 1 {
			t.Errorf("table %s missing", tb)
		}
	}
	for _, ix := range []string{"idx_req_ts", "idx_req_key_ts", "idx_req_conn_ts", "idx_req_model_ts"} {
		var n []int64
		if err := s.db.Select(&n, `SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, ix); err != nil {
			t.Fatal(err)
		}
		if n[0] != 1 {
			t.Errorf("index %s missing", ix)
		}
	}
	if mode := autoVacuum(t, s); mode != autoVacuumOn {
		t.Errorf("auto_vacuum = %d, want %d (incremental)", mode, autoVacuumOn)
	}
	info, err := os.Stat(filepath.Join(dir, "db", "analytics.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", info.Mode().Perm())
	}
}

func autoVacuum(t testing.TB, s *testStore) int64 {
	t.Helper()
	var mode []int64
	if err := s.db.Select(&mode, `PRAGMA auto_vacuum`); err != nil {
		t.Fatal(err)
	}
	if len(mode) == 0 {
		return -1
	}
	return mode[0]
}

func TestReopenKeepsDataAndAutoVacuum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db", "analytics.sqlite")
	s := openAt(t, path)
	w := s.NewWriter(WriterOptions{})
	w.Enqueue(Record{Provider: "p", Model: "m"})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s2 := openAt(t, path)
	if got := count(t, s2, "requests"); got != 1 {
		t.Fatalf("requests after reopen = %d", got)
	}
	if mode := autoVacuum(t, s2); mode != autoVacuumOn {
		t.Errorf("auto_vacuum after reopen = %d", mode)
	}
}

// A new string field in Record must be added to this list on purpose: it is the
// review point for "no bodies, headers or prompt text".
var allowedStrings = map[string]bool{
	"RequestID": true, "APIKeyID": true, "APIKeyName": true, "ConnectionID": true,
	"Provider": true, "Model": true, "Endpoint": true, "SourceFormat": true,
	"ErrorClass": true, "ComboName": true,
}

func TestRecordHasNoFreeTextFields(t *testing.T) {
	typ := reflect.TypeOf(Record{})
	for i := range typ.NumField() {
		f := typ.Field(i)
		switch f.Type.Kind() {
		case reflect.String:
			if !allowedStrings[f.Name] {
				t.Errorf("Record.%s is a string outside the allow-list", f.Name)
			}
		case reflect.Int, reflect.Int64, reflect.Bool, reflect.Float64:
		case reflect.Struct:
			if f.Type != reflect.TypeOf(time.Time{}) {
				t.Errorf("Record.%s has struct type %s", f.Name, f.Type)
			}
		default:
			t.Errorf("Record.%s has kind %s, which could carry a body", f.Name, f.Type.Kind())
		}
	}
	for name := range allowedStrings {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("allow-list names a field that does not exist: %s", name)
		}
	}
}

func TestSanitizeRecord(t *testing.T) {
	r := sanitize(Record{
		APIKeyName: "  na\x00me\n\t" + strings.Repeat("é", 100),
		Model:      strings.Repeat("m", 300),
		Endpoint:   "/v1/chat\r\nInjected: header",
		ErrorClass: "free text from upstream: your prompt was ...",
		Status:     500,
	})
	if n := len([]rune(r.APIKeyName)); n != maxKeyName {
		t.Errorf("APIKeyName has %d runes, want %d", n, maxKeyName)
	}
	for _, c := range r.APIKeyName + r.Endpoint {
		if c < 0x20 || c == 0x7f {
			t.Errorf("control character %q survived", c)
		}
	}
	if n := len([]rune(r.Model)); n != maxModel {
		t.Errorf("Model has %d runes, want %d", n, maxModel)
	}
	if r.ErrorClass != "unknown" {
		t.Errorf("ErrorClass = %q, want unknown", r.ErrorClass)
	}
	if got := sanitize(Record{Status: 200}).ErrorClass; got != "none" {
		t.Errorf("empty class on success = %q, want none", got)
	}
	if got := sanitize(Record{Status: 502}).ErrorClass; got != "unknown" {
		t.Errorf("empty class on failure = %q, want unknown", got)
	}
	for class := range errorClasses {
		if got := sanitize(Record{ErrorClass: class}).ErrorClass; got != class {
			t.Errorf("known class %q rewritten to %q", class, got)
		}
	}
	if sanitize(Record{}).Time.IsZero() {
		t.Error("zero time must default to now")
	}
}

func TestNoStoredValueLooksLikeAKeyOrBody(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	w.Enqueue(Record{APIKeyID: "id-1", APIKeyName: "ci", Provider: "openai", Model: "gpt", Endpoint: "/v1/chat/completions", Status: 200})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		RequestID    string `db:"requestId"`
		APIKeyID     string `db:"apiKeyId"`
		APIKeyName   string `db:"apiKeyName"`
		ConnectionID string `db:"connectionId"`
		Provider     string `db:"provider"`
		Model        string `db:"model"`
		Endpoint     string `db:"endpoint"`
		SourceFormat string `db:"sourceFormat"`
		ErrorClass   string `db:"errorClass"`
		ComboName    string `db:"comboName"`
	}
	if err := s.db.Select(&rows, `SELECT "requestId","apiKeyId","apiKeyName","connectionId","provider","model","endpoint","sourceFormat","errorClass","comboName" FROM "requests"`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		for _, v := range []string{r.RequestID, r.APIKeyID, r.APIKeyName, r.ConnectionID, r.Provider, r.Model, r.Endpoint, r.SourceFormat, r.ErrorClass, r.ComboName} {
			if strings.HasPrefix(v, "sk-") {
				t.Errorf("a stored value starts with sk-: %q", v)
			}
		}
	}
}

func TestSanitizeClampsNegativeAndNonFiniteNumbers(t *testing.T) {
	r := sanitize(Record{PromptTokens: -1, CompletionTokens: -2, CachedTokens: -3, CacheWriteTokens: -4, ReasoningTokens: -5, LatencyMs: -6, TTFBMs: -7, Attempt: -8, Cost: -0.5})
	if r.PromptTokens != 0 || r.CompletionTokens != 0 || r.CachedTokens != 0 || r.CacheWriteTokens != 0 ||
		r.ReasoningTokens != 0 || r.LatencyMs != 0 || r.TTFBMs != 0 || r.Attempt != 0 || r.Cost != 0 {
		t.Fatalf("negative values survived: %+v", r)
	}
	for _, c := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := sanitize(Record{Cost: c}).Cost; got != 0 {
			t.Errorf("cost %v stored as %v", c, got)
		}
	}
}

func TestOpenRefusesToRunWithoutItsCollaborators(t *testing.T) {
	if _, err := Open(context.Background(), Options{}); err == nil {
		t.Fatal("Open without DB, Schema and Log succeeded")
	}
}

func TestTheConfiguredSchemaWins(t *testing.T) {
	if got := (Options{}).schemaName(); got != SchemaName {
		t.Fatalf("an unset schema must fall back to %s, got %s", SchemaName, got)
	}
	if got := (Options{PGSchema: "rllmx_test_7"}).schemaName(); got != "rllmx_test_7" {
		t.Fatalf("a configured schema was ignored: %s", got)
	}
}
