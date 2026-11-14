package veoaifree

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/media"
	mediatest "rllmx/tests/media"
)

const page = `<html><script>window.data = {"nonce":"9f8e7d6c"};</script></html>`

func rig() *mediatest.Routes {
	return mediatest.NewRoutes(map[string]mediatest.Reply{
		"veoaifree.com/": func() (int, http.Header, []byte) {
			h := http.Header{}
			h.Add("Set-Cookie", "sess=one; Path=/")
			return 200, h, []byte(page)
		},
		"/wp-admin/admin-ajax.php": func() (int, http.Header, []byte) {
			return 200, http.Header{}, []byte("ok")
		},
	})
}

func req(kind, body string) media.Request {
	return media.Request{Kind: kind, Provider: "veoaifree-web", Body: []byte(body),
		Account: map[string]string{"baseUrl": "https://veoaifree.com"}}
}

func TestAnImageAnswerIsNormalized(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(mediatest.PNG)
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"addresses", "https://cdn.example/a.png , https://cdn.example/b.png", `"url":"https://cdn.example/a.png"`},
		{"inline", `"` + encoded + `"`, `"b64_json":"` + encoded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := rig()
			f.Set("/wp-admin/admin-ajax.php", func() (int, http.Header, []byte) {
				return 200, http.Header{}, []byte(tc.reply)
			})
			out, err := Images{}.Direct(context.Background(), req("image", `{"model":"nano-banana","prompt":"a fox","size":"1792x1024"}`), f)
			if err != nil {
				t.Fatalf("Direct: %v", err)
			}
			if !strings.Contains(string(out.Body), tc.want) {
				t.Fatalf("answer = %s, want %s", out.Body, tc.want)
			}
		})
	}
}

func TestAnImageAnswerIsBounded(t *testing.T) {
	var parts []string
	for i := 0; i < maxParts*3; i++ {
		parts = append(parts, "https://cdn.example/"+string(rune('a'+i))+".png")
	}
	urls, _ := imageParts(strings.Join(parts, ","))
	if len(urls) != maxParts {
		t.Fatalf("parts = %d, want %d", len(urls), maxParts)
	}
}

func TestTheFormCarriesTheNonceAndOrientation(t *testing.T) {
	f := rig()
	f.Set("/wp-admin/admin-ajax.php", func() (int, http.Header, []byte) {
		return 200, http.Header{}, []byte("https://cdn.example/a.png")
	})
	if _, err := (Images{}).Direct(context.Background(), req("image", `{"prompt":"a fox","size":"1792x1024"}`), f); err != nil {
		t.Fatalf("Direct: %v", err)
	}
	form := last(f)
	for _, want := range []string{"nonce=9f8e7d6c", "actionType=banan-image-generator", "IMAGE_ASPECT_RATIO_LANDSCAPE"} {
		if !strings.Contains(form, want) {
			t.Fatalf("form = %s, want %s", form, want)
		}
	}
}

func TestAVideoIsPolledAndItsAssetChecked(t *testing.T) {
	f := rig()
	f.Set("/wp-admin/admin-ajax.php", func() (int, http.Header, []byte) {
		if strings.Contains(last(f), "final-video-results") {
			return 200, http.Header{}, []byte(`{"videoUrl":"https://cdn.example/one.mp4"}`)
		}
		return 200, http.Header{}, []byte(`{"sceneData":"scene-1"}`)
	})
	f.Set("cdn.example/one.mp4", func() (int, http.Header, []byte) {
		return 200, http.Header{"Content-Type": []string{"video/mp4"}}, mediatest.MP4
	})
	out, err := Videos{Every: time.Millisecond}.Direct(context.Background(), req("video", `{"model":"veo","prompt":"a wave","size":"1024x1792"}`), f)
	if err != nil {
		t.Fatalf("Direct: %v", err)
	}
	var answer struct {
		Status string `json:"status"`
		Video  struct {
			URL string `json:"url"`
		} `json:"video"`
	}
	if err := json.Unmarshal(out.Body, &answer); err != nil || answer.Status != "completed" || answer.Video.URL != "https://cdn.example/one.mp4" {
		t.Fatalf("answer = %s (%v)", out.Body, err)
	}
	forms := f.Sent()
	if !strings.Contains(forms, "VIDEO_ASPECT_RATIO_PORTRAIT") || !strings.Contains(forms, "actionType=full-video-generate") {
		t.Fatalf("forms = %s", forms)
	}
	if strings.Contains(string(out.Body), "data:video/mp4;base64") {
		t.Fatal("the video was inlined")
	}
}

func TestARedirectIsRefused(t *testing.T) {
	f := rig()
	f.Set("/wp-admin/admin-ajax.php", func() (int, http.Header, []byte) {
		if strings.Contains(last(f), "final-video-results") {
			return 200, http.Header{}, []byte(`{"videoUrl":"https://cdn.example/one.mp4"}`)
		}
		return 200, http.Header{}, []byte(`{"sceneData":"scene-1"}`)
	})
	f.Set("cdn.example/one.mp4", func() (int, http.Header, []byte) {
		return http.StatusFound, http.Header{"Location": []string{"http://127.0.0.1/x.mp4"}}, nil
	})
	_, err := Videos{Every: time.Millisecond}.Direct(context.Background(), req("video", `{"prompt":"x"}`), f)
	if err == nil {
		t.Fatal("the redirect was followed")
	}
}

func TestAnAssetThatIsNotAnMp4IsRefused(t *testing.T) {
	f := rig()
	f.Set("/wp-admin/admin-ajax.php", func() (int, http.Header, []byte) {
		if strings.Contains(last(f), "final-video-results") {
			return 200, http.Header{}, []byte(`{"videoUrl":"https://cdn.example/one.mp4"}`)
		}
		return 200, http.Header{}, []byte(`{"sceneData":"scene-1"}`)
	})
	f.Set("cdn.example/one.mp4", func() (int, http.Header, []byte) {
		return 200, http.Header{"Content-Type": []string{"text/html"}}, []byte("<html>nope</html>")
	})
	_, err := Videos{Every: time.Millisecond}.Direct(context.Background(), req("video", `{"prompt":"x"}`), f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a provider error", err)
	}
}

func TestAMissingTokenIsRefused(t *testing.T) {
	f := rig()
	f.Set("veoaifree.com/", func() (int, http.Header, []byte) { return 200, http.Header{}, []byte("<html>nothing</html>") })
	_, err := Images{}.Direct(context.Background(), req("image", `{"prompt":"x"}`), f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusBadGateway {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "vdiscount") {
		t.Fatal("an internal word reached the error")
	}
}

func TestAQuotaRefusalIsItsStatus(t *testing.T) {
	f := rig()
	f.Set("/wp-admin/admin-ajax.php", func() (int, http.Header, []byte) {
		return http.StatusTooManyRequests, http.Header{}, []byte("slow down")
	})
	_, err := Images{}.Direct(context.Background(), req("image", `{"prompt":"x"}`), f)
	var pe *media.ProviderError
	if !errors.As(err, &pe) || pe.Status != http.StatusTooManyRequests {
		t.Fatalf("err = %v", err)
	}
}

func TestTheOrientationIsReadFromTheSize(t *testing.T) {
	cases := map[string]string{"1792x1024": "LANDSCAPE", "1024x1792": "PORTRAIT", "1024x1024": "SQUARE", "": "SQUARE"}
	for size, want := range cases {
		if got := aspectOf(size); got != want {
			t.Fatalf("aspectOf(%q) = %s, want %s", size, got, want)
		}
	}
}

// last is the body of the most recent call.
func last(f *mediatest.Routes) string {
	b := f.Bodies()
	return b[len(b)-1]
}
