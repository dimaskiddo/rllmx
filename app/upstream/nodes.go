package upstream

import "strings"

// Provider-node ids start with the node's kind, so an id alone says whether it
// names a user node and which API it speaks.
const (
	NodeOpenAIPrefix    = "openai-compatible-"
	NodeAnthropicPrefix = "anthropic-compatible-"
	NodeEmbeddingPrefix = "custom-embedding-"
)

// Node types as stored in the providerNodes table.
const (
	NodeTypeOpenAI    = "openai-compatible"
	NodeTypeAnthropic = "anthropic-compatible"
	NodeTypeEmbedding = "custom-embedding"
)

// NodeKind returns the node type an id names, or "" when the id is not a node's.
func NodeKind(id string) string {
	switch {
	case strings.HasPrefix(id, NodeOpenAIPrefix):
		return NodeTypeOpenAI
	case strings.HasPrefix(id, NodeAnthropicPrefix):
		return NodeTypeAnthropic
	case strings.HasPrefix(id, NodeEmbeddingPrefix):
		return NodeTypeEmbedding
	}
	return ""
}
