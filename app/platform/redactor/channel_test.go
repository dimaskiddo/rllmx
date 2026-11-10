package redactor

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/foundation/json"
	"github.com/goravel/framework/log/logger"

	"rllmx/tests"
)

type fakeInner struct {
	handler log.Handler
	err     error
	opened  string
}

func (f *fakeInner) Handle(channel string) (log.Handler, error) {
	f.opened = channel
	return f.handler, f.err
}

func TestChannelWrapsTheInnerHandlerOfTheSameChannelPath(t *testing.T) {
	inner := &tests.Handler{}
	f := &fakeInner{handler: inner}
	h, err := NewChannel(f).Handle("logging.channels.single")
	if err != nil {
		t.Fatal(err)
	}
	if f.opened != "logging.channels.single" {
		t.Fatalf("the inner handler read %q: it takes its path, level and format from the channel", f.opened)
	}
	if err := h.Handle(tests.Entry{LevelV: log.LevelInfo, MessageV: "sk-abcdefghijkl"}); err != nil {
		t.Fatal(err)
	}
	if len(inner.Entries) != 1 || strings.Contains(inner.Entries[0].Message(), "sk-abcdefghijkl") {
		t.Fatalf("the inner handler saw %#v", inner.Entries)
	}
}

func TestChannelReturnsTheInnerError(t *testing.T) {
	boom := errors.New("cannot open the log file")
	if h, err := NewChannel(&fakeInner{err: boom}).Handle("x"); !errors.Is(err, boom) || h != nil {
		t.Fatalf("handler %v error %v", h, err)
	}
}

func TestChannelImplementsTheFrameworksCustomLogger(t *testing.T) {
	var _ log.Logger = NewChannel(&fakeInner{})
}

// The framework's own text handler prints the values a context carries and the
// whole With map. Behind the redactor, neither may carry a secret to the file.
func TestNothingSecretReachesTheFrameworksTextHandler(t *testing.T) {
	type ctxKey string
	for _, formatter := range []string{logger.FormatterText, logger.FormatterJson} {
		var out bytes.Buffer
		file := logger.NewIOHandler(&out, tests.Config{}, json.New(), log.LevelDebug, formatter)
		h, _ := NewChannel(&fakeInner{handler: file}).Handle("logging.channels.single")
		ctx := context.WithValue(context.Background(), ctxKey("api_key"), "sk-ctxsecret1234")
		err := h.Handle(tests.Entry{
			LevelV:   log.LevelError,
			MessageV: "request failed for https://u:pw-9@host/x?key=QUERYSECRET&a=1",
			Ctx:      ctx,
			WithV:    map[string]any{"password": "pw-1", "path": "/api/health", "status": 500},
			RequestV: map[string]any{"authorization": "Bearer abc.def", "method": "GET"},
		})
		if err != nil {
			t.Fatal(err)
		}
		text := out.String()
		for _, leak := range []string{"sk-ctxsecret1234", "pw-9", "QUERYSECRET", "pw-1", "abc.def"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s: %q reached the file:\n%s", formatter, leak, text)
			}
		}
		for _, keep := range []string{"request failed", "/api/health", "GET", "a=1"} {
			if !strings.Contains(text, keep) {
				t.Errorf("%s: %q was lost:\n%s", formatter, keep, text)
			}
		}
	}
}
