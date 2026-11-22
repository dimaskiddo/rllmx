// Package plan decides whether one chat turn needs an architecture plan before
// it is answered: the settings it reads, the classifier, the condensed prompt,
// the plan turn it inserts and the combos it picks. It holds no I/O.
package plan

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

// BypassHeader turns auto plan off for one request.
const BypassHeader = "x-9router-auto-plan"

// The settings' defaults and bounds, as the dashboard offers them. The settings
// API validates a write against the same bounds this reader clamps to.
const (
	DefaultThreshold = 6
	DefaultMaxTokens = 800
	DefaultTimeoutMS = 15000
	MinThreshold     = 1
	MaxThreshold     = 10
	MinMaxTokens     = 200
	MaxMaxTokens     = 4000
	MinTimeoutMS     = 1000
	MaxTimeoutMS     = 60000
)

// maxScan bounds the text the classifier reads. A keyword past this point sits
// deep enough that the prompt is already far longer than an ordinary ask.
const maxScan = 16 << 10

// minWords is the length below which a turn is never an architecture task.
const minWords = 8

// smartConfidence is the heuristic confidence above which its verdict stands
// without asking a model.
const smartConfidence = 0.75

// comboRefMax bounds a stored combo id or name.
const comboRefMax = 128

// Settings is this request's auto plan configuration, already clamped.
type Settings struct {
	Enabled       bool
	Mode          string // auto | manual
	PlanCombo     string // a combo id or name
	CodeCombo     string
	Threshold     int // 1 to 10, as the dashboard slider
	SmartClassify bool
	MaxTokens     int
	TimeoutMS     int
}

// Read projects the settings row. A missing or unusable value takes its
// default; a value outside its bounds is clamped, because a stored row may
// predate the bounds or have been edited by hand.
func Read(doc document.Doc) Settings {
	s := Settings{
		Enabled:       doc.Bool("autoPlanEnabled", false),
		Mode:          "auto",
		Threshold:     clamp(doc.Int("autoPlanComplexityThreshold", DefaultThreshold), MinThreshold, MaxThreshold),
		SmartClassify: doc.Bool("autoPlanSmartClassify", false),
		MaxTokens:     clamp(doc.Int("autoPlanMaxTokens", DefaultMaxTokens), MinMaxTokens, MaxMaxTokens),
		TimeoutMS:     clamp(doc.Int("autoPlanTimeoutMs", DefaultTimeoutMS), MinTimeoutMS, MaxTimeoutMS),
	}
	if v, ok := doc.GetString("autoPlanMode"); ok && v == "manual" {
		s.Mode = "manual"
	}
	s.PlanCombo = comboRef(doc, "autoPlanComboId")
	s.CodeCombo = comboRef(doc, "autoCodeComboId")
	return s
}

// comboRef reads a stored combo reference; one that could not have been written
// by the settings API is ignored rather than passed on.
func comboRef(doc document.Doc, key string) string {
	v, ok := doc.GetString(key)
	if !ok {
		return ""
	}
	v = strings.TrimSpace(v)
	if len(v) > comboRefMax || text.HasControl(v) {
		return ""
	}
	return v
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Verdict is what one classification decided, and why. Score is the heuristic's
// 0 to 1 complexity; Reason names the skip or the deciding tier.
type Verdict struct {
	NeedsPlan  bool
	Score      float64
	Confidence float64
	Reason     string
}

// The build words, by-hand re-authoring of the reference's list: an English or
// Indonesian verb that asks for something to be made, changed or sized.
var buildPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bbuild\b`),
	regexp.MustCompile(`(?i)\bimplement\b`),
	regexp.MustCompile(`(?i)\bcreate app\b`),
	regexp.MustCompile(`(?i)\brefactor\b`),
	regexp.MustCompile(`(?i)\barchitect(ure)?\b`),
	regexp.MustCompile(`(?i)\bdesign system\b`),
	regexp.MustCompile(`(?i)\bendpoint\b`),
	regexp.MustCompile(`(?i)\bmodule\b`),
	regexp.MustCompile(`(?i)\bfull-stack\b`),
	regexp.MustCompile(`(?i)\bmicroservice\b`),
	regexp.MustCompile(`(?i)\bbangun\b`),
	regexp.MustCompile(`(?i)\bbuatkan\b`),
	regexp.MustCompile(`(?i)\brancang\b`),
	regexp.MustCompile(`(?i)\barsitektur\b`),
	regexp.MustCompile(`(?i)\bsistem\b`),
	regexp.MustCompile(`(?i)\bfitur baru\b`),
	regexp.MustCompile(`(?i)\baplikasi\b`),
}

// scopePatterns say the work crosses more than one place, which is what makes
// planning worth its tokens.
var scopePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(across|multiple|several|many)\s+files?\b`),
	regexp.MustCompile(`(?i)\b(beberapa|banyak)\s+file\b`),
	regexp.MustCompile(`(?i)\bend-to-end\b`),
	regexp.MustCompile(`(?i)\be2e\b`),
	regexp.MustCompile(`(?i)\bfull feature\b`),
	regexp.MustCompile(`(?i)\b(frontend|client).*(backend|server|api|database|db)\b`),
	regexp.MustCompile(`(?i)\b(backend|server|api|database|db).*(frontend|client)\b`),
	regexp.MustCompile(`(?i)\b(auth|authentication).*(database|db|session|token)\b`),
	regexp.MustCompile(`\b\d+\.\s+[A-Za-z]`),
}

// questionPatterns mark a turn as a question or a small correction, which a plan
// would only delay.
var questionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(why|how does|explain|jelaskan|kenapa|mengapa)\b`),
	regexp.MustCompile(`(?i)\b(typo|fix typo|one-line|spelling|syntax error)\b`),
	regexp.MustCompile(`(?i)\b(what is|apa itu|apakah)\b`),
}

// classify is the always-on tier: a pure heuristic over the conversation, with
// the reference's skip reasons.
func classify(r *translate.Request, s Settings) Verdict {
	if !s.Enabled {
		return Verdict{Confidence: 1, Reason: "disabled"}
	}
	if r == nil || len(r.Messages) == 0 {
		return Verdict{Confidence: 1, Reason: "empty_messages"}
	}
	// One pass, parts read in place: an agent session is long, and joining every
	// message's text to find a marker would cost more than the verdict is worth.
	toolLoop := false
	for _, m := range r.Messages {
		if m.Role == translate.RoleTool || len(m.ToolCalls) > 0 {
			toolLoop = true
		}
		for _, p := range m.Content {
			if p.Type == translate.PartText && hasPlan(p.Text) {
				return Verdict{Confidence: 1, Reason: "already_planned"}
			}
		}
	}
	if toolLoop {
		return Verdict{Confidence: 1, Reason: "agent_tool_loop"}
	}
	body := text.CutBytes(r.LastUserText(), maxScan)
	if body == "" {
		return Verdict{Confidence: 1, Reason: "no_user_turn"}
	}
	words := len(strings.Fields(body))
	if words < minWords {
		return Verdict{Confidence: 0.95, Score: 0.1, Reason: "too_short"}
	}
	builds := countMatches(buildPatterns, body)
	if anyMatches(questionPatterns, body) && (builds == 0 || words < 25) {
		return Verdict{Confidence: 0.9, Score: 0.1, Reason: "exclusion_pattern"}
	}
	scopes := countMatches(scopePatterns, body)

	score := 0.0
	for _, at := range []int{12, 25, 50} {
		if words >= at {
			score += 0.15
		}
	}
	score += min(0.4, float64(builds)*0.2)
	score += min(0.4, float64(scopes)*0.2)
	if builds > 0 && scopes > 0 {
		score += 0.2
	}
	score = min(1, max(0, score))

	threshold := float64(clamp(s.Threshold, MinThreshold, MaxThreshold)) / 10
	needs := score >= threshold
	reason := "below_threshold"
	if needs {
		reason = "heuristic_match"
	}
	return Verdict{
		NeedsPlan:  needs,
		Score:      score,
		Confidence: min(1, 0.5+math.Abs(score-threshold)),
		Reason:     reason,
	}
}

// Judge asks one bounded question about the last user turn and returns the
// model's answer. ok is false when the call could not be made or read.
type Judge func(ctx context.Context, question string) (answer string, ok bool)

// Decide answers whether this turn needs a plan: the heuristic, then, when the
// heuristic is unsure and the setting is on, one YES/NO model call. Every
// failure keeps the heuristic's verdict.
func Decide(ctx context.Context, r *translate.Request, s Settings, j Judge) Verdict {
	v := classify(r, s)
	if !s.SmartClassify || j == nil || v.Confidence >= smartConfidence {
		return v
	}
	if v.Reason != "heuristic_match" && v.Reason != "below_threshold" {
		return v
	}
	answer, ok := j(ctx, question(text.CutBytes(r.LastUserText(), maxScan)))
	if !ok {
		return v
	}
	if strings.Contains(strings.ToUpper(answer), "YES") {
		return Verdict{NeedsPlan: true, Score: 0.9, Confidence: 0.9, Reason: "tier2"}
	}
	return Verdict{NeedsPlan: false, Score: 0.1, Confidence: 0.9, Reason: "tier2"}
}

// question is the one-shot classifier question, bounded to the turn's opening.
func question(body string) string {
	return `Jawab hanya YES atau NO: apakah task berikut butuh perencanaan arsitektur multi-langkah sebelum ditulis kodenya?` +
		"\nTask: \"" + text.CutRunes(body, 300) + `"`
}

// countMatches counts the patterns that match, once each as the reference does.
func countMatches(patterns []*regexp.Regexp, body string) int {
	n := 0
	for _, p := range patterns {
		if p.MatchString(body) {
			n++
		}
	}
	return n
}

func anyMatches(patterns []*regexp.Regexp, body string) bool {
	for _, p := range patterns {
		if p.MatchString(body) {
			return true
		}
	}
	return false
}

// String renders the verdict for the DEBUG line, never the text it read.
func (v Verdict) String() string {
	return fmt.Sprintf("%s score=%.2f conf=%.2f", v.Reason, v.Score, v.Confidence)
}
