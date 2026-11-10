package kiro

import "testing"

func TestKiroAliasFoldsByFamilyAndVersion(t *testing.T) {
	for in, want := range map[string]string{
		// Anthropic spellings, with and without a snapshot date.
		"claude-opus-4-5":            "claude-opus-4.5",
		"claude-opus-4-5-20251101":   "claude-opus-4.5",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-sonnet-4-20250514":   "claude-sonnet-4",
		"claude-opus-5.5":            "claude-opus-5.5",
		"claude-opus-5":              "claude-opus-5",
		"claude-opus-4.8":            "claude-opus-4.8",
		"claude-sonnet-5":            "claude-sonnet-5",
		"claude-sonnet-4.6":          "claude-sonnet-4.6",
		"claude-haiku-4.5":           "claude-haiku-4.5",
		"claude-haiku-5.5":           "claude-haiku-5.5",
		"claude-fable-5":             "claude-fable-5",
		// The aliases Kiro already uses come back unchanged.
		"claude-sonnet-4.5": "claude-sonnet-4.5",
		// The rest of the catalog.
		"deepseek-3.2":     "deepseek-3.2",
		"glm-5":            "glm-5",
		"MiniMax-M2.5":     "minimax-m2.5",
		"MiniMax-M2.1":     "minimax-m2.1",
		"qwen3-coder-next": "qwen3-coder-next",
		"kimi-k3":          "kimi-k3",
		"gpt-5.6-sol":      "gpt-5.6-sol",
		"gpt-5.6-terra":    "gpt-5.6-terra",
		"gpt-5.6-luna":     "gpt-5.6-luna",
		"auto":             "auto",
		// An id from no family passes through, so an operator can reach a model
		// Kiro adds before this file knows it.
		"some-future-model": "some-future-model",
		"":                  "",
	} {
		if got := kiroAlias(in); got != want {
			t.Errorf("kiroAlias(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKiroAliasLeavesANonDateSuffixAlone(t *testing.T) {
	for in := range map[string]bool{
		"claude-opus-4-5-2025110":  true, // eight digits but not a date width
		"claude-opus-4-5-20251x01": true,
	} {
		if got := kiroAlias(in); got == "" {
			t.Errorf("%q produced nothing", in)
		}
	}
}
