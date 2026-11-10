package conversation

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/store/dialect"
)

const (
	maxIDLength = 256
	// assistantLen is how much of the first assistant text names a conversation,
	// and the least that is needed before it can.
	assistantLen = 50
)

var (
	// tokenPattern is what a session id may look like. Anything else is left out:
	// an id ends up in a header value and in a cache key.
	tokenPattern = regexp.MustCompile(`^[A-Za-z0-9._:\-]+$`)
	claudeUser   = regexp.MustCompile(`_session_([a-f0-9-]+)$`)
	antigravity  = regexp.MustCompile(`(?i)^[a-z]+/([0-9a-f-]{36})/`)
)

// Headers that carry a session id, in order.
var sessionHeaders = []string{"X-Session-Id", "Session-Id", "Session_id", "X-Amp-Thread-Id"}

// Input is what Resolve reads.
type Input struct {
	Header       http.Header
	Body         []byte
	ConnectionID string
}

// usable trims a candidate and drops it when empty, long or not a plain token.
func usable(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > maxIDLength || !tokenPattern.MatchString(v) {
		return ""
	}
	return v
}

func header(h http.Header, name string) string {
	if h == nil {
		return ""
	}
	return usable(h.Get(name))
}

// Resolve returns the conversation a request belongs to. The first source that
// gives a usable id wins: a Claude Code session, an Antigravity session, a
// session header, a body field, a hash of the opening assistant text, and last
// one made-up id per connection.
func (s *Store) Resolve(in Input) string {
	if id := fromClient(in); id != "" {
		return id
	}
	if id := s.fromAssistant(in); id != "" {
		return id
	}
	return s.perConnection(in.ConnectionID)
}

func fromClient(in Input) string {
	user := gjson.GetBytes(in.Body, "metadata.user_id").String()
	if id := claudeSession(user); id != "" {
		return "claude:" + id
	}
	if id := header(in.Header, "X-Claude-Code-Session-Id"); id != "" {
		return "claude:" + id
	}
	if id := antigravitySession(in.Body); id != "" {
		return "antigravity:" + id
	}
	for _, name := range sessionHeaders {
		if id := header(in.Header, name); id != "" {
			return id
		}
	}
	if id := header(in.Header, "X-Client-Request-Id"); id != "" {
		return id
	}
	for _, path := range []string{"prompt_cache_key", "session_id", "conversation_id"} {
		if id := usable(gjson.GetBytes(in.Body, path).String()); id != "" {
			return id
		}
	}
	return usable(user)
}

// claudeSession reads the session out of Claude Code's user id: a suffix
// `_session_<id>`, or a JSON object with `session_id`.
func claudeSession(user string) string {
	if user == "" {
		return ""
	}
	if m := claudeUser.FindStringSubmatch(user); m != nil {
		return usable(m[1])
	}
	if strings.HasPrefix(user, "{") {
		return usable(gjson.Get(user, "session_id").String())
	}
	return ""
}

func antigravitySession(body []byte) string {
	if v := gjson.GetBytes(body, "request.sessionId"); v.Exists() && v.String() != "" {
		return usable(v.String())
	}
	if m := antigravity.FindStringSubmatch(gjson.GetBytes(body, "requestId").String()); m != nil {
		return usable(m[1])
	}
	return ""
}

// fromAssistant names a conversation by its opening assistant text, so two
// conversations that start with the same user prompt do not collide. It needs at
// least assistantLen characters, and the hash is scoped to the connection.
func (s *Store) fromAssistant(in Input) string {
	text := openingAssistantText(in.Body)
	if len(text) < assistantLen {
		return ""
	}
	key := dialect.HashKey(in.ConnectionID, text[:assistantLen])
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.lookup(s.derived, key); ok {
		return id
	}
	id := s.madeUpID()
	s.store(s.derived, key, id)
	return id
}

func (s *Store) perConnection(connectionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.lookup(s.madeUp, connectionID); ok {
		return id
	}
	id := s.madeUpID()
	s.store(s.madeUp, connectionID, id)
	return id
}

// openingAssistantText joins the text of the assistant turns from the start of
// the conversation until it has enough to name it.
func openingAssistantText(body []byte) string {
	var b strings.Builder
	for _, path := range []string{"messages", "input"} {
		items := gjson.GetBytes(body, path)
		if !items.IsArray() {
			continue
		}
		for _, it := range items.Array() {
			if it.Get("role").String() != "assistant" {
				continue
			}
			c := it.Get("content")
			if c.Type == gjson.String {
				b.WriteString(c.String())
			} else {
				for _, part := range c.Array() {
					if t := part.Get("text"); t.Exists() {
						b.WriteString(t.String())
					} else {
						b.WriteString(part.Get("output").String())
					}
				}
			}
			if b.Len() >= assistantLen {
				break
			}
		}
		break
	}
	return b.String()
}
