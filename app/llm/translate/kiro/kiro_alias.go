package kiro

import "strings"

// kiroAlias folds a model id onto the alias CodeWhisperer accepts. The two
// disagree on spelling: Anthropic ships `claude-sonnet-4-5-20250929`, and the
// upstream answers anything but `claude-sonnet-4.5` with INVALID_MODEL_ID.
//
// The fold is by family and version word rather than a table of ids, so a
// snapshot suffix or a future minor revision still lands on its family's alias.
// Every version check names the family, so "sonnet-5" and "4-5" cannot be
// confused. An id from no family here passes through, which lets an operator
// reach a model Kiro adds before this file knows it.
func kiroAlias(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if id == "" {
		return model
	}
	id = stripSnapshot(id)

	switch {
	case strings.Contains(id, "sonnet"):
		switch {
		case version(id, "5.5", "5-5"):
			return "claude-sonnet-5.5"
		case version(id, "4.6", "4-6"):
			return "claude-sonnet-4.6"
		case strings.Contains(id, "sonnet-5"), strings.Contains(id, "sonnet.5"):
			return "claude-sonnet-5"
		case version(id, "4.5", "4-5"):
			return "claude-sonnet-4.5"
		case strings.Contains(id, "sonnet-4"), strings.Contains(id, "sonnet.4"):
			return "claude-sonnet-4"
		}
		return "claude-sonnet-4.5"
	case strings.Contains(id, "fable"):
		return "claude-fable-5"
	case strings.Contains(id, "opus"):
		switch {
		case version(id, "5.5", "5-5"):
			return "claude-opus-5.5"
		case strings.Contains(id, "opus-5"), strings.Contains(id, "opus.5"), strings.Contains(id, "opus 5"):
			return "claude-opus-5"
		case version(id, "4.8", "4-8"):
			return "claude-opus-4.8"
		case version(id, "4.7", "4-7"):
			return "claude-opus-4.7"
		case version(id, "4.5", "4-5"):
			return "claude-opus-4.5"
		}
		return "claude-opus-4.6"
	case strings.Contains(id, "haiku"):
		if version(id, "5.5", "5-5") {
			return "claude-haiku-5.5"
		}
		return "claude-haiku-4.5"
	case id == "auto":
		return "auto"
	case strings.Contains(id, "deepseek"):
		return "deepseek-3.2"
	case strings.Contains(id, "glm"):
		return "glm-5"
	case strings.Contains(id, "minimax"):
		if version(id, "2.5", "2-5") {
			return "minimax-m2.5"
		}
		return "minimax-m2.1"
	case strings.Contains(id, "qwen"):
		return "qwen3-coder-next"
	case strings.Contains(id, "kimi"):
		return "kimi-k3"
	case strings.Contains(id, "gpt"):
		switch {
		case strings.Contains(id, "terra"):
			return "gpt-5.6-terra"
		case strings.Contains(id, "luna"):
			return "gpt-5.6-luna"
		case strings.Contains(id, "sol"), version(id, "5.6", "5-6"):
			return "gpt-5.6-sol"
		}
		return model
	}
	return model
}

// version reports the id naming one version, written either way.
func version(id string, forms ...string) bool {
	for _, f := range forms {
		if strings.Contains(id, f) {
			return true
		}
	}
	return false
}

// stripSnapshot removes a trailing `-YYYYMMDD`, which names a release date and
// not a version.
func stripSnapshot(id string) string {
	if len(id) < 9 || id[len(id)-9] != '-' {
		return id
	}
	for i := len(id) - 8; i < len(id); i++ {
		if id[i] < '0' || id[i] > '9' {
			return id
		}
	}
	return id[:len(id)-9]
}
