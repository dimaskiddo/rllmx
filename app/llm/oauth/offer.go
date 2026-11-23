package oauth

import (
	"errors"

	"rllmx/app/llm/upstream"
)

// Offer is a provider's login as a dashboard needs it: the flow, the other
// ways in, and whether this deployment has configured it. It holds no URL,
// scope or client id.
type Offer struct {
	Flow           string   `json:"flow"`
	PAT            bool     `json:"pat,omitempty"`
	Bulk           bool     `json:"bulk,omitempty"`
	AppFromAccount bool     `json:"appFromAccount,omitempty"`
	Fields         []string `json:"fields,omitempty"`
	Variants       []string `json:"variants,omitempty"`
	Enabled        bool     `json:"enabled"`
}

// Offer describes an entry's login, or reports that it has none. Enabled
// follows NewFlow exactly, so a login the engine would refuse is offered
// switched off rather than hidden.
func (s *Service) Offer(e upstream.Entry) (Offer, bool) {
	if e.OAuth == nil {
		return Offer{}, false
	}
	clientID := ""
	if s.ClientID != nil {
		clientID = s.ClientID(e.ID)
	}
	_, err := NewFlow(e, clientID)
	if errors.Is(err, ErrUnsupported) {
		return Offer{}, false
	}
	o := Offer{
		Flow: e.OAuth.Flow, PAT: e.OAuth.PAT, Bulk: e.OAuth.Bulk,
		AppFromAccount: e.OAuth.AppFromAccount, Fields: e.OAuth.Fields,
		Enabled: !errors.Is(err, ErrDisabled),
	}
	if d, ok := s.dials().Code[e.OAuth.Dialect]; ok {
		o.Variants = d.Variants
	}
	return o, true
}
