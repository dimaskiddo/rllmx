package media

import "github.com/google/uuid"

// UUID is a random identifier, for the headers and request ids a provider's own
// client sends.
func UUID() string { return uuid.NewString() }
