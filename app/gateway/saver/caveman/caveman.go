// Package caveman holds the terse-answer instruction and the topic it is
// routed by. The saver wires it in and decides when it runs.
package caveman

const shared = " Keep code blocks, file paths, commands, error strings and URLs exactly as written. Security warnings, irreversible-action confirmations and multi-step ordered instructions are written in normal prose, then the terse style resumes. Stay in this style for every reply without drifting back."

// levels are the instructions, from full sentences down to classical Chinese.
var levels = map[string]string{
	"lite":         "Answer tersely. Full sentences and grammar stay, but drop filler, hedging and pleasantries (just, really, basically, sure, of course, I would be happy to). State the thing, the action and the reason, then the next step. Do not restate the question and do not close with an offer to help." + shared,
	"full":         "Answer without filler, articles or framing. Short sentences and fragments are fine when the meaning is clear. Give the thing, the action, the reason, the next step. No preamble, no summary of what you are about to do, no closing pleasantries." + shared,
	"ultra":        "Answer in the fewest words that still carry the meaning: telegraphic, keywords and lists over sentences, framing and restatement omitted. Facts, numbers, names and technical detail stay exact." + shared,
	"wenyan-lite":  "Answer in everyday classical Chinese (文言文), lightly compressed: short clauses, no filler, modern technical terms kept as they are. Meaning first, brevity second." + shared,
	"wenyan":       "Answer in classical Chinese (文言文), compressing hard: four-character phrasing where it fits, function words dropped, technical names and code untouched. The reader must still be able to act on the answer." + shared,
	"wenyan-ultra": "Answer in the most compressed classical Chinese (文言文) that keeps the facts: telegraphic clauses, no transitional wording, no explanation the reader can infer. Technical names, numbers and code stay exact." + shared,
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
