package network

import (
	"strconv"
	"strings"
	"time"
)

const altSvcMaxAge = 24 * time.Hour

// parseAltSvc reads one Alt-Svc header value for a request to port. It reports
// an h3 service on this host and port, with its lifetime, or a clear. Only an
// entry with an empty host and the request's own port counts: another host or
// port would move the QUIC dial away from the authority the guard checked.
func parseAltSvc(value, port string) (ttl time.Duration, clear, ok bool) {
	if strings.EqualFold(strings.TrimSpace(value), "clear") {
		return 0, true, true
	}
	for _, entry := range splitOutsideQuotes(value, ',') {
		parts := splitOutsideQuotes(entry, ';')
		name, authority, found := strings.Cut(strings.TrimSpace(parts[0]), "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "h3") {
			continue
		}
		authority = strings.TrimSpace(authority)
		if len(authority) < 2 || authority[0] != '"' || authority[len(authority)-1] != '"' {
			continue
		}
		if authority[1:len(authority)-1] != ":"+port {
			continue
		}
		age, valid := altSvcMaxAgeOf(parts[1:])
		if !valid {
			continue
		}
		if age == 0 {
			return 0, true, true
		}
		return age, false, true
	}
	return 0, false, false
}

// altSvcMaxAgeOf reads the ma parameter. Absent is a day; a number past a day is
// a day; anything that is not an unsigned number makes the entry unusable.
func altSvcMaxAgeOf(params []string) (time.Duration, bool) {
	for _, p := range params {
		key, val, _ := strings.Cut(strings.TrimSpace(p), "=")
		if !strings.EqualFold(strings.TrimSpace(key), "ma") {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" || strings.Trim(val, "0123456789") != "" {
			return 0, false
		}
		secs, err := strconv.ParseUint(val, 10, 64)
		if err != nil || secs > uint64(altSvcMaxAge/time.Second) {
			return altSvcMaxAge, true
		}
		return time.Duration(secs) * time.Second, true
	}
	return altSvcMaxAge, true
}

func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	quoted, start := false, 0
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			quoted = !quoted
		case s[i] == sep && !quoted:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
