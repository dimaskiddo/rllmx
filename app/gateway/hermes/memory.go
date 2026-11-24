// Package hermes reads the memory files a local Hermes Agent keeps and renders
// them as one block the router injects. It never runs the agent.
package hermes

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rllmx/app/platform/file"
	"rllmx/app/platform/text"
)

// Bounds: one memory file is read whole, a block is cut at the configured limit.
const (
	maxFile   = 64 << 10
	maxEntry  = 500
	entrySep  = "\n§\n"
	userFile  = "USER.md"
	memFile   = "MEMORY.md"
	memSubdir = "memories"
)

// Reader reads the two memory files, caching the rendered block until either
// file changes.
type Reader struct {
	// Home is the agent's directory; empty reads nothing.
	Home   string
	mu     sync.Mutex
	memAt  time.Time
	userAt time.Time
	limit  int
	block  string
	loaded bool
}

// Bypass is the header that turns the memory injection off for one request. It
// keeps the reference's name so an existing client keeps working.
const Bypass = "x-9router-hermes-memory"

// DefaultLimit is the block bound the skill's own field defaults to.
const DefaultLimit = 2200

// Block is the text the router injects, or empty when the agent has no notes.
// limit bounds the block in bytes; zero uses DefaultLimit.
func (r *Reader) Block(limit int) string {
	if r == nil || strings.TrimSpace(r.Home) == "" {
		return ""
	}
	if limit <= 0 {
		limit = DefaultLimit
	}
	memPath := filepath.Join(r.Home, memSubdir, memFile)
	userPath := filepath.Join(r.Home, memSubdir, userFile)
	memAt, userAt := modTime(memPath), modTime(userPath)

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded && r.limit == limit && memAt.Equal(r.memAt) && userAt.Equal(r.userAt) {
		return r.block
	}
	r.loaded, r.limit, r.memAt, r.userAt = true, limit, memAt, userAt
	r.block = render(entries(memPath), entries(userPath), limit)
	return r.block
}

// render joins the two folders' entries into one block, cut at limit.
func render(memory, user []string, limit int) string {
	var b strings.Builder
	if len(memory) > 0 {
		b.WriteString("MEMORY (agent notes):\n- ")
		b.WriteString(strings.Join(memory, "\n- "))
	}
	if len(user) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("USER PROFILE (agent context):\n- ")
		b.WriteString(strings.Join(user, "\n- "))
	}
	if b.Len() == 0 {
		return ""
	}
	out := "--- Agent Persistent Memory ---\n" + b.String() + "\n--- End Agent Memory ---"
	if limit > 0 && len(out) > limit {
		out = text.CutBytes(out, limit)
	}
	return out
}

// entries reads one file, splits it on the agent's separator and caps each
// entry. A missing or unreadable file has no entries.
func entries(path string) []string {
	data, err := readCapped(path, maxFile)
	if err != nil {
		return nil
	}
	var out []string
	for _, raw := range strings.Split(string(data), entrySep) {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		out = append(out, cut(entry, maxEntry))
	}
	return out
}

func readCapped(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return file.ReadUpTo(f, int64(limit)), nil
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// cut keeps at most n bytes on a character boundary, with an ellipsis.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return text.CutBytes(s, n-3) + "..."
}
