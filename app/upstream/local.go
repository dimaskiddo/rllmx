package upstream

// localNames are provider names that have no data file yet: the media-only
// providers and the Xiaomi token plan's short name. They resolve to a provider
// id and count as reserved, so a node prefix cannot take them.
var localNames = map[string]string{
	"el":               "elevenlabs",
	"jina":             "jina-ai",
	"jina-ai":          "jina-ai",
	"polly":            "aws-polly",
	"aws-polly":        "aws-polly",
	"xmtp":             "xiaomi-tokenplan",
	"xiaomi-tokenplan": "xiaomi-tokenplan",
}
