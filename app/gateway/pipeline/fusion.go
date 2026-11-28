package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"rllmx/app/gateway/combo"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/translate"
	"rllmx/app/store/analytics"
)

// panelResult is one panel member's outcome.
type panelResult struct {
	member     routing.Member
	out        Output
	fail       *accountFail
	text       string
	prompt     int64
	completion int64
	cost       float64
}

// fusion runs every member in parallel, takes the first quorum of answers, then
// asks a judge to combine them. One answer re-runs that member with the
// client's own request, so tools and streaming come back intact.
func (c *call) fusion(ctx context.Context, p combo.Policy, order []routing.Member) Output {
	if len(order) == 0 {
		return c.fail(http.StatusBadRequest, "invalid_request", "The combo names no model")
	}
	if len(order) == 1 {
		return c.fusionAnswer(ctx, order[0], nil, panelSpend{})
	}
	served := c.runPanel(ctx, p, order, panelQuorum(p, len(order)))
	// The client's one row carries the whole spend: the panel's, then the one
	// answer the client gets.
	var spent panelSpend
	for _, r := range served {
		spent.add(r)
	}

	switch len(served) {
	case 0:
		return c.fail(http.StatusServiceUnavailable, "no_account", "All fusion panel models failed")
	case 1:
		// One answer: re-run that member with the client's own request, so the
		// tools and the stream setting come back.
		return c.fusionAnswer(ctx, served[0].member, nil, spent)
	}

	answers := make([]string, len(served))
	for i, r := range served {
		answers[i] = r.text
	}
	judge := c.judgeMember(ctx, p, served[0].member)
	return c.fusionAnswer(ctx, judge, appendAnswers(answers), spent)
}

// panelQuorum is how many answers end the wait: the policy's, at least two and
// never more than the panel has.
func panelQuorum(p combo.Policy, members int) int {
	return min(max(p.MinPanel, 2), members)
}

// runPanel asks every member at once and returns the answers that carried text,
// once the quorum is in and the stragglers' grace has passed, or the panel's
// hard limit. Every member still running is cancelled and waited for first, so
// nothing writes after it returns.
func (c *call) runPanel(ctx context.Context, p combo.Policy, order []routing.Member, quorum int) []panelResult {
	pctx, cancel := context.WithTimeout(ctx, time.Duration(p.PanelMS)*time.Millisecond)
	defer cancel()
	ch := make(chan panelResult, len(order))
	var wg sync.WaitGroup
	for _, m := range order {
		wg.Add(1)
		go func(m routing.Member) {
			defer wg.Done()
			sub := c.memberCopy()
			out, fail := sub.comboMember(pctx, m, stripTools)
			text := ""
			if fail == nil {
				text = sub.answerText(out)
			}
			ch <- panelResult{
				member: m, out: out, fail: fail, text: text,
				prompt: sub.row.PromptTokens, completion: sub.row.CompletionTokens, cost: sub.row.Cost,
			}
		}(m)
	}

	var served []panelResult
	var graceTimer *time.Timer
	var grace <-chan time.Time
	defer func() {
		if graceTimer != nil {
			graceTimer.Stop()
		}
	}()
	collected := 0
collect:
	for collected < len(order) {
		select {
		case r := <-ch:
			collected++
			if r.fail == nil && r.text != "" {
				served = append(served, r)
				if len(served) >= quorum && grace == nil {
					graceTimer = time.NewTimer(time.Duration(p.StragglerMS) * time.Millisecond)
					grace = graceTimer.C
				}
			}
		case <-grace:
			break collect
		case <-pctx.Done():
			break collect
		}
	}
	cancel() // stragglers stop; every goroutine has its own buffered channel slot
	wg.Wait()
	return served
}

// panelSpend is what the panel's answers used.
type panelSpend struct {
	prompt, completion int64
	cost               float64
}

func (s *panelSpend) add(r panelResult) {
	s.prompt += r.prompt
	s.completion += r.completion
	s.cost += r.cost
}

// fusionAnswer runs the member that answers the client and adds the panel's
// spend to its row, after the member's own counts replaced what the row held.
func (c *call) fusionAnswer(ctx context.Context, m routing.Member, shape func(*translate.Request), spent panelSpend) Output {
	out, fail := c.comboMember(ctx, m, shape)
	// The row names the member that answered, or the one whose failure it is.
	c.row.Provider, c.row.Model = m.Provider, m.Model
	c.row.PromptTokens += spent.prompt
	c.row.CompletionTokens += spent.completion
	c.row.Cost += spent.cost
	if fail != nil {
		return c.upstreamFail(fail)
	}
	return out
}

// stripTools asks a panel member for prose only, and never streams to the
// client: the judge, not the panel, answers the client.
func stripTools(req *translate.Request) {
	req.Tools = nil
	req.ToolChoice = nil
	req.Stream = false
}

// appendAnswers adds the panel's answers as one user turn for the judge, with no
// model attribution: the judge sees the text, not who said it.
func appendAnswers(answers []string) func(*translate.Request) {
	var b strings.Builder
	b.WriteString("Combine these answers into one response to the original request. Answer in the same style the original asks for.\n")
	for i, a := range answers {
		fmt.Fprintf(&b, "\nAnswer %d:\n%s\n", i+1, a)
	}
	question := b.String()
	return func(req *translate.Request) {
		req.Messages = append(req.Messages, translate.Message{
			Role: translate.RoleUser, Content: translate.TextPart(question),
		})
	}
}

// judgeMember is the model that combines the panel: the policy's judge, else the
// first panel member.
func (c *call) judgeMember(ctx context.Context, p combo.Policy, first routing.Member) routing.Member {
	if p.JudgeModel == "" {
		return first
	}
	if t, err := c.p.d.Resolver.Resolve(ctx, p.JudgeModel); err == nil && t.Combo == "" && t.Provider != "" {
		return routing.Member{Provider: t.Provider, Model: t.Model}
	}
	// The judge does not resolve: the first panel member answers.
	return first
}

// answerText reads a served member's answer back into plain text; a body that
// will not read is not an answer.
func (c *call) answerText(out Output) string {
	if len(out.Body) == 0 {
		return ""
	}
	codec, ok := translate.Lookup(c.format)
	if !ok || c.tctx == nil {
		return ""
	}
	ans, err := codec.ReadResponse(out.Body, c.tctx)
	if err != nil || ans == nil {
		return ""
	}
	return ans.Message.Text()
}

// memberCopy is this request with fresh per-member state and no client stream,
// so panel members run beside each other without sharing mutable state.
func (c *call) memberCopy() *call {
	sub := *c
	sub.row = analytics.Record{
		Time: c.start, RequestID: c.in.RequestID, APIKeyID: c.in.Caller.ID, APIKeyName: c.in.Caller.Name,
		Endpoint: c.in.Path, SourceFormat: c.format,
	}
	sub.req, sub.tctx, sub.body, sub.dest = nil, nil, nil, destination{}
	sub.cacheKey, sub.errClass, sub.rowStatus = "", "", 0
	sub.attempt = 0
	// A panel member is the same request again: it must not plan a second time.
	sub.in.Internal = true
	sub.open = nil
	sub.clientStream = false
	sub.upStream = false
	sub.cloakedNames = nil
	return &sub
}
