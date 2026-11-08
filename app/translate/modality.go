package translate

import "rllmx/app/upstream"

// StripUnreadable removes the content a model cannot read, so an upstream is not
// sent an image or a clip it would reject. A message left with nothing keeps an
// empty text part, so the turn still exists and the roles still alternate.
func StripUnreadable(r *Request, c upstream.Capability) {
	for i := range r.Messages {
		m := &r.Messages[i]
		kept := m.Content[:0:0]
		dropped := false
		for _, p := range m.Content {
			if readable(p, c) {
				kept = append(kept, p)
			} else {
				dropped = true
			}
		}
		if dropped {
			if len(kept) == 0 {
				kept = TextPart("")
			}
			m.Content = kept
		}
	}
}

func readable(p Part, c upstream.Capability) bool {
	switch p.Type {
	case PartImage:
		return c.Vision
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
