package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var docName = regexp.MustCompile(`rllmx_[a-z_]+`)

// docMetric reduces a written sample name to its family: the suffixes a scrape
// appends, but not the name itself.
func docMetric(name string) string {
	for _, suffix := range []string{"_bucket", "_sum", "_count", "_total", "_info"} {
		if trimmed, ok := strings.CutSuffix(name, suffix); ok {
			return trimmed
		}
	}
	return name
}

func TestTheOperatorGuideAndTheRegistryNameTheSameFamilies(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "METRICS.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, family := range Families {
		if !strings.Contains(doc, family) {
			t.Errorf("%s is written by the registry but not documented", family)
		}
	}
	for _, name := range docName.FindAllString(doc, -1) {
		if family := docMetric(name); !slices.Contains(Families, family) {
			t.Errorf("the guide names %q, which the registry does not write", name)
		}
	}
}
