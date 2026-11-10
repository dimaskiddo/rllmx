package translate

import "strings"

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// segment is a stretch of streamed content and where it belongs.
type segment struct {
	text      string
	reasoning bool
}

// thinkSplitter splits <think>...</think> blocks out of streamed content. A tag
// can arrive in two pieces, so a tail that could still become one is held back
// until the next piece settles it.
type thinkSplitter struct {
	inside bool
	hold   string
}

func (s *thinkSplitter) split(in string) []segment {
	in = s.hold + in
	s.hold = ""
	var out []segment
	for in != "" {
		tag := thinkOpen
		if s.inside {
			tag = thinkClose
		}
		if i := strings.Index(in, tag); i >= 0 {
			if i > 0 {
				out = append(out, segment{text: in[:i], reasoning: s.inside})
			}
			s.inside = !s.inside
			in = in[i+len(tag):]
			continue
		}
		keep := partialTag(in, tag)
		if keep < len(in) {
			out = append(out, segment{text: in[:len(in)-keep], reasoning: s.inside})
		}
		s.hold = in[len(in)-keep:]
		break
	}
	return out
}

// drain releases a held tail at the end of the stream.
func (s *thinkSplitter) drain() []segment {
	if s.hold == "" {
		return nil
	}
	t := s.hold
	s.hold = ""
	return []segment{{text: t, reasoning: s.inside}}
}

// partialTag is how many bytes at the end of in are a proper prefix of tag.
func partialTag(in, tag string) int {
	for n := min(len(tag)-1, len(in)); n > 0; n-- {
		if strings.HasSuffix(in, tag[:n]) {
			return n
		}
	}
	return 0
}
