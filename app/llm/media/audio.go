package media

import "strings"

// Upload is the one file a transcription request carries.
type Upload struct {
	Audio       []byte
	Filename    string
	ContentType string
	// Fields are the form fields beside the file.
	Fields map[string]string
}

// audioMIMEs maps a requested format to the type its bytes are served as.
var audioMIMEs = map[string]string{
	"mp3": "audio/mpeg", "mpeg": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg",
	"opus": "audio/opus", "aac": "audio/aac", "flac": "audio/flac", "pcm": "audio/pcm",
}

// AudioMIME names the type of a requested audio format.
func AudioMIME(format string) string {
	if m, ok := audioMIMEs[strings.ToLower(format)]; ok {
		return m
	}
	return "audio/mpeg"
}

// AudioFormat names the format of an upstream answer from its content type, or
// the fallback when the type is unfamiliar.
func AudioFormat(contentType, fallback string) string {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "wav"):
		return "wav"
	case strings.Contains(ct, "mpeg"), strings.Contains(ct, "mp3"):
		return "mp3"
	case strings.Contains(ct, "ogg"):
		return "ogg"
	case strings.Contains(ct, "opus"):
		return "opus"
	case strings.Contains(ct, "pcm"):
		return "pcm"
	}
	return fallback
}

// ParseModelVoice splits a TTS model name into its model and voice. A known
// model id wins first (longest first), then the last slash holds the voice.
func ParseModelVoice(name, defaultModel, defaultVoice string, known []string) (model, voice string) {
	if name == "" {
		return defaultModel, defaultVoice
	}
	longest := ""
	for _, id := range known {
		if id != "" && (id == name || strings.HasPrefix(name, id+"/")) && len(id) > len(longest) {
			longest = id
		}
	}
	if longest != "" {
		if name == longest {
			return longest, defaultVoice
		}
		return longest, strings.TrimPrefix(name, longest+"/")
	}
	if i := strings.LastIndexByte(name, '/'); i > 0 {
		return name[:i], name[i+1:]
	}
	if defaultModel == "" {
		defaultModel = name
	}
	if defaultVoice == "" {
		defaultVoice = name
	}
	return defaultModel, defaultVoice
}
