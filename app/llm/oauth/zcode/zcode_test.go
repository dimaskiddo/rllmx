package zcode

import (
	"testing"

	"github.com/tidwall/gjson"
)

func FuzzZCodeLocation(f *testing.F) {
	for _, s := range []string{`{}`, `{"organizations":[]}`, `{"organizations":[{"organizationId":"o","projects":[{"projectId":"p"}]}]}`, `[`, `{"organizations":[{"organizationId":"../","projects":[{"projectId":"p"}]}]}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		org, project, ok := zcodeLocation(gjson.ParseBytes(raw))
		if ok && (!zcodeID.MatchString(org) || !zcodeID.MatchString(project)) {
			t.Fatalf("accepted %q %q", org, project)
		}
	})
}
