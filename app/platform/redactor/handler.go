package redactor

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/log"
)

// Handler is a log.Handler that scrubs an entry and passes it to another
// handler. Every field that can carry text is cleaned, so nothing depends on
// the caller remembering not to log a secret.
type Handler struct {
	next log.Handler
	tee  func(log.Entry)
}

// NewHandler puts the scrub between a caller and the channel that writes.
func NewHandler(next log.Handler) *Handler {
	return &Handler{next: next}
}

// Tee hands every clean entry to fn before the channel writes it, so a second
// reader such as the console ring only ever sees scrubbed text.
func (h *Handler) Tee(fn func(log.Entry)) { h.tee = fn }

// Enabled follows the wrapped handler.
func (h *Handler) Enabled(l log.Level) bool { return h.next.Enabled(l) }

// Handle cleans e and hands the copy to the wrapped handler.
func (h *Handler) Handle(e log.Entry) error {
	clean := cleanEntry(e)
	if h.tee != nil {
		h.tee(clean)
	}
	return h.next.Handle(clean)
}

// entry is a log.Entry whose fields are already clean. Its context is empty on
// purpose: the framework's file handler prints the values a context carries,
// and a request context can hold a credential.
type entry struct {
	code, domain, hint, message string
	level                       log.Level
	at                          time.Time
	data                        log.Data
	owner, user                 any
	request, response, trace    map[string]any
	tags                        []string
	with                        map[string]any
}

func (s *entry) Code() string             { return s.code }
func (s *entry) Context() context.Context { return context.Background() }
func (s *entry) Data() log.Data           { return s.data }
func (s *entry) Domain() string           { return s.domain }
func (s *entry) Hint() string             { return s.hint }
func (s *entry) Level() log.Level         { return s.level }
func (s *entry) Message() string          { return s.message }
func (s *entry) Owner() any               { return s.owner }
func (s *entry) Request() map[string]any  { return s.request }
func (s *entry) Response() map[string]any { return s.response }
func (s *entry) Tags() []string           { return s.tags }
func (s *entry) Time() time.Time          { return s.at }
func (s *entry) Trace() map[string]any    { return s.trace }
func (s *entry) User() any                { return s.user }
func (s *entry) With() map[string]any     { return s.with }

func cleanEntry(e log.Entry) log.Entry {
	s := &entry{
		code: Scrub(e.Code()), domain: Scrub(e.Domain()), hint: Scrub(e.Hint()), message: Scrub(e.Message()),
		level: e.Level(), at: e.Time(),
		owner: Value("owner", e.Owner()), user: Value("user", e.User()),
		request: Map(e.Request()), response: Map(e.Response()), trace: Map(e.Trace()), with: Map(e.With()),
	}
	if d := e.Data(); d != nil {
		s.data = log.Data(Map(d))
	}
	for _, t := range e.Tags() {
		s.tags = append(s.tags, Scrub(t))
	}
	return s
}
