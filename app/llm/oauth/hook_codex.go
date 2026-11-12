package oauth

// CodexAccount is the Codex login step: what the tokens themselves say about
// the account.
func CodexAccount(t Tokens) Account { return claimsAccount(t) }
