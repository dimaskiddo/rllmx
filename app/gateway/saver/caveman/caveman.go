// Package caveman holds the terse-answer instruction and the topic it is
// routed by. The saver wires it in and decides when it runs. The level texts
// are the reference's own (the caveman skill, MIT, github.com/JuliusBrussee/caveman),
// kept verbatim as the maintainer directed; an edit tracks the reference.
package caveman

import "strings"

const (
	sharedExamples = "Not: \"Sure! I'd be happy to help you with that. The issue you're experiencing is likely caused by...\" Yes: \"Bug in auth middleware. Token expiry check use `<` not `<=`. Fix:\""

	sharedBoundaries = "Code blocks, file paths, commands, errors, URLs: keep exact. Security warnings, irreversible action confirmations, multi-step ordered sequences: write normal. Resume terse style after."

	sharedAutoClarity = "Auto-Clarity: drop caveman for security warnings, irreversible actions, multi-step sequences where fragment ambiguity risks misread, or when user repeats a question. Resume after the clear part."

	sharedPersistence = "ACTIVE EVERY RESPONSE. No revert after many turns. No filler drift. Still active if unsure."

	sharedNoInventedAbbrev = "No invented abbreviations. Standard well-known tech acronyms (DB, API, HTTP, URL, JSON, ID, OS, CPU) OK. Names of code symbols, function names, API names, error strings: keep verbatim."

	sharedPreserveLanguage = "Preserve the user's dominant language. User wrote Vietnamese, reply Vietnamese. User wrote English, reply English. Wenyan/classical-Chinese levels override this language-preservation rule. Code identifiers, error strings, file paths, commands: keep in their original form regardless of language."

	sharedNoSelfReference = "No self-reference. Do not name or announce the style (no \"caveman mode\", no \"me caveman think\", no \"compressed mode active\"). Just respond."

	sharedNoDecoration = "No decorative emoji. No narrating tool calls (\"I will now search\", \"I used X to find Y\"). No status phrases (\"Sure!\", \"Of course!\", \"I'd be happy to\"). No causal arrow shorthand (\"A -> B -> fails\"). State the thing, the action, the reason. Then next step."
)

// shared is every boundary the reference carries after the level's own lines.
var shared = []string{
	sharedExamples, sharedBoundaries, sharedAutoClarity, sharedPersistence,
	sharedNoInventedAbbrev, sharedPreserveLanguage, sharedNoSelfReference, sharedNoDecoration,
}

func level(own ...string) string { return strings.Join(append(own, shared...), " ") }

// levels are the instructions, from full sentences down to classical Chinese.
var levels = map[string]string{
	"lite": level(
		"Respond tersely. Keep grammar and full sentences but drop filler, hedging and pleasantries (just/really/basically/sure/of course/I'd be happy to).",
		"Pattern: state the thing, the action, the reason. Then next step.",
	),
	"full": level(
		"Respond like terse caveman. All technical substance stay exact, only fluff die.",
		"Drop: articles (a/an/the), filler (just/really/basically/actually/simply), pleasantries, hedging. Fragments OK. Short synonyms (big not extensive, fix not implement a solution for).",
		"Pattern: [thing] [action] [reason]. [next step].",
	),
	"ultra": level(
		"Respond ultra-terse. Maximum compression. Telegraphic.",
		"Strip conjunctions. One word when one word enough.",
		"Pattern: [thing] [action] [reason]. [next step].",
	),
	"wenyan-lite": level(
		"Respond semi-classical. Drop filler/hedging but keep grammar structure, classical register.",
		"Use classical Chinese sentence patterns where natural. Keep English for technical terms.",
	),
	"wenyan": level(
		"Respond classical Chinese (文言文). Maximum classical terseness. 80-90% character reduction.",
		"Classical sentence patterns, verbs precede objects, subjects often omitted, classical particles (之/乃/為/其).",
		"Keep English for code, commands, function names, API names, error strings.",
	),
	"wenyan-ultra": level(
		"Respond extreme classical compression (文言文 ultra). Maximum compression, ultra terse.",
		"Same classical rules as wenyan-full but even more compressed. One classical particle per clause.",
	),
}

// Level is the instruction for a level, or "" for a name this package does not
// know.
func Level(name string) string { return levels[name] }

// The routing table the smart activation mode scores. It is data only: the
// saver pairs it with the shared scorer, so this package needs no import.
var (
	Description = "terse answers with less filler and fewer pleasantries"
	Triggers    = []string{
		"terse", "be concise", "short answer", "briefly", "no filler",
		"drop pleasantries", "caveman style",
	}
	Keywords  = []string{"terse", "concise", "brief", "short", "compression", "tokens", "filler"}
	Threshold = 0.35
)
