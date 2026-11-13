package fetch

import (
	"bytes"
	"testing"

	"golang.org/x/net/html"
)

// FuzzConvert holds the walker to its bounds: any document parses, and what it
// writes stays inside the output cap.
func FuzzConvert(f *testing.F) {
	f.Add([]byte("<html><body><p>hi</p></body></html>"))
	f.Add([]byte("<script>never()</script><h1>x"))
	f.Add([]byte("<blockquote><ul><li><pre><code>c</code></pre></li></ul></blockquote>"))
	f.Add([]byte("<a href=\"javascript:x\">t</a><img src=\"data:x\">"))
	f.Fuzz(func(t *testing.T, data []byte) {
		node, err := html.Parse(bytes.NewReader(data))
		if err != nil {
			t.Skip()
		}
		for _, markup := range []bool{true, false} {
			out := convert(node, nil, markup)
			if len(out) > maxOutput {
				t.Fatalf("%d bytes", len(out))
			}
		}
	})
}
