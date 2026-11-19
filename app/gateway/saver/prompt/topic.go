// Package prompt is the shared half of the prompt savers: the topic scorer
// that decides when a smart mode injects, and the injector that appends a
// prompt to the request's system message. Each saver keeps its own levels and
// topic and calls in here.
package prompt

import (
	"math"
	"regexp"
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/text"
)

// Routing bounds: the latest user text is cut before it is scored, so a huge
// request costs a bounded amount of work.
const (
	queryBytes  = 4 << 10
	queryTokens = 400
)

// stopWords carry no topic signal.
var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "but": true, "if": true, "then": true,
	"else": true, "when": true, "at": true, "by": true, "for": true, "with": true, "about": true,
	"against": true, "between": true, "into": true, "through": true, "during": true, "before": true,
	"after": true, "above": true, "below": true, "to": true, "from": true, "up": true, "down": true,
	"in": true, "out": true, "on": true, "off": true, "over": true, "under": true, "again": true,
	"further": true, "once": true, "here": true, "there": true, "all": true, "any": true, "both": true,
	"each": true, "few": true, "more": true, "most": true, "other": true, "some": true, "such": true,
	"no": true, "nor": true, "not": true, "only": true, "own": true, "same": true, "so": true,
	"than": true, "too": true, "very": true, "can": true, "will": true, "just": true, "don": true,
	"should": true, "now": true, "i": true, "me": true, "my": true, "myself": true, "we": true,
	"our": true, "ours": true, "ourselves": true, "you": true, "your": true, "yours": true,
	"he": true, "him": true, "his": true, "she": true, "her": true, "it": true, "its": true,
	"they": true, "them": true, "their": true, "what": true, "which": true, "who": true, "whom": true,
	"this": true, "that": true, "these": true, "those": true, "am": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true, "have": true, "has": true,
	"had": true, "having": true, "do": true, "does": true, "did": true, "doing": true, "would": true,
	"could": true, "code": true, "file": true, "please": true, "help": true, "want": true, "like": true,
	"make": true, "need": true,
}

// reTrivial is the reference's skip rule: a greeting or a bare sum says nothing
// about which style the user wants.
var reTrivial = regexp.MustCompile(`(?i)^(hi|hello|hey|\d+\s*[+\-*/]\s*\d+)$`)

// Topic is one saver's routing table: the words that make a request about it,
// and the score a text must reach.
type Topic struct {
	Name        string
	Description string
	Triggers    []string
	Keywords    []string
	Threshold   float64
}

// Match reports whether the text is about this topic: the topic's own words
// score against the text, and a trigger phrase found as written adds a fixed
// boost. peers are the other topics scoring beside this one, because the
// reference weighs every term by how many documents use it; passing none scores
// the topic alone.
func (t Topic) Match(query string, peers ...Topic) bool {
	query = strings.TrimSpace(query)
	if query == "" || reTrivial.MatchString(query) {
		return false
	}
	all := peers
	if len(all) == 0 {
		all = []Topic{t}
	}
	doc := documentVector(t, all)
	q := weighted(tokenize(query), nil, 0)
	var dot float64
	for term, w := range q {
		dot += w * doc[term]
	}
	score := math.Min(1, dot*1.8+triggerBoost(t.Triggers, query))
	return score >= t.Threshold
}

// triggerBoost is the reference's fixed bonus for a trigger phrase present in
// the text, ignoring very short ones that would match by accident.
func triggerBoost(triggers []string, query string) float64 {
	lower := strings.ToLower(query)
	for _, trig := range triggers {
		if len(trig) > 3 && strings.Contains(lower, trig) {
			return 0.35
		}
	}
	return 0
}

// documentVector is one document's normalized term weights: the name three
// times, keywords twice, triggers and description once.
func documentVector(t Topic, all []Topic) map[string]float64 {
	tokens := make([]string, 0, 64)
	for i := 0; i < 3; i++ {
		tokens = append(tokens, tokenize(t.Name)...)
	}
	for _, k := range t.Keywords {
		tokens = append(tokens, tokenize(k)...)
		tokens = append(tokens, tokenize(k)...)
	}
	for _, trig := range t.Triggers {
		tokens = append(tokens, tokenize(trig)...)
	}
	tokens = append(tokens, tokenize(t.Description)...)

	df := map[string]int{}
	for _, other := range all {
		seen := map[string]bool{}
		for _, term := range documentTokens(other) {
			if !seen[term] {
				seen[term] = true
				df[term]++
			}
		}
	}
	return weighted(tokens, df, len(all))
}

func documentTokens(t Topic) []string {
	tokens := append([]string{}, tokenize(t.Name)...)
	for _, k := range t.Keywords {
		tokens = append(tokens, tokenize(k)...)
	}
	for _, trig := range t.Triggers {
		tokens = append(tokens, tokenize(trig)...)
	}
	return append(tokens, tokenize(t.Description)...)
}

// weighted turns tokens into tf*idf weights scaled to unit length. A nil df
// with no documents weighs every term by its count alone, which is the query.
func weighted(tokens []string, df map[string]int, docs int) map[string]float64 {
	counts := map[string]int{}
	for _, term := range tokens {
		counts[term]++
	}
	out := make(map[string]float64, len(counts))
	var sumSq float64
	for term, n := range counts {
		idf := math.Log(float64(1+docs)/float64(1+df[term])) + 1
		w := float64(n) * idf
		out[term] = w
		sumSq += w * w
	}
	norm := math.Sqrt(sumSq)
	if norm == 0 {
		return out
	}
	for term := range out {
		out[term] /= norm
	}
	return out
}

// tokenize lower-cases the text, drops punctuation and stop words, and adds the
// stem of each remaining word.
func tokenize(raw string) []string {
	raw = strings.ToLower(text.CutBytes(raw, queryBytes))
	var out []string
	for _, word := range strings.FieldsFunc(raw, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return false
		}
		return true
	}) {
		if len(word) < 2 || stopWords[word] {
			continue
		}
		out = append(out, word)
		if stem := stemWord(word); stem != word && len(stem) > 2 {
			out = append(out, stem)
		}
		if len(out) >= queryTokens {
			break
		}
	}
	return out
}

// stemWord folds the common English endings so "simplify" and "simplified"
// score as one term.
func stemWord(w string) string {
	if len(w) <= 3 {
		return w
	}
	switch {
	case strings.HasSuffix(w, "ies") && len(w) > 4:
		return w[:len(w)-3] + "y"
	case strings.HasSuffix(w, "ing") && len(w) > 5:
		return w[:len(w)-3]
	case strings.HasSuffix(w, "ed") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "es") && len(w) > 4:
		return w[:len(w)-2]
	case strings.HasSuffix(w, "ment") && len(w) > 6:
		return w[:len(w)-4]
	case strings.HasSuffix(w, "tion") && len(w) > 6:
		return w[:len(w)-4]
	case strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && len(w) > 3:
		return w[:len(w)-1]
	}
	return w
}

// LatestUser is the query a smart mode scores: the last user message, cut to
// the routing bound.
func LatestUser(r *translate.Request) string {
	if r == nil {
		return ""
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role != translate.RoleUser {
			continue
		}
		if t := strings.TrimSpace(r.Messages[i].Text()); t != "" {
			return text.CutBytes(t, queryBytes)
		}
	}
	return ""
}
