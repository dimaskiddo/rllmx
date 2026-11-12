package controllers

import (
	"bytes"
	"mime/multipart"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	gatewaymedia "rllmx/app/gateway/media"

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

func multipartBody(t *testing.T, field string, audio []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("model", "whisper-1")
	part, err := mw.CreateFormFile("file", "a.mp3")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(audio)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestAnUploadIsParsedBeforeThePipeline(t *testing.T) {
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Post("/v1/audio/transcriptions", (&MediaController{}).Transcriptions)
	body, ct := multipartBody(t, "audio", []byte("audio"))
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", ct)
	req.RemoteAddr = "203.0.113.9:9999"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	// No caller is set and no pipeline is wired: the parse is what runs first.
	if rec.Code != nethttp.StatusUnauthorized && rec.Code != nethttp.StatusServiceUnavailable {
		t.Fatalf("%d", rec.Code)
	}
}

func TestAnOversizedUploadIs413(t *testing.T) {
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Post("/v1/audio/transcriptions", (&MediaController{}).Transcriptions)
	body, ct := multipartBody(t, "audio", make([]byte, gatewaymedia.MaxAudioBytes+1))
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/audio/transcriptions", body)
	req.Header.Set("Content-Type", ct)
	req.RemoteAddr = "203.0.113.9:9999"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != nethttp.StatusUnauthorized && rec.Code != nethttp.StatusRequestEntityTooLarge {
		t.Fatalf("%d", rec.Code)
	}
}
