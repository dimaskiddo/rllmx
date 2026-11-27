package translate

import (
	"testing"

	"rllmx/app/llm/upstream"
)

func userParts(parts ...Part) Message {
	return Message{Role: RoleUser, Content: parts}
}

func TestAStrippedImageLeavesALineThatSaysWhy(t *testing.T) {
	r := &Request{Messages: []Message{
		userParts(Part{Type: PartImage, URL: "old"}),
		{Role: RoleAssistant, Content: TextPart("ok")},
		userParts(TextPart("what is this")[0], Part{Type: PartImage, URL: "new"}),
	}}
	StripUnreadable(r, upstream.Capability{VisionSet: true})
	old, now := r.Messages[0].Content, r.Messages[2].Content
	if len(old) != 1 || old[0].Type != PartText || old[0].Text != earlierImage {
		t.Fatalf("an older turn reads %+v", old)
	}
	if len(now) != 2 || now[0].Text != "what is this" || now[1].Text != omittedImage {
		t.Fatalf("the current turn reads %+v", now)
	}
}

func TestAnImageOnlyTurnIsNeverLeftEmpty(t *testing.T) {
	r := &Request{Messages: []Message{userParts(Part{Type: PartImage, URL: "u"})}}
	StripUnreadable(r, upstream.Capability{VisionSet: true})
	if got := r.Messages[0].Content; len(got) != 1 || got[0].Text != omittedImage {
		t.Fatalf("%+v", got)
	}
}

func TestAReadableImageIsLeftAlone(t *testing.T) {
	r := &Request{Messages: []Message{userParts(Part{Type: PartImage, URL: "u"})}}
	StripUnreadable(r, upstream.Capability{Vision: true})
	if got := r.Messages[0].Content; len(got) != 1 || got[0].Type != PartImage {
		t.Fatalf("%+v", got)
	}
}

func TestAModelTheTablesNeverMentionKeepsTheImage(t *testing.T) {
	r := &Request{Messages: []Message{userParts(Part{Type: PartImage, URL: "u"})}}
	StripUnreadable(r, upstream.Capability{})
	if got := r.Messages[0].Content; len(got) != 1 || got[0].Type != PartImage {
		t.Fatalf("%+v", got)
	}
}
