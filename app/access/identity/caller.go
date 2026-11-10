// Package identity holds who a request is made by. Key derivation and the
// machine id belong to the same package and arrive with the API key step.
package identity

// KeyStatus says what a request presented as an API key.
type KeyStatus int

const (
	// KeyNone means the request presented no key.
	KeyNone KeyStatus = iota
	// KeyInvalid is a presented key that is unknown, inactive or malformed.
	KeyInvalid
	// KeyValid is a presented key that matches an active key.
	KeyValid
)

// Caller is who made a request.
type Caller struct {
	ID   string
	Name string
	// Restricted limits the key to Allow, which the pipeline checks.
	Restricted bool
	Allow      []string
	Anonymous  bool
}

// KeyResult is the verdict on a presented key.
type KeyResult struct {
	Status KeyStatus
	Caller Caller
}

const anonymousLocalID = "anonymous-local"

// AnonymousLocal is the caller of a local request that presented no valid key
// while the key requirement is off. Usage rows and cache keys use its fixed id.
func AnonymousLocal() Caller {
	return Caller{ID: anonymousLocalID, Name: "local", Anonymous: true}
}
