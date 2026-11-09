package oauth

import (
	"context"
	"time"

	"rllmx/app/dialect"
	"rllmx/app/upstream"
)

// Fixed words a device login can end with besides the token classes.
const (
	CodeExpired = "expired_token"
	CodeRefused = "rejected"
)

// DeviceStarted is what the browser needs to show the user where to go. The
// device code stays here.
type DeviceStarted struct {
	State           string `json:"state"`
	UserCode        string `json:"userCode,omitempty"`
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

// DevicePolled is the answer to one poll from the browser.
type DevicePolled struct {
	Status       string `json:"status"`
	ConnectionID string `json:"connectionId,omitempty"`
	Error        string `json:"error,omitempty"`
	// Interval is the pause, in seconds, the browser should keep between polls.
	Interval int `json:"interval,omitempty"`
}

// StartDevice opens a device login for a provider that has one.
func (s *Service) StartDevice(ctx context.Context, provider, bind string) (DeviceStarted, error) {
	f, err := s.flow(provider)
	if err != nil {
		return DeviceStarted{}, err
	}
	if f.Flow != upstream.FlowDevice {
		return DeviceStarted{}, ErrUnsupported
	}
	var deviceID string
	if f.UsesDeviceID() {
		deviceID = dialect.NewID()
	}
	grant, err := s.Tokens.Device(ctx, f, deviceID)
	if err != nil {
		return DeviceStarted{}, err
	}
	state, err := s.Sessions.Put(Session{
		Provider: f.Provider, Bind: bind, DeviceCode: grant.DeviceCode, Interval: grant.Interval, Life: grant.ExpiresIn,
		DeviceID: deviceID,
	})
	if err != nil {
		return DeviceStarted{}, err
	}
	return DeviceStarted{
		State: state, UserCode: grant.UserCode, VerificationURI: grant.VerificationURI,
		ExpiresIn: int(grant.ExpiresIn / time.Second), Interval: int(grant.Interval / time.Second),
	}, nil
}

// PollDevice asks the provider whether the user approved the code, at most once
// per interval however often the browser asks, and only for the browser that
// started the login. A login that is waiting answers "pending" with no call.
func (s *Service) PollDevice(ctx context.Context, state, bind string) DevicePolled {
	sess, gate := s.Sessions.Poll(state, func(x Session) bool { return SameBind(x.Bind, bind) })
	switch gate {
	case GateUnknown:
		// A finished login is read once, like the redirect flows.
		return fromStatus(s.Sessions.Status(state, bind))
	case GateWait:
		return DevicePolled{Status: "pending"}
	}
	f, err := s.flow(sess.Provider)
	if err != nil {
		return s.endDevice(state, CodeInput)
	}
	tokens, pending, err := s.Tokens.DevicePoll(ctx, f, sess.DeviceCode, sess.DeviceID)
	switch {
	case err != nil:
		return s.endDevice(state, deviceCode(err))
	case pending == pollSlow:
		s.Sessions.Rest(state, min(sess.Interval+slowStep, maxSlowInterval))
		return DevicePolled{Status: "pending", Interval: int(min(sess.Interval+slowStep, maxSlowInterval) / time.Second)}
	case pending == pollPending:
		s.Sessions.Rest(state, sess.Interval)
		return DevicePolled{Status: "pending", Interval: int(sess.Interval / time.Second)}
	}
	id, err := s.store(ctx, f, tokens, sess.DeviceID)
	if err != nil {
		if code := ClassOf(err); code != "" {
			return s.endDevice(state, deviceCode(err))
		}
		if s.Log != nil {
			s.Log.Warningf("oauth: storing a login failed")
		}
		return s.endDevice(state, CodeStorage)
	}
	s.Sessions.End(state, id, "")
	return DevicePolled{Status: "done", ConnectionID: id}
}

// maxSlowInterval caps how far "slow down" can stretch the pause.
const maxSlowInterval = 30 * time.Second

func (s *Service) endDevice(state, code string) DevicePolled {
	s.Sessions.End(state, "", code)
	return DevicePolled{Status: "error", Error: code}
}

func fromStatus(st Status) DevicePolled {
	return DevicePolled{Status: st.State, ConnectionID: st.ConnectionID, Error: st.Error}
}

// deviceCode reduces a failed poll to a fixed word.
func deviceCode(err error) string {
	switch ClassOf(err) {
	case ClassExpired:
		return CodeExpired
	case ClassDenied:
		return CodeDenied
	case ClassTransport:
		return string(ClassTransport)
	}
	return CodeRefused
}
