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

func TestChannelWritesToEveryOpenHandler(t *testing.T) {
	file, term := &tests.Handler{}, &tests.Handler{}
	h, err := NewChannel(&fakeInner{handler: file}, &fakeInner{handler: term}).Handle("logging.channels.single")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Handle(tests.Entry{LevelV: log.LevelInfo, MessageV: "one line"}); err != nil {
		t.Fatal(err)
	}
	if len(file.Entries) != 1 || len(term.Entries) != 1 {
		t.Fatalf("file %d entries, stdout %d", len(file.Entries), len(term.Entries))
	}
}

func TestEachSinkKeepsItsOwnLevel(t *testing.T) {
	file := &tests.Handler{}
	term := &tests.Handler{Min: log.LevelWarning}
	h, _ := NewChannel(&fakeInner{handler: file}, &fakeInner{handler: term}).Handle("logging.channels.single")
	if !h.Enabled(log.LevelInfo) {
		t.Fatal("the channel drops a level one sink accepts")
	}
	if err := h.Handle(tests.Entry{LevelV: log.LevelInfo, MessageV: "one line"}); err != nil {
		t.Fatal(err)
	}
	if len(file.Entries) != 1 || len(term.Entries) != 0 {
		t.Fatalf("file %d entries, stdout %d", len(file.Entries), len(term.Entries))
	}
}

func TestOneFailingSinkDoesNotStopTheNext(t *testing.T) {
	boom := errors.New("disk full")
	file, term := &tests.Handler{Err: boom}, &tests.Handler{}
	h, _ := NewChannel(&fakeInner{handler: file}, &fakeInner{handler: term}).Handle("logging.channels.single")
	err := h.Handle(tests.Entry{LevelV: log.LevelInfo, MessageV: "one line"})
	if !errors.Is(err, boom) {
		t.Fatalf("the file error was lost: %v", err)
	}
	if len(term.Entries) != 1 {
		t.Fatalf("stdout saw %d entries", len(term.Entries))
	}
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
