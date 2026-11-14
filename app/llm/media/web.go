package media

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// WebUserAgent is the browser identity a web-session provider is sent. It is a
// fixed word: nothing about the machine or the operator goes on the wire.
const WebUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"

// MaxWebPrompt bounds a prompt a web-session provider is sent, in bytes.
const MaxWebPrompt = 8000

// cappedClient is a client that can bound one answer.
type cappedClient interface {
	DoCapped(ctx context.Context, method, target string, header map[string]string, body []byte, limit int64) (int, http.Header, []byte, error)
}

// Capped sends one call and reads the answer under limit. A client that cannot
// cap its own reads is checked after the fact, which is all a test double needs.
func Capped(ctx context.Context, c Client, method, target string, h map[string]string, body []byte, limit int64) (int, http.Header, []byte, error) {
	if capped, ok := c.(cappedClient); ok {
		return capped.DoCapped(ctx, method, target, h, body, limit)
	}
	status, header, raw, err := c.Do(ctx, method, target, h, body)
	if err != nil {
		return 0, nil, nil, err
	}
	if int64(len(raw)) > limit {
		return 0, nil, nil, &ProviderError{Status: http.StatusBadGateway, Text: "The provider answered more than this router reads"}
	}
	return status, header, raw, nil
}

// HTTPSOnly refuses an address a provider named that is not an https URL with a
// host and no embedded credential, before anything is sent to it.
func HTTPSOnly(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return &ProviderError{Status: http.StatusBadGateway, Text: "The provider named an address this router does not read"}
	}
	return nil
}

// WebPrompt reads the prompt of a web-session image or video request. These
// providers make one item per turn, so an n above one is refused rather than
// repeated: each repeat would be another billable conversation.
func WebPrompt(body []byte) (string, error) {
	prompt := strings.TrimSpace(gjson.GetBytes(body, "prompt").String())
	if prompt == "" || len(prompt) > MaxWebPrompt {
		return "", BadRequestf("the prompt is empty or too long")
	}
	if n := gjson.GetBytes(body, "n"); n.Exists() && n.Int() != 1 {
		return "", BadRequestf("this provider makes one item per request")
	}
	return prompt, nil
}

// Sleep waits for d or for the context, whichever ends first, and reports the
// context's error when it was the context.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// PollEvery calls step until it reports done, an error, the context ends or the
// limit passes. It waits first, then between tries, with one timer reused.
// Poll's fixed 1.5 s cadence suits a vendor job; a web render runs for minutes
// and wants its own pace.
func PollEvery(ctx context.Context, every, limit time.Duration, step func(context.Context) (bool, error), timeout error) error {
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	wait := time.NewTimer(every)
	defer wait.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return timeout
		case <-wait.C:
		}
		done, err := step(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		wait.Reset(every)
	}
}
