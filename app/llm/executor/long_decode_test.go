package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// hops answers each call in order and keeps what it was sent.
type hops struct {
	replies []Response
	// again answers every call after the scripted ones, with a fresh body each
	// time: a body read once cannot be read again.
	again func() Response
	sent  [][]byte
	err   error
}

func (h *hops) Execute(_ context.Context, req Request) (Response, error) {
	h.sent = append(h.sent, req.Body)
	if i := len(h.sent) - 1; i < len(h.replies) {
		return h.replies[i], nil
	}
	if h.again != nil {
		return h.again(), nil
	}
	return Response{}, h.err
}

type spyBody struct {
	io.Reader
	closed bool
}

func (b *spyBody) Close() error { b.closed = true; return nil }

func answer(status int, ctype, body string) Response {
	return Response{Status: status, Header: http.Header{"Content-Type": {ctype}, "Content-Length": {"9"}}, Body: &spyBody{Reader: strings.NewReader(body)}}
}

func jsonHop(text, finish, token string, prompt, out int) Response {
	c := `{"content":{"role":"model","parts":[{"text":"` + text + `"}]},"finishReason":"` + finish + `"`
	if token != "" {
		c += `,"continuationToken":"` + token + `"`
	}
	return answer(200, "application/json", `{"candidates":[`+c+`}],"usageMetadata":{"promptTokenCount":`+itoa(prompt)+`,"candidatesTokenCount":`+itoa(out)+`}}`)
}

func itoa(n int) string { return strconv.Itoa(n) }

func sseHop(text, finish, token string, out int) Response {
	cand := `{"content":{"role":"model","parts":[{"text":"` + text + `"}]}`
	if finish != "" {
		cand += `,"finishReason":"` + finish + `"`
	}
	if token != "" {
		cand += `,"continuationToken":"` + token + `"`
	}
	return answer(200, "text/event-stream", "data: {\"candidates\":["+cand+"}],\"usageMetadata\":{\"candidatesTokenCount\":"+itoa(out)+"}}\n\n")
}

func readAll(t *testing.T, r Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const askBody = `{"contents":[{"role":"user","parts":[{"text":"write"}]}],"generationConfig":{"maxOutputTokens":200000}}`
const envelopeBody = `{"project":"p","model":"m","request":{"contents":[{"role":"user","parts":[{"text":"write"}]}],"generationConfig":{"maxOutputTokens":200000}}}`

func TestALongDecodeIsOnlyRunForAModelThatContinuesAndAskedForMoreThanOneHop(t *testing.T) {
	long := Request{LongDecode: true}
	for name, tc := range map[string]struct {
		body string
		want bool
	}{
		"asks for more":      {askBody, true},
		"envelope asks more": {envelopeBody, true},
		"exactly one hop":    {`{"generationConfig":{"maxOutputTokens":65536}}`, false},
		"asks for less":      {`{"generationConfig":{"maxOutputTokens":1000}}`, false},
		"no limit":           {`{"contents":[]}`, false},
		"not json":           {`nope`, false},
		"limit is text":      {`{"generationConfig":{"maxOutputTokens":"200000"}}`, true},
		"limit is negative":  {`{"generationConfig":{"maxOutputTokens":-5}}`, false},
	} {
		long.Body = []byte(tc.body)
		if got := WantsLongDecode(long); got != tc.want {
			t.Errorf("%s: %v", name, got)
		}
	}
	if WantsLongDecode(Request{Body: []byte(askBody)}) {
		t.Error("a model that does not continue was run as one")
	}
}

func TestAHopIsSentAgainWithTheIdenticalBodyAndOnlyTheLatestToken(t *testing.T) {
	h := &hops{replies: []Response{jsonHop("a", "CONTINUATION", "T1", 5, 10), jsonHop("b", "CONTINUATION", "T2", 5, 10), jsonHop("c", "STOP", "", 5, 7)}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if len(h.sent) != 3 {
		t.Fatalf("%d calls", len(h.sent))
	}
	if gjson.GetBytes(h.sent[0], "continuationToken").Exists() {
		t.Errorf("the first call carries a token: %s", h.sent[0])
	}
	for i, want := range []string{"T1", "T2"} {
		got := h.sent[i+1]
		if gjson.GetBytes(got, "continuationToken").String() != want {
			t.Errorf("hop %d token: %s", i+2, got)
		}
		stripped, _ := sjson.DeleteBytes(got, "continuationToken")
		if string(stripped) != askBody {
			t.Errorf("hop %d changed the request:\n%s\n%s", i+2, stripped, askBody)
		}
	}
}

func TestACloudCodeEnvelopeTakesTheTokenInsideItsRequest(t *testing.T) {
	h := &hops{replies: []Response{jsonHop("a", "CONTINUATION", "T1", 1, 1), jsonHop("b", "STOP", "", 1, 1)}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(envelopeBody), LongDecode: true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, resp)
	if gjson.GetBytes(h.sent[1], "request.continuationToken").String() != "T1" || gjson.GetBytes(h.sent[1], "continuationToken").Exists() {
		t.Errorf("%s", h.sent[1])
	}
}

func TestHopsAreJoinedIntoOneTurnWithTheirUsageSummed(t *testing.T) {
	h := &hops{replies: []Response{jsonHop("Hello ", "CONTINUATION", "T1", 100, 40), jsonHop("world", "STOP", "", 100, 25)}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	g := gjson.Parse(readAll(t, resp))
	if resp.Status != 200 || g.Get("candidates.0.content.parts.#").Int() != 2 || g.Get("candidates.0.content.parts.0.text").String() != "Hello " || g.Get("candidates.0.content.parts.1.text").String() != "world" {
		t.Errorf("%s", g.Raw)
	}
	if g.Get("candidates.0.finishReason").String() != "STOP" || g.Get("candidates.0.continuationToken").Exists() {
		t.Errorf("finish: %s", g.Raw)
	}
	if g.Get("usageMetadata.candidatesTokenCount").Int() != 65 || g.Get("usageMetadata.promptTokenCount").Int() != 200 {
		t.Errorf("usage: %s", g.Get("usageMetadata").Raw)
	}
	if resp.Header.Get("Content-Length") != "" {
		t.Error("the length of the first hop was kept for a longer body")
	}
}

func TestAWrappedEnvelopeAnswerStaysWrapped(t *testing.T) {
	wrap := func(inner string) Response {
		return answer(200, "application/json", `{"response":`+inner+`,"traceId":"x"}`)
	}
	h := &hops{replies: []Response{
		wrap(`{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"CONTINUATION","continuationToken":"T"}],"usageMetadata":{"candidatesTokenCount":3}}`),
		wrap(`{"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":4}}`),
	}}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(envelopeBody), LongDecode: true}, 20)
	g := gjson.Parse(readAll(t, resp))
	if g.Get("response.candidates.0.content.parts.#").Int() != 2 || g.Get("response.usageMetadata.candidatesTokenCount").Int() != 7 || g.Get("traceId").String() != "x" {
		t.Errorf("%s", g.Raw)
	}
}

func TestAFailedLaterHopEndsTheTurnAsTruncatedNotAsAnError(t *testing.T) {
	for name, h := range map[string]*hops{
		"refused":  {replies: []Response{jsonHop("a", "CONTINUATION", "T", 1, 1), answer(500, "application/json", `{"error":"x"}`)}},
		"failed":   {replies: []Response{jsonHop("a", "CONTINUATION", "T", 1, 1)}, err: errors.New("boom")},
		"not json": {replies: []Response{jsonHop("a", "CONTINUATION", "T", 1, 1), answer(200, "application/json", `<html>`)}},
	} {
		resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20)
		if err != nil || resp.Status != 200 {
			t.Fatalf("%s: %v %d", name, err, resp.Status)
		}
		g := gjson.Parse(readAll(t, resp))
		if g.Get("candidates.0.finishReason").String() != "MAX_TOKENS" || g.Get("candidates.0.content.parts.0.text").String() != "a" {
			t.Errorf("%s: %s", name, g.Raw)
		}
	}
}

func TestTheFirstHopsFailureIsTheAnswerUntouched(t *testing.T) {
	h := &hops{replies: []Response{answer(429, "application/json", `{"error":{"message":"slow"}}`)}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20)
	if err != nil || resp.Status != 429 || !strings.Contains(readAll(t, resp), "slow") || len(h.sent) != 1 {
		t.Fatalf("%v %d after %d calls", err, resp.Status, len(h.sent))
	}
	h = &hops{replies: nil, err: errors.New("down")}
	if _, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20); err == nil {
		t.Error("the first call's error was lost")
	}
}

func TestARunawayUpstreamStopsAtTheHopCap(t *testing.T) {
	h := &hops{again: func() Response { return jsonHop("x", "CONTINUATION", "T", 1, 1) }}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 4)
	g := gjson.Parse(readAll(t, resp))
	if len(h.sent) != 4 || g.Get("candidates.0.finishReason").String() != "MAX_TOKENS" {
		t.Errorf("%d calls: %s", len(h.sent), g.Raw)
	}
}

func bigHop(size int, finish, token string) Response {
	c := `{"content":{"parts":[{"text":"` + strings.Repeat("x", size) + `"}]},"finishReason":"` + finish + `"`
	if token != "" {
		c += `,"continuationToken":"` + token + `"`
	}
	return answer(200, "application/json", `{"candidates":[`+c+`}]}`)
}

func TestAFirstAnswerPastTheByteBoundIsAnErrorForTheCallerToFailOver(t *testing.T) {
	h := &hops{replies: []Response{bigHop(maxJoinedBytes, "CONTINUATION", "T")}}
	if _, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20); err == nil {
		t.Fatal("an unreadable first answer was passed on as one")
	}
}

func TestHopsThatTogetherPassTheByteBoundEndTheTurnTruncatedWithWhatFit(t *testing.T) {
	half := maxJoinedBytes/2 + 100
	h := &hops{replies: []Response{bigHop(half, "CONTINUATION", "T"), bigHop(half, "STOP", "")}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20)
	if err != nil || resp.Status != 200 {
		t.Fatalf("%v %d", err, resp.Status)
	}
	body := readAll(t, resp)
	if len(body) > maxJoinedBytes || !strings.Contains(body[len(body)-200:], "MAX_TOKENS") || len(h.sent) != 2 {
		t.Errorf("%d bytes after %d calls, ends %q", len(body), len(h.sent), body[len(body)-120:])
	}
}

func TestAStreamRunsEveryHopAsOneTurn(t *testing.T) {
	h := &hops{replies: []Response{sseHop("Hel", "CONTINUATION", "T1", 10), sseHop("lo ", "CONTINUATION", "T2", 12), sseHop("you", "STOP", "", 5)}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if len(h.sent) != 3 || strings.Contains(body, "continuationToken") || strings.Contains(body, "CONTINUATION") {
		t.Fatalf("%d calls: %s", len(h.sent), body)
	}
	if strings.Count(body, "finishReason") != 1 || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Errorf("an intermediate finish reached the client: %s", body)
	}
	var texts, usage []string
	for _, line := range strings.Split(body, "\n") {
		if d, ok := strings.CutPrefix(line, "data:"); ok {
			g := gjson.Parse(strings.TrimSpace(d))
			texts = append(texts, g.Get("candidates.0.content.parts.0.text").String())
			usage = append(usage, g.Get("usageMetadata.candidatesTokenCount").String())
		}
	}
	if strings.Join(texts, "") != "Hello you" || strings.Join(usage, ",") != "10,22,27" {
		t.Errorf("texts %v usage %v", texts, usage)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") || resp.Header.Get("Content-Length") != "" {
		t.Errorf("headers %v", resp.Header)
	}
}

func TestAStreamWhoseNextHopFailsEndsAsTruncated(t *testing.T) {
	h := &hops{replies: []Response{sseHop("a", "CONTINUATION", "T", 9), answer(503, "text/event-stream", "")}}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 20)
	body := readAll(t, resp)
	if !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) || strings.Contains(body, "CONTINUATION") || !strings.Contains(body, `"candidatesTokenCount":9`) {
		t.Errorf("%s", body)
	}
}

func TestAStreamPastTheHopCapEndsAsTruncated(t *testing.T) {
	h := &hops{again: func() Response { return sseHop("x", "CONTINUATION", "T", 1) }}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 3)
	body := readAll(t, resp)
	if len(h.sent) != 3 || !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) {
		t.Errorf("%d calls: %s", len(h.sent), body)
	}
}

func TestAStreamThatEndsOnAContinuationWithoutATokenIsTruncatedNotRunAway(t *testing.T) {
	h := &hops{replies: []Response{sseHop("x", "CONTINUATION", "", 1)}}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 20)
	body := readAll(t, resp)
	if len(h.sent) != 1 || !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) {
		t.Errorf("%d calls: %s", len(h.sent), body)
	}
}

func TestAWrappedStreamStaysWrapped(t *testing.T) {
	wrap := func(inner string) Response {
		return answer(200, "text/event-stream", "data: {\"response\":"+inner+"}\n\n")
	}
	h := &hops{replies: []Response{
		wrap(`{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"CONTINUATION","continuationToken":"T"}],"usageMetadata":{"candidatesTokenCount":3}}`),
		wrap(`{"candidates":[{"content":{"parts":[{"text":"b"}]},"finishReason":"STOP"}],"usageMetadata":{"candidatesTokenCount":4}}`),
	}}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(envelopeBody), Stream: true, LongDecode: true}, 20)
	body := readAll(t, resp)
	if !strings.Contains(body, `"response":{`) || strings.Contains(body, "CONTINUATION") || !strings.Contains(body, `"candidatesTokenCount":7`) {
		t.Errorf("%s", body)
	}
}

func TestClosingAStreamClosesTheHopThatIsLive(t *testing.T) {
	first := sseHop("a", "CONTINUATION", "T", 1)
	live := &spyBody{Reader: strings.NewReader("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"b\"}]}}]}\n\n")}
	second := Response{Status: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: live}
	h := &hops{replies: []Response{first, second}}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 20)
	buf := make([]byte, 8)
	for i := 0; i < 50 && len(h.sent) < 2; i++ {
		_, _ = resp.Body.Read(buf)
	}
	if err := resp.Body.Close(); err != nil || !live.closed {
		t.Errorf("close %v, live hop closed %v", err, live.closed)
	}
	if !first.Body.(*spyBody).closed {
		t.Error("the finished hop was left open")
	}
}

func TestAnEndedContextStopsTheStreamBeforeAnotherHop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &hops{replies: []Response{sseHop("a", "CONTINUATION", "T", 1), sseHop("b", "STOP", "", 1)}}
	resp, _ := Continue(ctx, h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 20)
	cancel()
	body := readAll(t, resp)
	if len(h.sent) != 1 || !strings.Contains(body, `"finishReason":"MAX_TOKENS"`) {
		t.Errorf("%d calls: %s", len(h.sent), body)
	}
}

func TestARequestThatNeedsNoLongDecodeIsNotTouched(t *testing.T) {
	first := answer(200, "application/json", `{"candidates":[{"finishReason":"STOP"}]}`)
	h := &hops{replies: []Response{first}}
	resp, err := Continue(context.Background(), h, Request{Body: []byte(`{"generationConfig":{"maxOutputTokens":100}}`)}, 20)
	if err != nil || resp.Body != first.Body || len(h.sent) != 1 {
		t.Errorf("%v: the answer was wrapped for a request that cannot continue", err)
	}
}

func TestAnAnswerThatDoesNotContinueIsReturnedAsItCame(t *testing.T) {
	first := jsonHop("a", "STOP", "", 1, 1)
	h := &hops{replies: []Response{first}}
	resp, _ := Continue(context.Background(), h, Request{Body: []byte(askBody), LongDecode: true}, 20)
	if got := readAll(t, resp); !strings.Contains(got, `"finishReason":"STOP"`) || len(h.sent) != 1 {
		t.Errorf("%s after %d calls", got, len(h.sent))
	}
}

func FuzzLongDecodeStream(f *testing.F) {
	for _, s := range []string{
		"", "data: {}\n\n", "data: {\"candidates\":[{\"finishReason\":\"CONTINUATION\",\"continuationToken\":\"T\"}]}\n\n",
		"data: [DONE]\n\n", "data: " + strings.Repeat("{", 3000), "\x00\xff\n\n", "data: {\"response\":7}\n\n",
	} {
		f.Add(s, s)
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		h := &hops{replies: []Response{answer(200, "text/event-stream", a), answer(200, "text/event-stream", b)}}
		resp, err := Continue(context.Background(), h, Request{Body: []byte(askBody), Stream: true, LongDecode: true}, 3)
		if err != nil {
			return
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	})
}
