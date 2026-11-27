package network

// Compression is how an outbound request body may be encoded. A provider that
// reads a compressed body accepts only some of these, so the pipeline keeps the
// choice in configuration.
type Compression string

const (
	// CompressionOff sends every body as it is.
	CompressionOff Compression = "off"
	// CompressionGzip is the default: every HTTP server reads it.
	CompressionGzip Compression = "gzip"
	// CompressionBrotli encodes smaller, for a provider known to accept it.
	CompressionBrotli Compression = "brotli"
)

// Encoding is the Content-Encoding name, or empty when nothing is encoded.
func (c Compression) Encoding() string {
	switch c {
	case CompressionGzip:
		return "gzip"
	case CompressionBrotli:
		return "br"
	}
	return ""
}
