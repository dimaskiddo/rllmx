// Package qoder holds what the Qoder login and quota reader share: the account
// hosts and the personal-token exchange.
package qoder

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
)

// OpenAPI names the account host of each site.
var OpenAPI = map[string]string{
	"qoder":    "https://openapi.qoder.sh",
	"qoder-cn": "https://openapi.qoder.com.cn",
}

// Do sends one request and answers its status and body.
type Do func(ctx context.Context, method, url string, body []byte, headers map[string]string) (int, []byte, error)

// ErrRefused means the personal token was not accepted.
var ErrRefused = errors.New("qoder: the personal token was refused")

// Exchange trades a personal token for a job token. The answer names when it
// stops being usable; an answer without that takes a day.
func Exchange(ctx context.Context, do Do, base, personal string) (string, time.Time, error) {
	code, raw, err := do(ctx, http.MethodPost, base+"/api/v1/jobToken/exchange",
		[]byte(`{"personal_token":`+strconv.Quote(personal)+`}`), map[string]string{
			"User-Agent": "qodercli/1.0.0", "Cosy-Version": "1.0.0", "Cosy-ClientType": "5",
		})
	if err != nil {
		return "", time.Time{}, err
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		return "", time.Time{}, ErrRefused
	}
	if code != http.StatusOK {
		return "", time.Time{}, errors.New("qoder: the exchange failed")
	}
	data := gjson.ParseBytes(raw)
	token := data.Get("token").String()
	if token == "" {
		return "", time.Time{}, errors.New("qoder: the exchange answered no token")
	}
	until := time.Now().Add(24 * time.Hour)
	if at := data.Get("expires_at").String(); at != "" {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			until = t
		}
	} else if secs := data.Get("expires_in").Int(); secs > 0 {
		until = time.Now().Add(time.Duration(secs) * time.Second)
	}
	return token, until, nil
}
