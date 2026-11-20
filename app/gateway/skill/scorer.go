package skill

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"rllmx/app/platform/text"
)

// Routing bounds: the latest user text is cut before it is scored, so a huge
// request costs a bounded amount of work.
const (
	queryBytes  = 4 << 10
	queryTokens = 400
)

// reTrivial is the reference's skip rule: a greeting or a bare sum says nothing
// about which skill the user needs.
var reTrivial = regexp.MustCompile(`(?i)^(hi|hello|hey|\d+\s*[+\-*/]\s*\d+)$`)

// stopWords carry no routing signal.
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

// Weights are how many times each field's words enter a skill's document
// vector. The local router ranks a skill's own words; the imported catalog
// leans on its id and name.
type Weights struct {
	ID, Name, Keywords, Triggers, Description int
}

var (
	localWeights = Weights{Name: 3, Keywords: 2, Triggers: 1, Description: 1}
	eccWeights   = Weights{ID: 5, Name: 4, Keywords: 3, Triggers: 3, Description: 1}
)

// Doc is one skill's routing text.
type Doc struct {
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

// Index is a corpus of skill documents, weighted once against each other.
type Index struct {
	docs []vector
}

type vector struct {
	id       string
	terms    map[string]float64
	triggers []string
}

// BuildIndex weights every document against the corpus, so a term many skills
// share counts for less than one a few skills use.
func BuildIndex(docs []Doc, w Weights) Index {
	df := make(map[string]int)
	sets := make([]map[string]bool, len(docs))
	for i, d := range docs {
		set := make(map[string]bool)
		for _, term := range docTokens(d, Weights{ID: 1, Name: 1, Keywords: 1, Triggers: 1, Description: 1}) {
			set[term] = true
		}
		sets[i] = set
		for term := range set {
			df[term]++
		}
	}
	out := Index{docs: make([]vector, len(docs))}
	for i, d := range docs {
		out.docs[i] = vector{
			id:       d.ID,
			terms:    weighted(docTokens(d, w), df, len(docs)),
			triggers: d.Triggers,
		}
	}
	return out
}

// Score returns the documents that clear floor, strongest first. A greeting or
// a bare sum returns nothing, and a query over the bound is cut first.
func (ix Index) Score(query string, floor float64) []Match {
	query = strings.TrimSpace(text.CutBytes(query, queryBytes))
	if query == "" || reTrivial.MatchString(query) {
		return nil
	}
	q := weighted(tokenize(query), nil, 0)
	lower := strings.ToLower(query)
	var out []Match
	for _, d := range ix.docs {
		var dot float64
		for term, w := range q {
			dot += w * d.terms[term]
		}
		score := math.Min(1, dot*1.8+triggerBoost(d.triggers, lower))
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

// triggerBoost is the fixed bonus for a trigger phrase present in the text,
// ignoring very short ones that would match by accident. lowerQuery is already
// folded, as Score does once per request.
func triggerBoost(triggers []string, lowerQuery string) float64 {
	for _, trig := range triggers {
		if len(trig) > 3 && strings.Contains(lowerQuery, strings.ToLower(trig)) {
			return 0.35
		}
	}
	return 0
}

// docTokens repeats each field's words as its weight says.
func docTokens(d Doc, w Weights) []string {
	var out []string
	add := func(n int, s string) {
		for range n {
			out = append(out, tokenize(s)...)
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

// weighted turns tokens into tf*idf weights scaled to unit length. A nil df
// with no documents weighs every term by its count alone, which is the query.
func weighted(tokens []string, df map[string]int, docs int) map[string]float64 {
	counts := make(map[string]int, len(tokens))
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

// stemWord folds common English endings so "simplify" and "simplified" score as
// one term.
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
