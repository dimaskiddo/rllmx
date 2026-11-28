package kiro

import (
	"encoding/json"
	"strconv"
	"strings"

	"rllmx/app/platform/text"
)

// kiroCanonical is a conversation Kiro accepts: alternating turns, a last user
// turn, and every tool call answered by the turn that follows it.
type kiroCanonical struct {
	History []kiroTurn
	Current kiroUser
	// Valid is false when no repair made the conversation shippable, so the
	// request is answered here instead of spending an upstream call on a 400.
	Valid bool
}

// canonicalizeKiro repairs a conversation into the shape Kiro requires: merge,
// normalize roles, reconcile tool pairs, then validate. A conversation that
// still fails is flattened to text, which always validates.
func canonicalizeKiro(turns []kiroTurn, specs []kiroToolSpec, names map[string]string) kiroCanonical {
	merged := normalizeRoles(kiroMergeTurns(turns))
	reconcileTools(merged, specs, names)
	current, history := splitCurrent(merged)
	if kiroValid(history, current, specs) {
		return kiroCanonical{History: history, Current: current, Valid: true}
	}
	flattenTools(merged)
	current, history = splitCurrent(merged)
	return kiroCanonical{History: history, Current: current, Valid: kiroValid(history, current, specs)}
}

// kiroMergeTurns drops empty turns and merges adjacent ones of the same role,
// because Kiro requires them to alternate.
func kiroMergeTurns(turns []kiroTurn) []kiroTurn {
	out := make([]kiroTurn, 0, len(turns))
	for _, t := range turns {
		switch {
		case t.User == nil && t.Asst == nil:
		case t.User != nil && len(out) > 0 && out[len(out)-1].User != nil:
			mergeUser(out[len(out)-1].User, t.User)
		case t.Asst != nil && len(out) > 0 && out[len(out)-1].Asst != nil:
			mergeAsst(out[len(out)-1].Asst, t.Asst)
		default:
			out = append(out, t)
		}
	}
	return out
}

func mergeUser(dst, src *kiroUser) {
	appendContent(&dst.Content, src.Content)
	dst.Images = append(dst.Images, src.Images...)
	dst.Results = append(dst.Results, src.Results...)
}

func mergeAsst(dst, src *kiroAsst) {
	appendContent(&dst.Content, src.Content)
	dst.Calls = append(dst.Calls, src.Calls...)
}

// appendContent joins two pieces of turn text.
func appendContent(dst *string, extra string) {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return
	}
	if *dst == "" {
		*dst = extra
		return
	}
	*dst += "\n\n" + extra
}

// normalizeRoles makes the shape the upstream requires: a first user turn, a
// last user turn, and no empty one of either.
func normalizeRoles(turns []kiroTurn) []kiroTurn {
	if len(turns) == 0 {
		return turns
	}
	if turns[0].Asst != nil {
		turns = append([]kiroTurn{{User: &kiroUser{Content: kiroEmptyUserPlaceholder}}}, turns...)
	}
	for i := range turns {
		switch {
		case turns[i].User != nil:
			u := turns[i].User
			u.Content = strings.TrimSpace(u.Content)
			if u.Content == "" {
				u.Content = kiroEmptyUserPlaceholder
				if len(u.Results) > 0 {
					u.Content = kiroToolResultsPlaceholder
				}
			}
		case turns[i].Asst != nil:
			a := turns[i].Asst
			a.Content = strings.TrimSpace(a.Content)
			if a.Content == "" && len(a.Calls) == 0 {
				a.Content = "..."
			}
		}
	}
	return turns
}

// reconcileTools makes every call adjacent to its result: a call with no result
// becomes text in its own turn, and a result with no call becomes text in the
// user's. Kiro refuses a conversation where the two do not pair one to one.
func reconcileTools(turns []kiroTurn, specs []kiroToolSpec, names map[string]string) {
	known := make(map[string]bool, len(specs))
	for _, s := range specs {
		known[s.ToolSpecification.Name] = true
	}
	used := map[string]bool{}
	for i := 0; i < len(turns); i++ {
		a := turns[i].Asst
		if a == nil || len(a.Calls) == 0 {
			continue
		}
		var next *kiroUser
		if i+1 < len(turns) {
			next = turns[i+1].User
		}
		kept, results := pairCalls(a, next, known, names, used, i)
		if len(kept) > 0 {
			a.Calls = kept
		} else {
			a.Calls = nil
		}
		if next == nil {
			// A call with no turn to answer it: the model's own words keep the
			// fact, so nothing is lost.
			continue
		}
		next.Results = results
	}
}

// pairCalls keeps the calls that have a result and a spec, and turns everything
// else into text. The ids it keeps are unique and shaped as Kiro accepts.
func pairCalls(a *kiroAsst, next *kiroUser, known map[string]bool, names map[string]string, used map[string]bool, turn int) ([]kiroCall, []kiroResult) {
	var calls []kiroCall
	results := map[string]kiroResult{}
	if next != nil {
		for _, r := range next.Results {
			results[r.ToolUseID] = r
		}
	}
	var kept []kiroResult
	for i, c := range a.Calls {
		mapped := names[c.Name]
		if mapped == "" {
			mapped = c.Name
		}
		res, hasResult := results[c.ToolUseID]
		valid := hasResult && known[mapped] && kiroInputOK(c.Input)
		if !valid {
			appendContent(&a.Content, "[Tool call: "+mapped+"("+string(c.Input)+")]")
			if hasResult {
				appendContent(&next.Content, "[Tool result: "+plainResult(res)+"]")
				delete(results, c.ToolUseID)
			}
			continue
		}
		id := kiroToolID(c.ToolUseID, turn, i+1, mapped, used)
		calls = append(calls, kiroCall{ToolUseID: id, Name: mapped, Input: c.Input})
		kept = append(kept, kiroResult{ToolUseID: id, Status: res.Status, Content: res.Content})
		delete(results, c.ToolUseID)
	}
	// A result whose call is gone becomes text, so the model still sees it.
	for _, r := range results {
		appendContent(&next.Content, "[Tool result: "+plainResult(r)+"]")
	}
	return calls, kept
}

// kiroToolID keeps an id the shape Kiro accepts, invents one when it has none,
// and makes it unique within the request.
func kiroToolID(raw string, turn, index int, name string, used map[string]bool) string {
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	id := text.CutRunes(b.String(), kiroToolIDMax)
	if id == "" {
		id = text.CutRunes("call_msg"+strconv.Itoa(turn)+"_tc"+strconv.Itoa(index)+"_"+name, kiroToolIDMax)
	}
	base := id
	for n := 2; used[base]; n++ {
		suffix := "_" + strconv.Itoa(n)
		base = text.CutRunes(id, kiroToolIDMax-len(suffix)) + suffix
	}
	used[base] = true
	return base
}

// kiroInputOK reports an input Kiro can carry: a JSON object, or nothing.
func kiroInputOK(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	_, ok := v.(map[string]any)
	return ok
}

// plainResult is a result's text, for the flattened form.
func plainResult(r kiroResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Text == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(c.Text)
	}
	return b.String()
}

// flattenTools turns every structured tool turn into text. It is the second
// repair, and it always validates: nothing structural is left to refuse.
func flattenTools(turns []kiroTurn) {
	for i := range turns {
		if a := turns[i].Asst; a != nil && len(a.Calls) > 0 {
			for _, c := range a.Calls {
				appendContent(&a.Content, "[Tool call: "+c.Name+"("+string(c.Input)+")]")
			}
			a.Calls = nil
		}
		if u := turns[i].User; u != nil && len(u.Results) > 0 {
			for _, r := range u.Results {
				appendContent(&u.Content, "[Tool result: "+plainResult(r)+"]")
			}
			u.Results = nil
		}
	}
}

// splitCurrent takes the last user turn out as the one the model answers, and
// leaves the rest as history. A conversation with no user turn gets one, so the
// caller always has a current message to send.
func splitCurrent(turns []kiroTurn) (kiroUser, []kiroTurn) {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].User == nil {
			continue
		}
		current := *turns[i].User
		history := append([]kiroTurn{}, turns[:i]...)
		history = append(history, turns[i+1:]...)
		return current, history
	}
	return kiroUser{Content: kiroEmptyUserPlaceholder}, turns
}

// kiroValid reports a conversation the upstream will accept: turns alternate
// starting with a user turn, every call has an adjacent result, ids are unique
// and named by a spec, and the current turn has text.
func kiroValid(history []kiroTurn, current kiroUser, specs []kiroToolSpec) bool {
	known := make(map[string]bool, len(specs))
	for _, s := range specs {
		known[s.ToolSpecification.Name] = true
	}
	used := map[string]bool{}
	all := append(append([]kiroTurn{}, history...), kiroTurn{User: &current})
	for i, t := range all {
		user := i%2 == 0
		if (t.User != nil) != user {
			return false
		}
		if t.User != nil {
			if t.User.Content == "" {
				return false
			}
			if i == 0 && len(t.User.Results) > 0 {
				return false
			}
			continue
		}
		a := t.Asst
		next := kiroUser{}
		if i+1 < len(all) && all[i+1].User != nil {
			next = *all[i+1].User
		}
		if len(a.Calls) != len(next.Results) {
			return false
		}
		ids := make(map[string]bool, len(next.Results))
		for _, r := range next.Results {
			ids[r.ToolUseID] = true
		}
		for _, c := range a.Calls {
			if c.ToolUseID == "" || used[c.ToolUseID] || !ids[c.ToolUseID] || !known[c.Name] {
				return false
			}
			used[c.ToolUseID] = true
		}
	}
	return current.Content != ""
}
