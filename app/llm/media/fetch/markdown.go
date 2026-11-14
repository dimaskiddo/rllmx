package fetch

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// Bounds on one page: a depth a document cannot nest past, nodes a walk visits,
// and bytes it writes.
const (
	maxDepth  = 512
	maxNodes  = 200000
	maxOutput = 1 << 20
)

// skip are the elements whose text is not the page: scripts, styles and framing.
var skip = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "iframe": true, "head": true, "object": true, "embed": true,
}

// convert walks the parsed page and writes it as markdown or as plain text.
func convert(root *html.Node, base *url.URL, markup bool) string {
	w := &writer{base: base, markup: markup}
	w.walk(root, 0)
	return tidy(w.b.String())
}

// writer holds the page being written and the state a block needs: how deep a
// quote is, and where each open list stands.
type writer struct {
	b      strings.Builder
	base   *url.URL
	markup bool
	nodes  int
	quotes int
	pre    bool
	lists  []int
}

func (w *writer) walk(n *html.Node, depth int) {
	if depth > maxDepth || w.b.Len() >= maxOutput {
		return
	}
	w.nodes++
	if w.nodes > maxNodes {
		return
	}
	switch n.Type {
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			w.walk(c, depth+1)
		}
	case html.TextNode:
		w.inline(n.Data)
	case html.ElementNode:
		if !skip[n.Data] {
			w.element(n, depth)
		}
	}
}

func (w *writer) element(n *html.Node, depth int) {
	if w.leaf(n) {
		return
	}
	f := w.frame(n)
	defer f.undo()
	if f.block {
		w.block()
	}
	w.raw(f.before)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, depth+1)
	}
	w.raw(f.after)
	if f.block {
		w.block()
	}
}

// leaf writes an element that has no children to walk and says it did.
func (w *writer) leaf(n *html.Node) bool {
	switch n.Data {
	case "br":
		w.newline()
	case "hr":
		w.block()
		w.raw("---")
		w.block()
	case "img":
		if src := w.safeURL(attr(n, "src")); src != "" && w.markup {
			w.raw("![" + attr(n, "alt") + "](" + src + ")")
		}
	default:
		return false
	}
	return true
}

// frame is what an element writes around its children, and how to undo the
// state it set while they are written.
type frame struct {
	before, after string
	block         bool
	undo          func()
}

// frame decides what surrounds an element's children.
func (w *writer) frame(n *html.Node) frame {
	f := frame{undo: func() {}}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		f.block = true
		if w.markup {
			f.before = strings.Repeat("#", int(n.Data[1]-'0')) + " "
		}
	case "p", "div", "section", "article", "header", "footer", "main", "aside", "figure", "figcaption", "form", "table", "tr":
		f.block = true
	case "blockquote":
		f.block = true
		if w.markup {
			w.quotes++
			f.undo = func() { w.quotes-- }
		}
	case "ul", "ol":
		f.block = true
		depth := 0
		if n.Data == "ul" {
			depth = -1
		}
		w.lists = append(w.lists, depth)
		f.undo = func() { w.lists = w.lists[:len(w.lists)-1] }
	case "li":
		w.newline()
		if len(w.lists) > 0 && w.lists[len(w.lists)-1] >= 0 {
			w.lists[len(w.lists)-1]++
			f.before = strconv.Itoa(w.lists[len(w.lists)-1]) + ". "
		} else {
			f.before = "- "
		}
	case "pre":
		f.block = true
		if w.markup {
			f.before, f.after = "```\n", "\n```"
			w.pre = true
			f.undo = func() { w.pre = false }
		}
	case "td", "th":
		if w.markup {
			f.before, f.after = " ", " |"
		}
	case "strong", "b":
		if w.markup {
			f.before, f.after = "**", "**"
		}
	case "em", "i":
		if w.markup {
			f.before, f.after = "*", "*"
		}
	case "code":
		if w.markup && !w.pre {
			f.before, f.after = "`", "`"
		}
	case "a":
		if w.markup {
			if href := w.safeURL(attr(n, "href")); href != "" {
				f.before, f.after = "[", "]("+href+")"
			}
		}
	}
	return f
}

// inline writes a text node with its runs of whitespace collapsed and the space
// it had on either side kept, so a word beside a link keeps them apart.
func (w *writer) inline(raw string) {
	s := strings.Join(strings.Fields(raw), " ")
	if s == "" {
		return
	}
	if first, _ := utf8.DecodeRuneInString(raw); unicode.IsSpace(first) && w.last() != 0 && w.last() != ' ' && w.last() != '\n' {
		w.raw(" ")
	}
	w.raw(s)
	if last, _ := utf8.DecodeLastRuneInString(raw); unicode.IsSpace(last) {
		w.raw(" ")
	}
}

func (w *writer) last() byte {
	s := w.b.String()
	if s == "" {
		return 0
	}
	return s[len(s)-1]
}

// raw appends text under the output bound, marking a quoted line as it starts.
func (w *writer) raw(s string) {
	if s == "" {
		return
	}
	if w.quotes > 0 && (w.b.Len() == 0 || w.last() == '\n') && s[0] != '\n' {
		w.b.WriteString("> ")
	}
	room := maxOutput - w.b.Len()
	if room <= 0 {
		return
	}
	if len(s) > room {
		s = s[:room]
	}
	w.b.WriteString(s)
}

// newline writes one line break.
func (w *writer) newline() {
	w.raw("\n")
}

// block separates two block elements.
func (w *writer) block() {
	if w.b.Len() == 0 {
		return
	}
	w.newline()
	w.newline()
}

// safeURL keeps an address a browser may follow: http, https, or one relative
// to the page. Anything else, a script or a data URL, is dropped.
func (w *writer) safeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if !u.IsAbs() {
		if w.base == nil {
			return ""
		}
		u = w.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// tidy trims line ends and leaves at most one blank line between blocks.
func tidy(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}
