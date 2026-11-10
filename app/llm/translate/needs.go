package translate

import "rllmx/app/llm/upstream"

// needs are the input kinds the current turn carries.
type needs struct {
	Vision bool
	PDF    bool
	Audio  bool
	Video  bool
}

// Any reports whether the turn needs anything beyond text.
func (n needs) Any() bool { return n.Vision || n.PDF || n.Audio || n.Video }

// detectNeeds reads the current turn only: the messages after the last
// assistant turn. An image in an older turn never counts, so a long chat that
// once held a picture is not forced onto a vision model forever.
func detectNeeds(r *Request) needs {
	var n needs
	start := 0
	for i := len(r.Messages) - 1; i >= 0; i-- {
		if r.Messages[i].Role == RoleAssistant {
			start = i + 1
			break
		}
	}
	for _, m := range r.Messages[start:] {
		for _, p := range m.Content {
			switch p.Type {
			case PartImage:
				n.Vision = true
			case PartAudio:
				n.Audio = true
			case PartFile:
				switch p.MediaType {
				case "video/mp4", "video/webm", "video/quicktime", "video/mpeg":
					n.Video = true
				default:
					n.PDF = true
				}
			}
		}
	}
	return n
}

// Met reports whether a model's capability row covers every need.
func (n needs) Met(c upstream.Capability) bool {
	return (!n.Vision || c.Vision) && (!n.PDF || c.PDF) && (!n.Audio || c.AudioInput) && (!n.Video || c.VideoInput)
}
