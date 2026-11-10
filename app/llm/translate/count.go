package translate

import "unicode/utf8"

// CountTokens estimates the input tokens of a request: the runes of every text
// the model reads, over four, rounded up. Images, audio and files add nothing.
func CountTokens(req *Request) int64 {
	var n int64
	for _, m := range req.Messages {
		for _, p := range m.Content {
			if p.Type == PartText {
				n += int64(utf8.RuneCountInString(p.Text))
			}
		}
		n += int64(utf8.RuneCountInString(m.Reasoning))
		for _, c := range m.ToolCalls {
			n += int64(utf8.RuneCountInString(c.Name) + utf8.RuneCountInString(c.Arguments))
		}
	}
	for _, t := range req.Tools {
		n += int64(utf8.RuneCountInString(t.Name) + utf8.RuneCountInString(t.Description) + utf8.RuneCount(t.Parameters))
	}
	return (n + 3) / 4
}
