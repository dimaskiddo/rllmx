package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type entry struct {
	m      Manifest
	prompt string
	rules  []Rule
}

// recheckEvery is how long a view of the folder is trusted before the folder is
// looked at again. A write made through this router calls Invalidate and is seen
// at once; a file placed by hand shows within this time.
const recheckEvery = 2 * time.Second

// view is one load of the registry. It is never changed after it is built, so a
// reader holds no lock while it uses one.
type view struct {
	sig      string
	entries  []entry
	byID     map[string]entry
	localIdx index
	eccIdx   index
	problems []string
}

// Registry is the installed skills: the embedded core set with the operator's
// DATA_DIR/skills folder laid over it by id. A folder that fails validation is
// skipped and reported through Problems, never half-loaded.
type Registry struct {
	dir      string
	builtins fs.FS

	cur     atomic.Pointer[view]
	checked atomic.Int64
	// load serialises rebuilds only: no reader waits on it once a view exists.
	load sync.Mutex
}

// NewRegistry returns a Registry reading dir, with builtins as the base set.
// Either half may be absent: a nil builtins or a missing dir simply contributes
// no skills.
func NewRegistry(dir string, builtins fs.FS) *Registry {
	return &Registry{dir: dir, builtins: builtins}
}

// Invalidate makes the next read look at the folder again, for a writer that
// just changed it.
func (r *Registry) Invalidate() {
	if r != nil {
		r.checked.Store(0)
	}
}

// List returns the valid skills, sorted by id.
func (r *Registry) List() []Manifest {
	if r == nil {
		return nil
	}
	v := r.refresh()
	out := make([]Manifest, len(v.entries))
	for i, e := range v.entries {
		out[i] = e.m
	}
	return out
}

// Find returns the installed manifest with this id.
func (r *Registry) Find(id string) (Manifest, bool) {
	if r == nil {
		return Manifest{}, false
	}
	e, ok := r.refresh().byID[id]
	return e.m, ok
}

// Prompt returns a skill's prompt text, or "" for an id the registry does not
// hold.
func (r *Registry) Prompt(id string) string {
	if r == nil {
		return ""
	}
	return r.refresh().byID[id].prompt
}

// Signature names the installed set; the response-cache key carries it so an
// answer built from one skill is never served for another.
func (r *Registry) Signature() string {
	if r == nil {
		return ""
	}
	return r.refresh().sig
}

// Problems lists the folders that were skipped and why, for the dashboard.
func (r *Registry) Problems() []string {
	if r == nil {
		return nil
	}
	return append([]string(nil), r.refresh().problems...)
}

// refresh returns the current view, rebuilding it when the folder changed. The
// folder is looked at no more than once per recheckEvery, and a rebuild reads
// the disk with no reader waiting on it.
func (r *Registry) refresh() *view {
	now := time.Now().UnixNano()
	if v := r.cur.Load(); v != nil && now-r.checked.Load() < int64(recheckEvery) {
		return v
	}
	r.load.Lock()
	defer r.load.Unlock()
	now = time.Now().UnixNano()
	if v := r.cur.Load(); v != nil && now-r.checked.Load() < int64(recheckEvery) {
		return v
	}
	sig := r.signature()
	if v := r.cur.Load(); v != nil && v.sig == sig {
		r.checked.Store(now)
		return v
	}
	byID := map[string]entry{}
	var problems []string
	r.loadBuiltins(byID, &problems)
	r.loadDisk(byID, &problems)
	entries := make([]entry, 0, len(byID))
	for _, e := range byID {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].m.ID < entries[j].m.ID })
	v := &view{sig: sig, entries: entries, byID: byID, problems: problems}
	v.localIdx, v.eccIdx = buildIndexes(entries)
	r.cur.Store(v)
	r.checked.Store(now)
	return v
}

// sourceECC marks a skill written by the catalog sync, so the two routers keep
// their own corpora.
const sourceECC = "ecc"

// buildIndexes weights the two corpora once per reload, so no request pays for
// tokenizing every installed skill.
func buildIndexes(entries []entry) (local, ecc index) {
	localDocs := make([]doc, 0, len(entries))
	eccDocs := make([]doc, 0, len(entries))
	for _, e := range entries {
		d := doc{ID: e.m.ID, Name: e.m.Name, Description: e.m.Description, Triggers: e.m.Triggers, Keywords: e.m.Keywords}
		if e.m.Source == sourceECC {
			eccDocs = append(eccDocs, d)
			continue
		}
		if e.m.Routable {
			localDocs = append(localDocs, d)
		}
	}
	return buildIndex(localDocs, localWeights), buildIndex(eccDocs, eccWeights)
}

// LocalIndex scores a request against the routable installed skills.
func (r *Registry) localIndex() index {
	if r == nil {
		return index{}
	}
	return r.refresh().localIdx
}

// ECCIndex scores a request against the imported catalog.
func (r *Registry) eccIndex() index {
	if r == nil {
		return index{}
	}
	return r.refresh().eccIdx
}

func (r *Registry) loadBuiltins(byID map[string]entry, problems *[]string) {
	if r.builtins == nil {
		return
	}
	manifests, err := fs.Glob(r.builtins, "builtin/*/manifest.json")
	if err != nil {
		*problems = append(*problems, "builtin skills: "+err.Error())
		return
	}
	for _, manifest := range manifests {
		dir := path.Dir(manifest)
		m, prompt, rules, err := loadFS(r.builtins, dir, path.Base(dir))
		if err != nil {
			*problems = append(*problems, err.Error())
			continue
		}
		byID[m.ID] = entry{m: m, prompt: prompt, rules: rules}
	}
}

// CatalogDir is the folder the catalog sync writes into, under the skills
// folder. Its children are skills; the folder itself is not.
const CatalogDir = "ecc-imported"

// FetchDir is where a repository is cloned or unpacked before its skills are
// read: beside the skills folder, not in it, so a half-finished clone is never
// scanned as a skill.
func FetchDir(dataDir string) string { return filepath.Join(dataDir, ".skill-fetch") }

// loadDisk reads the operator's folder last, so a skill of the same id there
// replaces the embedded one. The catalog folder holds one level of skills.
func (r *Registry) loadDisk(byID map[string]entry, problems *[]string) {
	if r.dir == "" {
		return
	}
	ents, err := os.ReadDir(r.dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			*problems = append(*problems, "skills: "+err.Error())
		}
		return
	}
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		*problems = append(*problems, "skills: "+err.Error())
		return
	}
	defer root.Close()
	fsys := root.FS()
	catalog := false
	for _, ent := range ents {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		if ent.Name() == CatalogDir {
			catalog = true
			continue
		}
		m, prompt, rules, err := loadFS(fsys, ent.Name(), ent.Name())
		if err != nil {
			*problems = append(*problems, err.Error())
			continue
		}
		byID[m.ID] = entry{m: m, prompt: prompt, rules: rules}
	}
	// Last, and only into ids nothing holds, so an import can never replace a
	// built-in or a skill the operator wrote, whatever order the folders list in.
	if catalog {
		r.loadCatalog(fsys, byID, problems)
	}
}

// loadCatalog reads the imported catalog's children as skills.
func (r *Registry) loadCatalog(fsys fs.FS, byID map[string]entry, problems *[]string) {
	ents, err := os.ReadDir(filepath.Join(r.dir, CatalogDir))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			*problems = append(*problems, "skills: "+err.Error())
		}
		return
	}
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		dir := path.Join(CatalogDir, ent.Name())
		m, prompt, rules, err := loadFS(fsys, dir, ent.Name())
		if err != nil {
			*problems = append(*problems, err.Error())
			continue
		}
		if _, held := byID[m.ID]; held {
			continue
		}
		byID[m.ID] = entry{m: m, prompt: prompt, rules: rules}
	}
}

// signature names the folder's state by name, size and mtime. A sync that
// rewrites a prompt changes it, so the next look reloads.
func (r *Registry) signature() string {
	h := sha256.New()
	if r.dir != "" {
		if ents, err := os.ReadDir(r.dir); err == nil {
			for _, ent := range ents {
				if !ent.IsDir() {
					continue
				}
				if ent.Name() == CatalogDir {
					if kids, err := os.ReadDir(filepath.Join(r.dir, CatalogDir)); err == nil {
						for _, kid := range kids {
							if kid.IsDir() {
								signatureDir(h, r.dir, path.Join(CatalogDir, kid.Name()))
							}
						}
					}
					continue
				}
				signatureDir(h, r.dir, ent.Name())
			}
		}
	}
	if r.builtins != nil {
		if names, err := fs.Glob(r.builtins, "builtin/*/manifest.json"); err == nil {
			for _, n := range names {
				fmt.Fprintf(h, "e:%s;", n)
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// signatureDir adds one skill folder's files to the signature.
func signatureDir(h hash.Hash, root, dir string) {
	fmt.Fprintf(h, "d:%s;", dir)
	for _, name := range []string{"manifest.json", "prompt.txt", "prompt.md", "rules.json", "hook.js"} {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), name)); err == nil {
			fmt.Fprintf(h, "%s=%d@%d;", name, st.Size(), st.ModTime().UnixNano())
		}
	}
}
