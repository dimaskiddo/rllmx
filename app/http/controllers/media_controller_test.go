package controllers

import (
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/tests"
)

func TestAMediaCallNeedsAnAPIKey(t *testing.T) {
	for name, handler := range map[string]contractshttp.HandlerFunc{
		"embeddings": (&MediaController{}).Embeddings,
		"images":     (&MediaController{}).Images,
	} {
		r := tests.NewRoute(t)
		r.SetGlobalMiddleware(nil)
		r.Post("/v1/"+name, handler)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(nethttp.MethodPost, "/v1/"+name, strings.NewReader(`{"model":"m"}`))
		req.RemoteAddr = "203.0.113.9:9999"
		r.ServeHTTP(rec, req)
		if rec.Code != nethttp.StatusUnauthorized {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
}
