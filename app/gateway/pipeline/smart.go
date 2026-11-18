package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"rllmx/app/gateway/combo"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
)

// smartRouter asks one model which member of a combo fits the request. The
// question and the answer travel in the client's own format, through the
// ordinary pipeline, so the call costs one row like any other; a failure is
// combo.ErrNoChoice and the declared order stands.
type smartRouter struct{ call *call }

// Choose sends the choice question to the policy's router model and reads back
// the member it named. The router model must be a plain model: a combo there
// would recurse.
func (r smartRouter) Choose(ctx context.Context, routerModel, state string, models []string) (combo.Choice, error) {
	if routerModel == "" {
		return combo.Choice{}, combo.ErrNoChoice
	}
	t, err := r.call.p.d.Resolver.Resolve(ctx, routerModel)
	if err != nil || t.Combo != "" {
		return combo.Choice{}, combo.ErrNoChoice
	}
	dest, out, ok := r.call.destinationOf(ctx, t)
	if !ok {
		return combo.Choice{}, combo.ErrNoChoice
	}
	codec, ok := translate.Lookup(r.call.format)
	if !ok {
		return combo.Choice{}, combo.ErrNoChoice
	}
	// A fresh request from the client's own body: the question replaces the
	// conversation, because the router is asked once with no history.
	req := *r.call.req
	req.Model = routerModel
	req.Stream = false
	req.Messages = []translate.Message{{
		Role:    translate.RoleUser,
		Content: translate.TextPart(choiceQuestion(state, models)),
	}}
	caps := r.call.p.d.Caps.For(dest.provider, dest.model)
	tctx := translate.NewCtx(r.call.format, dest.format)
	tctx.Provider, tctx.Model, tctx.Caps, tctx.Entry = dest.provider, dest.model, caps, dest.entry
	body, err := codec.WriteRequest(&req, tctx)
	if err != nil {
		return combo.Choice{}, combo.ErrNoChoice
	}
	rctx, cancel := context.WithTimeout(ctx, smartRouterTimeout)
	defer cancel()
	out = r.call.p.Handle(rctx, Input{
		Path: r.call.in.Path, Body: body, Caller: r.call.in.Caller,
		RequestID: r.call.in.RequestID + "-route", UserAgent: r.call.in.UserAgent,
		Header: r.call.in.Header, AcceptJSON: r.call.in.AcceptJSON,
	}, func() (stream.Client, error) { return nil, errors.New("the router never streams") })
	if out.Status < 200 || out.Status > 299 {
		return combo.Choice{}, combo.ErrNoChoice
	}
	answer, err := codec.ReadResponse(out.Body, tctx)
	if err != nil {
		return combo.Choice{}, combo.ErrNoChoice
	}
	return parseChoice(answer.Message.Text())
}

// choiceQuestion is the one-shot question the router answers with JSON:
// {"model":"<member>","confidence":<0..1>}.
func choiceQuestion(state string, models []string) string {
	quoted := make([]string, len(models))
	for i, m := range models {
		quoted[i] = `"` + m + `"`
	}
	var b strings.Builder
	b.WriteString("Which of these configured models is the most appropriate for this request? Answer with JSON only: {\"model\":\"<one of the models>\",\"confidence\":<0..1>}. Models: ")
	b.WriteString(strings.Join(quoted, ", "))
	if state != "" {
		b.WriteString(". Request: ")
		b.WriteString(state)
	}
	return b.String()
}

// parseChoice reads the router's answer; anything else is no choice.
func parseChoice(text string) (combo.Choice, error) {
	var body struct {
		Model      string  `json:"model"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		// The router may wrap its answer in prose: the first JSON object wins.
		start := strings.Index(text, "{")
		end := strings.LastIndex(text, "}")
		if start < 0 || end <= start || json.Unmarshal([]byte(text[start:end+1]), &body) != nil {
			return combo.Choice{}, combo.ErrNoChoice
		}
	}
	if body.Model == "" {
		return combo.Choice{}, combo.ErrNoChoice
	}
	if body.Confidence < 0 {
		body.Confidence = 0
	}
	if body.Confidence > 1 {
		body.Confidence = 1
	}
	return combo.Choice{Model: body.Model, Confidence: body.Confidence}, nil
}

// smartRouterTimeout bounds the router call.
const smartRouterTimeout = 8 * time.Second
