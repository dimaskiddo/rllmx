package fetch

import (
	"bytes"
	"context"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// accept is what the reader asks for: pages it can convert, then anything.
const accept = "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5"

// Local reads the page itself, for a deployment with no extraction vendor: the
// fetch target is the request, and the page is converted here.
type Local struct{}

// Kind is the service it serves.
func (Local) Kind() string { return upstream.KindFetch }

// Build makes the GET the reader takes.
func (Local) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	return media.Call{Method: http.MethodGet, URL: target(r), Header: map[string]string{"Accept": accept}}, nil
}

// Parse converts the page the reader received.
func (Local) Parse(_ context.Context, r media.Request, _ media.Client, status int, header http.Header, raw []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, refused(status)
	}
	title, text, err := readPage(raw, header.Get("Content-Type"), target(r), format(r))
	if err != nil {
		return media.Response{}, err
	}
	return reply("local-fetch", target(r), format(r), title, text, nil, limit(r), cost(r))
}

// readPage turns a fetched page into the requested format. A type this reader
// cannot convert is refused rather than described.
func readPage(raw []byte, contentType, targetURL, want string) (title, text string, err error) {
	mediaType, _, _ := mime.ParseMediaType(strings.ToLower(contentType))
	switch {
	case mediaType == "text/plain", mediaType == "text/markdown", mediaType == "text/x-markdown":
		return "", string(raw), nil
	case mediaType != "text/html" && mediaType != "application/xhtml+xml":
		return "", "", media.BadRequestf("the address is not a page this router reads")
	}
	decoded, err := charset.NewReader(bytes.NewReader(raw), contentType)
	if err != nil {
		return "", "", media.BadRequestf("the page is in an encoding this router cannot read")
	}
	node, err := html.Parse(decoded)
	if err != nil {
		return "", "", media.BadRequestf("the page cannot be read")
	}
	base, _ := url.Parse(targetURL)
	if want == "html" {
		return pageTitle(node), string(raw), nil
	}
	return pageTitle(node), convert(node, base, want == "markdown"), nil
}

// pageTitle is the page's own title, trimmed to one line.
func pageTitle(root *html.Node) string {
	var title string
	var visit func(*html.Node, int)
	visit = func(n *html.Node, depth int) {
		if title != "" || depth > maxDepth {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" {
			title = strings.Join(strings.Fields(nodeText(n, 0)), " ")
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c, depth+1)
		}
	}
	visit(root, 0)
	return title
}

// nodeText is the text a node holds, without descending further than the depth
// a title may have.
func nodeText(n *html.Node, depth int) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	if depth > maxDepth {
		return ""
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(nodeText(c, depth+1))
	}
	return b.String()
}
