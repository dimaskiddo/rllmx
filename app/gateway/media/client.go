package media

import (
	"bytes"
	"context"
	"io"
	"net/http"

	appfile "rllmx/app/platform/file"
)

// HTTP sends one media call through the guarded transport. The client it wraps
// refuses redirects by construction, so a provider cannot move the call to an
// address that was not checked.
type HTTP struct {
	Client *http.Client
	// Cap bounds one answer; zero is 64 MiB.
	Cap int64
}

// Do sends the call and reads the answer whole. An adapter that polls sends
// each later call through the same method.
func (h HTTP) Do(ctx context.Context, method, target string, header map[string]string, body []byte) (int, http.Header, []byte, error) {
	return h.DoCapped(ctx, method, target, header, body, h.Cap)
}

// DoCapped is Do with a limit for this one answer, for an image fetch the
// caller bounds tighter than a media answer.
func (h HTTP) DoCapped(ctx context.Context, method, target string, header map[string]string, body []byte, limit int64) (int, http.Header, []byte, error) {
	if h.Client == nil {
		return 0, nil, nil, errNoClient
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	cap := limit
	if cap == 0 {
		cap = 64 << 20
	}
	raw, err := appfile.ReadCapped(resp.Body, cap)
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	return resp.StatusCode, resp.Header, raw, nil
}

type clientError string

func (e clientError) Error() string { return string(e) }

const errNoClient clientError = "media: no HTTP client"
