// Package prompt is the shared half of the prompt savers: the topic that decides
// when a smart mode injects. Each saver keeps its own levels and topic and calls
// in here; the scoring itself is the shared relevance package.
package prompt

import (
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/relevance"
)

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
	if relevance.Trivial(query) {
		return false
	}
	all := peers
	if len(all) == 0 {
		all = []Topic{t}
	}
	q := relevance.Weigh(relevance.Tokens(query), nil, 0)
	score := relevance.Cosine(q, documentVector(t, all), t.Triggers, strings.ToLower(query))
	return score >= t.Threshold
}

// documentVector is one document's normalized term weights: the name three
// times, keywords twice, triggers and description once.
func documentVector(t Topic, all []Topic) map[string]float64 {
	tokens := make([]string, 0, 64)
	for i := 0; i < 3; i++ {
		tokens = append(tokens, relevance.Tokens(t.Name)...)
	}
	for _, k := range t.Keywords {
		tokens = append(tokens, relevance.Tokens(k)...)
		tokens = append(tokens, relevance.Tokens(k)...)
	}
	for _, trig := range t.Triggers {
		tokens = append(tokens, relevance.Tokens(trig)...)
	}
	tokens = append(tokens, relevance.Tokens(t.Description)...)

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
	return relevance.Weigh(tokens, df, len(all))
}

func documentTokens(t Topic) []string {
	tokens := append([]string{}, relevance.Tokens(t.Name)...)
	for _, k := range t.Keywords {
		tokens = append(tokens, relevance.Tokens(k)...)
	}
	for _, trig := range t.Triggers {
		tokens = append(tokens, relevance.Tokens(trig)...)
	}
	return append(tokens, relevance.Tokens(t.Description)...)
}

// LatestUser is the query a smart mode scores: the last user message, cut to
// the routing bound.
func LatestUser(r *translate.Request) string {
	return relevance.Cut(r.LastUserText())
}
