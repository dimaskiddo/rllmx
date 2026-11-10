package executor

import (
	"regexp"
	"testing"
)

var opencodeSessionForm = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

func TestAnOpencodeSessionHasTheShapeTheUpstreamDecodes(t *testing.T) {
	for _, seed := range []string{"opencode\x00generic\x00conv-1", "", "x", "a much longer conversation id that still fits"} {
		got := opencodeSessionId(seed)
		if !opencodeSessionForm.MatchString(got) {
			t.Errorf("%q: %q does not match the shape", seed, got)
		}
	}
}

func TestAnOpencodeSessionIsStableForOneConversation(t *testing.T) {
	first, again := opencodeSessionId("c1"), opencodeSessionId("c1")
	if first != again {
		t.Fatal("one conversation got two ids")
	}
	if opencodeSessionId("c1") == opencodeSessionId("c2") {
		t.Fatal("two conversations share an id")
	}
}

func TestTheOpencodeTimeFieldCarriesASmallCounter(t *testing.T) {
	// The first twelve hex digits after "ses_" decode to a 48-bit field whose
	// low twelve bits are the counter, which must stay small.
	for _, seed := range []string{"a", "b", "c"} {
		id := opencodeSessionId(seed)
		var v uint64
		for _, c := range []byte(id[4:16]) {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= uint64(c - '0')
			default:
				v |= uint64(c-'a') + 10
			}
		}
		// The field is the complement, so a counter of one reads as 0xffe.
		if counter := v & 0xfff; counter != 0xffe {
			t.Errorf("%q: counter field %#x, want the complement of 1", id, counter)
		}
	}
}

func TestTheOpencodeRequestIdIsShapedLikeTheClients(t *testing.T) {
	if got := opencodeRequestID(1_700_000_000_000); !regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`).MatchString(got) {
		t.Fatalf("%q", got)
	}
}
