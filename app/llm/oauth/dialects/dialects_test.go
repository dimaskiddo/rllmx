package dialects

import (
	"slices"
	"testing"

	"rllmx/app/llm/upstream"
)

func TestTheTablesMatchTheNamesADataFileMayUse(t *testing.T) {
	d := Default()
	var have []string
	for n := range d.Hooks {
		have = append(have, n)
	}
	want := upstream.HookNames()
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		t.Fatalf("hooks %v, schema %v", have, want)
	}
	if len(d.Device) != len(upstream.DialectNames()) {
		t.Errorf("%d dialects in code, %d in the schema", len(d.Device), len(upstream.DialectNames()))
	}
	for _, n := range upstream.CodeDialectNames() {
		if _, ok := d.Code[n]; !ok {
			t.Errorf("code dialect %s is missing", n)
		}
	}
	if len(d.Code) != len(upstream.CodeDialectNames()) {
		t.Errorf("%d code dialects in code, %d in the schema", len(d.Code), len(upstream.CodeDialectNames()))
	}
	for _, n := range upstream.RefreshHookNames() {
		if _, ok := d.Refresh[n]; !ok {
			t.Errorf("refresh hook %s is missing", n)
		}
	}
}

func TestEveryRefreshAndDialectNameIsInItsTable(t *testing.T) {
	d := Default()
	for _, n := range upstream.RefreshHookNames() {
		if _, ok := d.Refresh[n]; !ok {
			t.Errorf("refresh hook %s is missing", n)
		}
	}
}
