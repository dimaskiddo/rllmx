package minimax

import (
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func FuzzMiniMaxGrant(f *testing.F) {
	for _, s := range []string{`{}`, `{"user_code":"u","verification_uri":"https://a.example/x","expired_in":1}`, `{"device_code":"d","user_code":"u","verification_uri":"https://a.example/x","interval":-5,"expires_in":99999999999}`, `[`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		body := gjson.ParseBytes(raw)
		if d := minimaxDeadline(body, time.Unix(1_700_000_000, 0)); d <= 0 || d > 30*time.Minute {
			t.Fatalf("life %v", d)
		}
		for _, by := range []bool{true, false} {
			if i := minimaxInterval(body, by); i < time.Second || i > time.Minute {
				t.Fatalf("interval %v", i)
			}
		}
	})
}
