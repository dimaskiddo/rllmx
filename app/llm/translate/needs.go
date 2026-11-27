package translate

import "rllmx/app/llm/upstream"

// Needs are the input kinds the current turn carries.
type Needs struct {
	Vision bool
	PDF    bool
	Audio  bool
	Video  bool
}

// Any reports whether the turn needs anything beyond text.
func (n Needs) Any() bool { return n.Vision || n.PDF || n.Audio || n.Video }

// trailingStart is where the current turn begins: the messages after the last
// assistant turn.
func trailingStart(messages []Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleAssistant {
			return i + 1
		}
	}
	return 0
}

// DetectNeeds reads the current turn only: the messages after the last
// assistant turn. An image in an older turn never counts, so a long chat that
// once held a picture is not forced onto a vision model forever.
func DetectNeeds(r *Request) Needs {
	var n Needs
	for _, m := range r.Messages[trailingStart(r.Messages):] {
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

// Met reports whether a model's capability row covers every need. Vision is met
// by a model the tables say nothing about, because the request tries it rather
// than stripping the image.
func (n Needs) Met(c upstream.Capability) bool {
	return (!n.Vision || c.Vision || !c.VisionSet) && (!n.PDF || c.PDF) && (!n.Audio || c.AudioInput) && (!n.Video || c.VideoInput)
}
