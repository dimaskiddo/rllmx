package quota

import "testing"

func TestCodexTitlesItsWindowNames(t *testing.T) {
	out := parseCodex([]byte(`{"rate_limit":{"primary_window":{"used_percent":10},"secondary_window":{"used_percent":20}},` +
		`"code_review_rate_limit":{"rate_limit":{"primary_window":{"used_percent":5}}}}`))
	for key, name := range map[string]string{"session": "Session", "weekly": "Weekly", "review session": "Review session"} {
		q, ok := out.Quotas[key]
		if !ok {
			t.Errorf("no %q row: %+v", key, out.Quotas)
			continue
		}
		if q.DisplayName != name {
			t.Errorf("%q named %q, want %q", key, q.DisplayName, name)
		}
	}
}
