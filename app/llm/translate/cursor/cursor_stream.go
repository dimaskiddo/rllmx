package cursor

import "rllmx/app/llm/translate"

// NewStreamReader returns the reader for a Cursor answer. The service's frames
// are decoded by the executor, which hands the hub one Chat Completions chunk
// per event, so nothing binary reaches the stream machinery.
func (Codec) NewStreamReader(c *translate.Ctx) translate.StreamReader {
	if codec, ok := translate.Lookup(translate.FormatOpenAI); ok {
		return codec.NewStreamReader(c)
	}
	return cursorNoReader{}
}

// NewStreamWriter returns the writer for a Cursor answer. No client speaks this
// format, so a request is never answered in it.
func (Codec) NewStreamWriter(c *translate.Ctx) translate.StreamWriter { return cursorNoWriter{} }

// ReadResponse and WriteResponse refuse a whole answer: the service answers in
// chunks only.
func (Codec) ReadResponse(body []byte, c *translate.Ctx) (*translate.Response, error) {
	return nil, translate.ErrBadBody
}

// WriteResponse refuses to answer in this format.
func (Codec) WriteResponse(r *translate.Response, c *translate.Ctx) ([]byte, error) {
	return nil, translate.ErrBadBody
}

type cursorNoReader struct{}

func (cursorNoReader) Read(event string, data []byte) []translate.Chunk { return nil }
func (cursorNoReader) Flush() []translate.Chunk                         { return nil }

type cursorNoWriter struct{}

func (cursorNoWriter) Write(translate.Chunk) []translate.Frame { return nil }
func (cursorNoWriter) Held() bool                              { return false }
func (cursorNoWriter) Close() []translate.Frame                { return nil }
