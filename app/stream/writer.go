package stream

import (
	"net/http"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"
)

// WriterOf returns the writer a stream handler gives NewSSE. The driver puts a
// *gin.BodyWriter in front of Gin's writer: it has no Unwrap, hides flush
// errors, and copies every byte written into a buffer that lives as long as the
// request. A stream must not go through it, so this steps to the writer behind
// it. Anything else is returned as it is.
func WriterOf(ctx contractshttp.Context) http.ResponseWriter {
	if ctx == nil {
		return nil
	}
	w := ctx.Response().Writer()
	if bw, ok := w.(*goravelgin.BodyWriter); ok {
		return bw.ResponseWriter
	}
	return w
}
