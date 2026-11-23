package oauth_test

import (
	"errors"
	"reflect"
	"testing"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/dialects"
	"rllmx/app/llm/upstream"
)

func TestOfferMatchesNewFlow(t *testing.T) {
	cases := []struct {
		name     string
		entry    upstream.Entry
		clientID string
		want     oauth.Offer
		wantOK   bool
	}{
		{
			name:  "an entry without a login",
			entry: upstream.Entry{ID: "plain"},
		},
		{
			name:     "a device login with a client id",
			entry:    upstream.Entry{ID: "x", OAuth: &upstream.OAuth{Flow: upstream.FlowDevice}},
			clientID: "client-x",
			want:     oauth.Offer{Flow: upstream.FlowDevice, Enabled: true},
			wantOK:   true,
		},
		{
			name:   "a device login this deployment has not configured",
			entry:  upstream.Entry{ID: "x", OAuth: &upstream.OAuth{Flow: upstream.FlowDevice}},
			want:   oauth.Offer{Flow: upstream.FlowDevice},
			wantOK: true,
		},
		{
			name:   "an import login needs no client id",
			entry:  upstream.Entry{ID: "x", OAuth: &upstream.OAuth{Flow: upstream.FlowImport, Fields: []string{"machineId"}}},
			want:   oauth.Offer{Flow: upstream.FlowImport, Fields: []string{"machineId"}, Enabled: true},
			wantOK: true,
		},
		{
			name:   "a login that brings its own application",
			entry:  upstream.Entry{ID: "x", OAuth: &upstream.OAuth{Flow: upstream.FlowCode, AppFromAccount: true, PAT: true}},
			want:   oauth.Offer{Flow: upstream.FlowCode, AppFromAccount: true, PAT: true, Enabled: true},
			wantOK: true,
		},
		{
			name:   "a login the provider runs without a client",
			entry:  upstream.Entry{ID: "x", OAuth: &upstream.OAuth{Flow: upstream.FlowCode, NoClient: true, Bulk: true}},
			want:   oauth.Offer{Flow: upstream.FlowCode, Bulk: true, Enabled: true},
			wantOK: true,
		},
		{
			name: "a code login with a paste and two variants",
			entry: upstream.Entry{ID: "x", OAuth: &upstream.OAuth{
				Flow: upstream.FlowCodePKCE, Dialect: "kiro-social", NoClient: true,
			}},
			want: oauth.Offer{
				Flow: upstream.FlowCodePKCE, Variants: []string{upstream.VariantGoogle, upstream.VariantGitHub},
				Enabled: true,
			},
			wantOK: true,
		},
		{
			name: "a code login naming a step this build lacks",
			entry: upstream.Entry{ID: "x", OAuth: &upstream.OAuth{
				Flow: upstream.FlowCodePKCE, Dialect: "absent", NoClient: true,
			}},
			want:   oauth.Offer{Flow: upstream.FlowCodePKCE, Enabled: true},
			wantOK: true,
		},
	}

	s := oauth.Service{Dialects: dialects.Default()}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s.ClientID = func(string) string { return c.clientID }
			got, ok := s.Offer(c.entry)
			if ok != c.wantOK {
				t.Fatalf("offered = %v, want %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("offer:\n got %+v\nwant %+v", got, c.want)
			}
			_, err := oauth.NewFlow(c.entry, c.clientID)
			if enabled := !errors.Is(err, oauth.ErrDisabled); enabled != got.Enabled {
				t.Fatalf("enabled = %v while NewFlow said %v", got.Enabled, err)
			}
		})
	}
}

func TestOfferWithNoRegistryOrClientReader(t *testing.T) {
	e := upstream.Entry{ID: "x", OAuth: &upstream.OAuth{Flow: upstream.FlowDevice}}
	got, ok := (&oauth.Service{}).Offer(e)
	if !ok || got.Enabled {
		t.Fatalf("got %+v, offered %v; want a switched-off offer", got, ok)
	}
}
