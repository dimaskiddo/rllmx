// Package llm gives executor and oauth tests the request shapes they build by
// hand. It is a sub-package because it imports app/llm/upstream.
package llm

import "rllmx/app/llm/upstream"

// Bearer is the usual bearer authorization block.
func Bearer() *upstream.Auth {
	return &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}
}

// Secret is a key value that a test may compare against what was sent.
const Secret = "sk-test-secret-value"
