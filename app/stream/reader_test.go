package stream

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

func collect(t *testing.T, src Source) []Event {
	t.Helper()
	var out []Event
	for {
		ev, err := src.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, ev)
	}
}

func TestReaderFramingAcrossLineEnds(t *testing.T) {
	want := []Event{{Name: "a", Data: []byte(`{"x":1}`)}, {Data: []byte("l1\nl2")}, {Data: []byte("tail")}}
	for name, in := range map[string]string{
		"lf":        "event: a\ndata: {\"x\":1}\n\ndata: l1\ndata: l2\n\ndata: tail\n\n",
		"crlf":      "event: a\r\ndata: {\"x\":1}\r\n\r\ndata: l1\r\ndata: l2\r\n\r\ndata: tail\r\n\r\n",
		"bare cr":   "event: a\rdata: {\"x\":1}\r\rdata: l1\rdata: l2\r\rdata: tail\r\r",
		"bom":       bom + "event: a\ndata: {\"x\":1}\n\ndata: l1\ndata: l2\n\ndata: tail\n\n",
		"no space":  "event:a\ndata:{\"x\":1}\n\ndata:l1\ndata:l2\n\ndata:tail\n\n",
		"comments":  ": hi\nevent: a\nid: 7\nretry: 5\ndata: {\"x\":1}\n\n: x\ndata: l1\ndata: l2\n\ndata: tail\n\n",
		"no tail":   "event: a\ndata: {\"x\":1}\n\ndata: l1\ndata: l2\n\ndata: tail",
		"one byte":  "event: a\r\ndata: {\"x\":1}\r\n\r\ndata: l1\ndata: l2\n\ndata: tail\n\n",
		"mixed end": "event: a\ndata: {\"x\":1}\r\n\r\ndata: l1\rdata: l2\n\ndata: tail\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			var r io.Reader = strings.NewReader(in)
			if name == "one byte" {
				r = iotest.OneByteReader(r)
			}
			got := collect(t, NewReader(r))
			if len(got) != len(want) {
				t.Fatalf("%d events: %q", len(got), got)
			}
			for i := range want {
				if got[i].Name != want[i].Name || string(got[i].Data) != string(want[i].Data) {
					t.Fatalf("event %d: %q %q", i, got[i].Name, got[i].Data)
				}
			}
		})
	}
}

func TestReaderSkipsEventsWithoutData(t *testing.T) {
	got := collect(t, NewReader(strings.NewReader("event: ping\n\n\n\ndata: x\n\n")))
	if len(got) != 1 || string(got[0].Data) != "x" || got[0].Name != "" {
		t.Fatalf("%q", got)
	}
}

func TestAnOversizedEventClosesTheReader(t *testing.T) {
	big := "data: " + strings.Repeat("a", MaxEventBytes+10) + "\n\n"
	r := NewReader(strings.NewReader(big))
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("err %v", err)
	}
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("a failed reader recovered: %v", err)
	}
	var lines []string
	for range 3 {
		lines = append(lines, "data: "+strings.Repeat("b", MaxEventBytes/2))
	}
	r = NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n\n"))
	if _, err := r.Next(); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("many lines of one event passed the cap: %v", err)
	}
}

func TestAReadErrorEndsTheReader(t *testing.T) {
	boom := errors.New("boom")
	r := NewReader(io.MultiReader(strings.NewReader("data: a\n\n"), iotest.ErrReader(boom)))
	if ev, err := r.Next(); err != nil || string(ev.Data) != "a" {
		t.Fatalf("%q %v", ev.Data, err)
	}
	if _, err := r.Next(); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestLinesReadObjectsOnly(t *testing.T) {
	in := bom + "{\"a\":1}\n\n  {\"b\":2}  \r\nnot json\n[1]\n{\"c\":3}"
	got := collect(t, NewLines(strings.NewReader(in)))
	if len(got) != 3 || string(got[1].Data) != `{"b":2}` || string(got[2].Data) != `{"c":3}` {
		t.Fatalf("%q", got)
	}
}

func TestNDJSONWriterHeadersAndLines(t *testing.T) {
	rec := httptest.NewRecorder()
	n, err := NewNDJSON(rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Line(`{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if err := n.Line("{\"a\":\n1}"); !errors.Is(err, ErrLineBreak) {
		t.Fatalf("err %v", err)
	}
	if err := n.Raw([]byte("{\"error\":\"x\"}\n")); err != nil {
		t.Fatal(err)
	}
	if rec.Header().Get("Content-Type") != "application/x-ndjson" || rec.Header().Get("X-Accel-Buffering") != "no" || !rec.Flushed {
		t.Fatalf("headers %v flushed %v", rec.Header(), rec.Flushed)
	}
	if rec.Body.String() != "{\"a\":1}\n{\"error\":\"x\"}\n" {
		t.Fatalf("%q", rec.Body.String())
	}
}

func TestOneHugeLineWithoutAnEndIsRefused(t *testing.T) {
	for name, src := range map[string]Source{
		"sse":    NewReader(strings.NewReader(strings.Repeat("a", MaxEventBytes+10))),
		"ndjson": NewLines(strings.NewReader("{" + strings.Repeat("a", MaxEventBytes+10))),
	} {
		if _, err := src.Next(); !errors.Is(err, ErrEventTooLarge) {
			t.Errorf("%s: err %v", name, err)
		}
	}
}
