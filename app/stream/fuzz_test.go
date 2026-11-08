package stream

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"rllmx/app/translate"
)

func FuzzSSEReader(f *testing.F) {
	for _, s := range []string{"data: a\n\n", "event: x\r\ndata: {}\r\n\r\n", "\xef\xbb\xbfdata: a\rdata: b\r\r", ": c\nid: 1\n\n", "data"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		r := NewReader(bytes.NewReader(in))
		for range len(in) + 2 {
			if _, err := r.Next(); err != nil {
				return
			}
		}
		t.Fatal("the reader never ended")
	})
}

func FuzzNDJSONReader(f *testing.F) {
	for _, s := range []string{"{\"a\":1}\n", "{}\r\n{}", "x\n[1]\n{\"b\"", ""} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		l := NewLines(bytes.NewReader(in))
		for range len(in) + 2 {
			ev, err := l.Next()
			if err != nil {
				return
			}
			if len(ev.Data) == 0 || ev.Data[0] != '{' {
				t.Fatalf("line %q is not an object", ev.Data)
			}
		}
		t.Fatal("the reader never ended")
	})
}

func FuzzAddClientBuffer(f *testing.F) {
	for _, s := range []string{`{"usage":{"prompt_tokens":3}}`, `{"type":"message_start","message":{"usage":{"input_tokens":1}}}`, `{"done":true,"prompt_eval_count":2}`, `[`, ``} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		for _, format := range translate.Formats() {
			out := AddClientBuffer(format, []byte(in))
			if json.Valid([]byte(in)) != json.Valid(out) {
				t.Fatalf("%s: validity changed: %q -> %q", format, in, out)
			}
			if !json.Valid([]byte(in)) && string(out) != in {
				t.Fatalf("%s: a payload that is not JSON was changed", format)
			}
		}
	})
}

func FuzzPump(f *testing.F) {
	f.Add("data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", uint8(0), uint8(1))
	f.Add("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n", uint8(1), uint8(0))
	f.Fuzz(func(t *testing.T, in string, u, d uint8) {
		formats := translate.Formats()
		up, down := formats[int(u)%len(formats)], formats[int(d)%len(formats)]
		body := io.NopCloser(bytes.NewReader([]byte(in)))
		Run(t.Context(), Job{Body: body, Source: NewReader(body), UpstreamFormat: up, ClientFormat: down, Ctx: translate.NewCtx(down, up), Client: &fakeClient{}})
	})
}
