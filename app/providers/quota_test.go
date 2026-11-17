package providers

import (
	"context"
	"testing"
	"time"

	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
)

func TestAQuotaReadingPresentsTheStoredKey(t *testing.T) {
	conn := repository.Connection{ID: "c1", Provider: "glm", AuthType: "apikey", Data: `{"apiKey":"sk-glm"}`}
	got, err := quotaAccess(context.Background(), &HTTP{}, conn)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "sk-glm" || got.Conn.ID != "c1" {
		t.Fatalf("%+v", got)
	}

	empty := repository.Connection{ID: "c2", Provider: "glm", AuthType: "apikey", Data: `{}`}
	got, err = quotaAccess(context.Background(), &HTTP{}, empty)
	if err != nil || got.Token != "" {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestTheReaderIsBuiltWithAGuardedClient(t *testing.T) {
	h := &HTTP{Upstream: &settings.Upstream{ConnectTimeout: time.Second}}
	reader := newQuotaReader(h)
	if reader.Client == nil || reader.Renew == nil || reader.Access == nil {
		t.Fatalf("%+v", reader)
	}
}
