package providers

import (
	"strings"
	"testing"
)

// A warm-up must land on the connection whose window it read: another account
// of the provider would take the lock for a refusal that was not its own.
func TestAWarmUpIsPinnedToTheConnectionWhoseWindowItReads(t *testing.T) {
	in := pingInput("conn-1", "claude", "claude-opus-5-5", "autoping-1")
	if in.Connection != "conn-1" {
		t.Fatalf("connection %q", in.Connection)
	}
	if !strings.Contains(string(in.Body), `"model":"claude/claude-opus-5-5"`) {
		t.Fatalf("body %s", in.Body)
	}
	if in.Path != "/v1/chat/completions" || in.RequestID != "autoping-1" {
		t.Fatalf("%+v", in)
	}
}
