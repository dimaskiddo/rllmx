package cursor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/network"
	"rllmx/app/store/dialect"
)

// ErrMachine means the account holds no machine id, which the service refuses
// every request without. Nothing is sent.
var ErrMachine = executor.ErrBadAccount

// Cursor sends a turn to the agent service as Connect-framed protobuf over
// HTTP/2, and answers in the chat chunks every other format is translated from.
type Cursor struct {
	Default *executor.Default
	// Cache keeps the account's own pick for the service's "Auto" entry; nil
	// asks the service on every such request.
	Cache *cache.Cache
	// PeekIdle bounds the silence tolerated before a turn that has sent nothing
	// is taken as ended.
	PeekIdle time.Duration
	// Sleep waits out the empty-turn retry; nil uses a real timer.
	Sleep func(time.Duration) <-chan time.Time
	// Now is the clock for the header's stamp; nil uses time.Now.
	Now func() time.Time
}

const (
	// catalogueLife is how long the account's model answer is kept.
	catalogueLife = 5 * time.Minute
	// emptyTurnWait is the pause before a turn that produced nothing is asked
	// again with its tail only.
	emptyTurnWait = 400 * time.Millisecond
	emptyTurnTail = 2
	// maxCatalogue bounds what the model answer may read.
	maxCatalogue = 1 << 20
)

// Execute sends one turn.
func (c *Cursor) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	machine := account(req, "machineId")
	if !validMachine(machine) {
		return executor.Response{}, ErrMachine
	}
	if !gjson.ValidBytes(req.Body) {
		return executor.Response{}, executor.ErrUnreachable
	}
	model, err := c.resolveModel(ctx, req, machine)
	if err != nil {
		return executor.Response{}, err
	}
	body := req.Body
	for attempt := 0; ; attempt++ {
		resp, empty, err := c.turn(ctx, req, body, model, machine)
		if err != nil || !empty || attempt >= 1 {
			return resp, err
		}
		body = tailBody(body)
		if err := executor.Wait(ctx, emptyTurnWait, c.Sleep); err != nil {
			return executor.Response{}, err
		}
	}
}

// validMachine holds the machine id to a plain token, because it is joined into
// a header beside the checksum.
func validMachine(machine string) bool {
	if machine == "" || len(machine) > 128 {
		return false
	}
	for _, r := range machine {
		if r <= ' ' || r >= 0x7f {
			return false
		}
	}
	return true
}

// resolveModel turns the picker's alias into a model the service serves: sent as
// it stands, the alias answers an empty turn.
func (c *Cursor) resolveModel(ctx context.Context, req executor.Request, machine string) (string, error) {
	if model := req.Model; model != "" && model != "default" && model != "auto" {
		return model, nil
	}
	pick := c.autoModel(ctx, req, machine)
	if pick == "" {
		pick = req.Model
	}
	return pick, nil
}

// autoModel asks the service which model its own picker would run, through the
// bounded cache. A failure leaves the alias in place.
func (c *Cursor) autoModel(ctx context.Context, req executor.Request, machine string) string {
	if c.Cache == nil {
		return c.fetchAuto(ctx, req, machine)
	}
	key := dialect.HashKey("cursor", req.Key, machine)
	raw, err := c.Cache.Load(ctx, cache.Connections, key, catalogueLife, func(ctx context.Context) ([]byte, error) {
		pick := c.fetchAuto(ctx, req, machine)
		if pick == "" {
			return nil, errors.New("cursor: the account has no usable model")
		}
		return []byte(pick), nil
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

// fetchAuto reads the model answer and picks from it.
func (c *Cursor) fetchAuto(ctx context.Context, req executor.Request, machine string) string {
	resp, err := c.do(ctx, req, machine, false, http.NoBody)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		executor.Drain(resp.Body)
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogue))
	if err != nil {
		return ""
	}
	// The picker's flags ride in the same answer, at a field of their own.
	if pick := autoModelFrom(raw); pick != "" {
		return pick
	}
	if models := usableModels(raw); len(models) > 0 {
		return models[0]
	}
	return ""
}

// turn makes one run call: the frame goes out, the acks follow on the same
// stream, and the answer is read until it produces something.
func (c *Cursor) turn(ctx context.Context, req executor.Request, body []byte, model, machine string) (executor.Response, bool, error) {
	run, err := runRequest(body, model, newMessageID())
	if err != nil {
		return executor.Response{}, false, err
	}
	pipe := newPeer()
	resp, err := c.do(ctx, req, machine, true, io.MultiReader(bytes.NewReader(run), pipe.reader()))
	if err != nil {
		pipe.close()
		return executor.Response{}, false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		pipe.close()
		executor.Drain(resp.Body)
		_ = resp.Body.Close()
		return executor.Response{Status: resp.StatusCode, Header: resp.Header}, false, nil
	}
	reader := &turnRead{
		frames: &frameReader{r: resp.Body}, model: model, composer: composerModel(model),
		write: pipe.write,
		close: func() error { pipe.close(); return resp.Body.Close() },
	}
	return c.readStart(reader)
}

// readStart reads the turn until its first delta arrives or it ends, so a
// refusal sent in the stream is answered with its status. A turn that ended
// having produced nothing is reported as empty, for the retry.
func (c *Cursor) readStart(r *turnRead) (executor.Response, bool, error) {
	var prefix bytes.Buffer
	reader := newStartReader(r)
	defer func() {
		if prefix.Len() == 0 {
			reader.close()
		}
	}()
	buf := make([]byte, 8<<10)
	for {
		n, err := reader.Wait(buf, c.PeekIdle)
		prefix.Write(buf[:n])
		if err != nil {
			var refused *statusError
			if errors.As(err, &refused) {
				_ = r.Close()
				return executor.Response{Status: statusOf(refused.code), Header: jsonHeader(), Body: http.NoBody}, false, nil
			}
			if !errors.Is(err, io.EOF) {
				_ = r.Close()
				return executor.Response{}, false, err
			}
			break
		}
		if n == 0 {
			// Nothing arrived in time. The turn is handed over as it stands, and
			// its silence is the stream timers' business from here.
			break
		}
		if bytes.Contains(prefix.Bytes(), []byte(`"content"`)) {
			break
		}
	}
	if prefix.Len() == 0 {
		_ = r.Close()
		return executor.Response{Status: http.StatusOK, Header: streamHeader(), Body: http.NoBody}, true, nil
	}
	return executor.Response{Status: http.StatusOK, Header: streamHeader(),
		Body: handOver(prefix.Bytes(), reader, r.Close)}, false, nil
}

// handOver is the answer once the start has been read ahead. Closing it ends the
// read-ahead too: r.Close alone stops the source's read but leaves the pump
// waiting to hand over a chunk nobody will take.
func handOver(prefix []byte, reader *startReader, closeTurn func() error) turnBody {
	return turnBody{
		Reader: io.MultiReader(bytes.NewReader(prefix), reader),
		close: func() error {
			reader.close()
			return closeTurn()
		},
	}
}

// turnBody is the answer: what was read ahead, then the rest of the turn. Its
// Close ends the exchange, so the service is never left waiting on a stream
// this router stopped reading.
type turnBody struct {
	io.Reader
	close func() error
}

func (b turnBody) Close() error { return b.close() }

// do builds one call to the service. The credential, the identity headers and
// the Connect framing are the same on both addresses, so they are set here once.
func (c *Cursor) do(ctx context.Context, req executor.Request, machine string, stream bool, body io.Reader) (*http.Response, error) {
	// The agent service speaks HTTP/2 only, so a QUIC attempt is a wasted dial.
	ctx = network.WithoutHTTP3(ctx)
	target, contentType := req.URL, "application/connect+proto"
	if !stream {
		target = strings.TrimSuffix(target, "/Run") + "/GetUsableModels"
		contentType = "application/proto"
	}
	h := headers(req.Key, machine, ghost(req))
	h["Content-Type"] = contentType
	if !stream {
		h["Accept"] = "application/proto"
		delete(h, "connect-protocol-version")
		delete(h, "connect-accept-encoding")
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return nil, executor.ErrUnreachable
	}
	for k, v := range h {
		hr.Header.Set(k, v)
	}
	// The operator's overrides may add a header but never replace the credential.
	for k, v := range req.Overrides {
		if !strings.EqualFold(k, "Authorization") {
			hr.Header.Set(k, v)
		}
	}
	client := *c.Default.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(hr)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", executor.ErrUnreachable, ctx.Err())
	}
	return resp, nil
}

// tailBody keeps only the last turns of a conversation, for the empty-turn
// retry: a long replay is what the service answers with nothing.
func tailBody(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	all := messages.Array()
	if len(all) <= emptyTurnTail+1 {
		return body
	}
	keep := make([]any, 0, emptyTurnTail+1)
	for _, m := range all[:len(all)-emptyTurnTail] {
		if m.Get("role").String() == "system" {
			keep = append(keep, m.Value())
		}
	}
	for _, m := range all[len(all)-emptyTurnTail:] {
		keep = append(keep, m.Value())
	}
	return rawSet(body, "messages", keep)
}

// ghost reports the service's privacy mode, which is on unless the account says
// otherwise.
func ghost(req executor.Request) bool { return account(req, "ghostMode") != "false" }

// account reads one text value of the account's provider data, or "".
func account(req executor.Request, name string) string {
	if req.Account == nil {
		return ""
	}
	return req.Account(name)
}

// jsonHeader and streamHeader are the answer's own content types.
func jsonHeader() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}}
}

func streamHeader() http.Header {
	return http.Header{"Content-Type": []string{"text/event-stream"}}
}
