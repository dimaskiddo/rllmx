package tests

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/log"
)

// Entry is a log.Entry whose fields a test sets directly.
type Entry struct {
	CodeV     string
	Ctx       context.Context
	DataV     log.Data
	DomainV   string
	HintV     string
	LevelV    log.Level
	MessageV  string
	OwnerV    any
	RequestV  map[string]any
	ResponseV map[string]any
	TagsV     []string
	TimeV     time.Time
	TraceV    map[string]any
	UserV     any
	WithV     map[string]any
}

func (e Entry) Code() string { return e.CodeV }

func (e Entry) Context() context.Context {
	if e.Ctx == nil {
		return context.Background()
	}
	return e.Ctx
}

func (e Entry) Data() log.Data           { return e.DataV }
func (e Entry) Domain() string           { return e.DomainV }
func (e Entry) Hint() string             { return e.HintV }
func (e Entry) Level() log.Level         { return e.LevelV }
func (e Entry) Message() string          { return e.MessageV }
func (e Entry) Owner() any               { return e.OwnerV }
func (e Entry) Request() map[string]any  { return e.RequestV }
func (e Entry) Response() map[string]any { return e.ResponseV }
func (e Entry) Tags() []string           { return e.TagsV }
func (e Entry) Time() time.Time          { return e.TimeV }
func (e Entry) Trace() map[string]any    { return e.TraceV }
func (e Entry) User() any                { return e.UserV }
func (e Entry) With() map[string]any     { return e.WithV }

// Handler is a log.Handler that keeps the entries it is given.
type Handler struct {
	Min     log.Level
	Entries []log.Entry
	Err     error
}

func (h *Handler) Enabled(l log.Level) bool { return l.Level() >= h.Min.Level() }

func (h *Handler) Handle(e log.Entry) error {
	h.Entries = append(h.Entries, e)
	return h.Err
}
