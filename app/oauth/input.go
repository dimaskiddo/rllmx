package oauth

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// ErrInput means pasted text holds no usable code.
var ErrInput = errors.New("oauth: the pasted text holds no authorization code")

// A code may carry the tokens themselves (Cline's does), so it can be as long as
// two JWTs.
const (
	maxInput = 32 << 10
	maxCode  = 16 << 10
)

// Input is what a browser brings back from the provider.
type Input struct {
	Code string
	// State is the state found in the input, or "" when it carried none.
	State string
	// Failed is set when the provider reported an error. Its text is dropped.
	Failed bool
}

// ParseInput reads what a user pasted from the address bar, or what the
// callback page forwards: a full address with the code in the query or the
// fragment, a bare code, or `code#state`.
func ParseInput(raw string) (Input, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxInput {
		return Input{}, ErrInput
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		u, err := url.Parse(raw)
		if err != nil {
			return Input{}, ErrInput
		}
		in := fromValues(u.Query())
		if in.Code == "" && !in.Failed {
			if frag, err := url.ParseQuery(u.Fragment); err == nil {
				in = fromValues(frag)
			}
		}
		return check(in)
	}
	code, state, _ := strings.Cut(raw, "#")
	return check(Input{Code: code, State: state})
}

// fromValues reads a decoded query. Decoding turns the plus sign of a base64
// code into a space and no code holds a real space, so a space is put back as
// the plus sign. Pasted text that was never decoded keeps its spaces and is
// refused.
func fromValues(v url.Values) Input {
	code := strings.ReplaceAll(v.Get("code"), " ", "+")
	return Input{Code: code, State: v.Get("state"), Failed: v.Get("error") != ""}
}

func check(in Input) (Input, error) {
	if in.Failed {
		in.Code = ""
		return in, nil
	}
	if in.Code == "" || len(in.Code) > maxCode || len(in.State) > maxCode || hasSpace(in.Code) || hasSpace(in.State) {
		return Input{}, ErrInput
	}
	return in, nil
}

func hasSpace(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}
