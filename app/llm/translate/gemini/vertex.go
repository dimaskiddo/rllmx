package gemini

import (
	"rllmx/app/llm/translate"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// VertexCodec is a Gemini body for Vertex AI. Vertex refuses the id Gemini adds
// to a function call and its response, and checks the thought signature, so the
// writer replaces every signature with Vertex's own placeholder and takes the ids
// off. An answer is read as Gemini's.
type VertexCodec struct{ GeminiCodec }

func init() { translate.Register(VertexCodec{}) }

// Name is the format name.
func (VertexCodec) Name() string { return translate.FormatVertex }

// WriteRequest writes the Gemini body and fits it to Vertex.
func (g VertexCodec) WriteRequest(r *translate.Request, c *translate.Ctx) ([]byte, error) {
	cc := *c
	cc.Safety = false
	body, err := g.GeminiCodec.WriteRequest(r, &cc)
	if err != nil {
		return nil, err
	}
	return fitVertex(body, c.Signature), nil
}

// fitVertex sets the Vertex signature on every part that carries one and removes
// the ids from calls and responses.
func fitVertex(body []byte, signature string) []byte {
	contents := gjson.GetBytes(body, "contents")
	if !contents.IsArray() {
		return body
	}
	for i, turn := range contents.Array() {
		for j, part := range turn.Get("parts").Array() {
			base := "contents." + strconv.Itoa(i) + ".parts." + strconv.Itoa(j)
			if part.Get("thoughtSignature").Exists() && signature != "" {
				body, _ = sjson.SetBytes(body, base+".thoughtSignature", signature)
			}
			if part.Get("functionCall.id").Exists() {
				body, _ = sjson.DeleteBytes(body, base+".functionCall.id")
			}
			if part.Get("functionResponse.id").Exists() {
				body, _ = sjson.DeleteBytes(body, base+".functionResponse.id")
			}
		}
	}
	return body
}
