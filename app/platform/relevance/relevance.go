// Package relevance scores how well a text matches a small set of documents:
// the tokenizer, the stemmer and the tf-idf weighting the skill routers and the
// token savers' smart modes share. It holds no I/O and no state.
package relevance

import (
	"math"
	"regexp"
	"strings"

	"rllmx/app/platform/text"
)

// Bounds on what one text is scored from: the text is cut before it is
// tokenized, so a huge request costs a bounded amount of work.
const (
	QueryBytes  = 4 << 10
	queryTokens = 400
)

// BoostLen is the shortest trigger phrase that counts, and Boost the fixed bonus a
// present trigger adds. A very short phrase would match by accident.
const (
	boostMinLen = 3
	Boost       = 0.35
	// Scale is the factor the cosine is multiplied by before the boost is added.
	Scale = 1.8
)

// trivial is the reference's skip rule: a greeting or a bare sum says nothing
// about which skill or style the user wants.
var trivial = regexp.MustCompile(`(?i)^(hi|hello|hey|\d+\s*[+\-*/]\s*\d+)$`)

// Trivial reports a query that no document should be matched against.
func Trivial(query string) bool { return query == "" || trivial.MatchString(query) }

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

// Tokens lower-cases the text, drops punctuation and stop words, and adds the
// stem of each remaining word.
func Tokens(raw string) []string {
	raw = strings.ToLower(text.CutBytes(raw, QueryBytes))
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

// Weigh turns tokens into tf*idf weights scaled to unit length. A nil df with
// no documents weighs every term by its count alone, which is the query.
func Weigh(tokens []string, df map[string]int, docs int) map[string]float64 {
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

// Cosine is the dot product of two unit-length weight vectors, scaled, plus the
// trigger boost, and capped at one.
func Cosine(query, doc map[string]float64, triggers []string, lowerQuery string) float64 {
	var dot float64
	for term, w := range query {
		dot += w * doc[term]
	}
	return math.Min(1, dot*Scale+TriggerBoost(triggers, lowerQuery))
}

// TriggerBoost is the fixed bonus for a trigger phrase present in the text,
// ignoring very short ones that would match by accident. lowerQuery is already
// folded to lower case.
func TriggerBoost(triggers []string, lowerQuery string) float64 {
	for _, trig := range triggers {
		if len(trig) > boostMinLen && strings.Contains(lowerQuery, strings.ToLower(trig)) {
			return Boost
		}
	}
	return 0
}

// Cut bounds a text to what is scored.
func Cut(s string) string { return text.CutBytes(s, QueryBytes) }
