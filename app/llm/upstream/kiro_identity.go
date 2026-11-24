package upstream

import (
	"crypto/sha256"
	"encoding/hex"
)

// Kiro rejects an identity older than its current IDE with "This version of
// Kiro is no longer supported", so every Kiro call, inference, token renewal,
// model list and usage read, presents the same one built here.
const (
	// KiroIDEVersion is the IDE release the identity claims.
	KiroIDEVersion = "1.4.154"
	// KiroACPAgent is the user agent of the IDE's Responses transport, which
	// speaks for the editor rather than for an AWS SDK client.
	KiroACPAgent   = "KiroIDE-" + KiroIDEVersion + "-KiroIDE/acp-client"
	kiroSDKVersion = "1.0.0"
	kiroAgentOS    = "windows#10.0.26200"
	kiroNode       = "nodejs#22.21.1"
)

// KiroClient is the AWS SDK client a call belongs to, which names itself in the
// user agent: the streaming client for inference, the runtime client for the
// model list, the profile list and the usage limits.
type KiroClient struct{ api, metrics string }

var (
	// KiroStreaming is the client generateAssistantResponse goes through.
	KiroStreaming = KiroClient{api: "codewhispererstreaming", metrics: "E"}
	// KiroRuntime is the client every other call goes through.
	KiroRuntime = KiroClient{api: "codewhispererruntime", metrics: "N,E"}
)

// KiroSeed is what an account is told apart by, in the order of how stable each
// is: a token that rotates must not change the identity.
type KiroSeed struct {
	ClientID, RefreshToken, ProfileARN, APIKey, AccessToken string
}

func (s KiroSeed) value() string {
	for _, v := range []string{s.ClientID, s.RefreshToken, s.ProfileARN, s.APIKey, s.AccessToken} {
		if v != "" {
			return v
		}
	}
	return "kiro-anonymous"
}

// KiroIdentity is the User-Agent pair the Kiro IDE sends. The machine id is a
// hash of the account's most stable identifier, so no credential reaches a
// header and one account keeps one identity.
func KiroIdentity(seed KiroSeed, client KiroClient) map[string]string {
	sum := sha256.Sum256([]byte(seed.value()))
	id := "KiroIDE-" + KiroIDEVersion + "-" + hex.EncodeToString(sum[:])
	return map[string]string{
		"User-Agent": "aws-sdk-js/" + kiroSDKVersion + " ua/2.1 os/" + kiroAgentOS + " lang/js md/" + kiroNode +
			" api/" + client.api + "#" + kiroSDKVersion + " m/" + client.metrics + " " + id,
		"x-amz-user-agent": "aws-sdk-js/" + kiroSDKVersion + " " + id,
	}
}
