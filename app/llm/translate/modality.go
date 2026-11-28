package translate

import "rllmx/app/llm/upstream"

// What replaces content a model cannot read. The current turn gets a sentence
// that names the reason; an older turn gets a neutral one, because a combo may
// route the next turn to a model that reads it.
const (
	omittedImage = "[image omitted: model has no vision support]"
	omittedAudio = "[audio omitted: model has no audio support]"
	omittedFile  = "[file omitted: model has no document support]"
	earlierImage = "[Previous image omitted from context.]"
	earlierAudio = "[Previous audio omitted from context.]"
	earlierFile  = "[Previous file omitted from context.]"
)

// StripUnreadable removes the content a model cannot read, so an upstream is not
// sent an image or a clip it would reject, and leaves one line where the part
// was: a turn that held only an image would otherwise go out empty, and the
// answer would be about nothing.
func StripUnreadable(r *Request, c upstream.Capability) {
	start := trailingStart(r.Messages)
	for i := range r.Messages {
		m := &r.Messages[i]
		var dropped [3]bool
		kept := m.Content[:0:0]
		for _, p := range m.Content {
			if readable(p, c) {
				kept = append(kept, p)
				continue
			}
			switch p.Type {
			case PartImage:
				dropped[0] = true
			case PartAudio:
				dropped[1] = true
			case PartFile:
				dropped[2] = true
			}
		}
		if !dropped[0] && !dropped[1] && !dropped[2] {
			continue
		}
		now := i >= start
		for _, line := range []struct {
			dropped bool
			text    [2]string
		}{
			{dropped[0], [2]string{earlierImage, omittedImage}},
			{dropped[1], [2]string{earlierAudio, omittedAudio}},
			{dropped[2], [2]string{earlierFile, omittedFile}},
		} {
			if !line.dropped {
				continue
			}
			if now {
				kept = append(kept, TextPart(line.text[1])...)
			} else {
				kept = append(kept, TextPart(line.text[0])...)
			}
		}
		m.Content = kept
	}
}

func readable(p Part, c upstream.Capability) bool {
	switch p.Type {
	case PartImage:
		return c.ReadsImages()
	case PartAudio:
		return c.AudioInput
	case PartFile:
		switch p.MediaType {
		case "video/mp4", "video/webm", "video/quicktime", "video/mpeg":
			return c.VideoInput
		}
		return c.PDF
	}
	return true
}
