package skill

import (
	"sort"
	"strings"

	"rllmx/app/platform/relevance"
)

// weights are how many times each field's words enter a skill's document
// vector. The local router ranks a skill's own words; the imported catalog
// leans on its id and name.
type weights struct {
	ID, Name, Keywords, Triggers, Description int
}

var (
	localWeights = weights{Name: 3, Keywords: 2, Triggers: 1, Description: 1}
	eccWeights   = weights{ID: 5, Name: 4, Keywords: 3, Triggers: 3, Description: 1}
)

// doc is one skill's routing text.
type doc struct {
	ID          string
	Name        string
	Description string
	Triggers    []string
	Keywords    []string
}

// Match is one skill the query scored high enough for.
type Match struct {
	ID    string
	Score float64
}

// index is a corpus of skill documents, weighted once against each other.
type index struct {
	docs []vector
}

type vector struct {
	id       string
	terms    map[string]float64
	triggers []string
}

// buildIndex weights every document against the corpus, so a term many skills
// share counts for less than one a few skills use.
func buildIndex(docs []doc, w weights) index {
	df := make(map[string]int)
	sets := make([]map[string]bool, len(docs))
	for i, d := range docs {
		set := make(map[string]bool)
		for _, term := range docTokens(d, weights{ID: 1, Name: 1, Keywords: 1, Triggers: 1, Description: 1}) {
			set[term] = true
		}
		sets[i] = set
		for term := range set {
			df[term]++
		}
	}
	out := index{docs: make([]vector, len(docs))}
	for i, d := range docs {
		out.docs[i] = vector{
			id:       d.ID,
			terms:    relevance.Weigh(docTokens(d, w), df, len(docs)),
			triggers: d.Triggers,
		}
	}
	return out
}

// Score returns the documents that clear floor, strongest first. A greeting or
// a bare sum returns nothing, and a query over the bound is cut first.
func (ix index) Score(query string, floor float64) []Match {
	query = strings.TrimSpace(relevance.Cut(query))
	if relevance.Trivial(query) {
		return nil
	}
	q := relevance.Weigh(relevance.Tokens(query), nil, 0)
	lower := strings.ToLower(query)
	var out []Match
	for _, d := range ix.docs {
		score := relevance.Cosine(q, d.terms, d.triggers, lower)
		if score >= floor {
			out = append(out, Match{ID: d.id, Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// docTokens repeats each field's words as its weight says.
func docTokens(d doc, w weights) []string {
	var out []string
	add := func(n int, s string) {
		for range n {
			out = append(out, relevance.Tokens(s)...)
		}
	}
	add(w.ID, d.ID)
	add(w.Name, d.Name)
	for _, k := range d.Keywords {
		add(w.Keywords, k)
	}
	for _, t := range d.Triggers {
		add(w.Triggers, t)
	}
	add(w.Description, d.Description)
	return out
}
