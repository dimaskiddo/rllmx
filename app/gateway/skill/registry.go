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
	"sync"
)

type entry struct {
	m      Manifest
	prompt string
	rules  []Rule
}

// Registry is the installed skills: the embedded core set with the operator's
// DATA_DIR/skills folder laid over it by id. A folder that fails validation is
// skipped and reported through Problems, never half-loaded.
type Registry struct {
	dir      string
	builtins fs.FS

	mu       sync.Mutex
	sig      string
	entries  []entry
	byID     map[string]entry
	localIdx Index
	eccIdx   Index
	problems []string
}

// NewRegistry returns a Registry reading dir, with builtins as the base set.
// Either half may be absent: a nil builtins or a missing dir simply contributes
// no skills.
func NewRegistry(dir string, builtins fs.FS) *Registry {
	return &Registry{dir: dir, builtins: builtins}
}

// List returns the valid skills, sorted by id.
func (r *Registry) List() []Manifest {
	if r == nil {
		return nil
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Manifest, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.m
	}
	return out
}

// Find returns the installed manifest with this id.
func (r *Registry) Find(id string) (Manifest, bool) {
	if r == nil {
		return Manifest{}, false
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	return e.m, ok
}

// Prompt returns a skill's prompt text, or "" for an id the registry does not
// hold.
func (r *Registry) Prompt(id string) string {
	if r == nil {
		return ""
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byID[id].prompt
}

// Signature names the installed set; the response-cache key carries it so an
// answer built from one skill is never served for another.
func (r *Registry) Signature() string {
	if r == nil {
		return ""
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sig
}

// Problems lists the folders that were skipped and why, for the dashboard.
func (r *Registry) Problems() []string {
	if r == nil {
		return nil
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.problems...)
}

// refresh reloads when the folder changed, so an import or a sync is visible on
// the next request without a restart.
func (r *Registry) refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	sig := r.signatureLocked()
	if r.byID != nil && sig == r.sig {
		return
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
	r.sig, r.byID, r.entries, r.problems = sig, byID, entries, problems
	r.buildIndexes()
}

// sourceECC marks a skill written by the catalog sync, so the two routers keep
// their own corpora.
const sourceECC = "ecc"

// buildIndexes weights the two corpora once per reload, so no request pays for
// tokenizing every installed skill.
func (r *Registry) buildIndexes() {
	local := make([]Doc, 0, len(r.entries))
	ecc := make([]Doc, 0, len(r.entries))
	for _, e := range r.entries {
		d := Doc{ID: e.m.ID, Name: e.m.Name, Description: e.m.Description, Triggers: e.m.Triggers, Keywords: e.m.Keywords}
		if e.m.Source == sourceECC {
			ecc = append(ecc, d)
			continue
		}
		if e.m.Routable {
			local = append(local, d)
		}
	}
	r.localIdx = BuildIndex(local, localWeights)
	r.eccIdx = BuildIndex(ecc, eccWeights)
}

// LocalIndex scores a request against the routable installed skills.
func (r *Registry) LocalIndex() Index {
	if r == nil {
		return Index{}
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.localIdx
}

// ECCIndex scores a request against the imported catalog.
func (r *Registry) ECCIndex() Index {
	if r == nil {
		return Index{}
	}
	r.refresh()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.eccIdx
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
	fsys := os.DirFS(r.dir)
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		if ent.Name() == CatalogDir {
			r.loadCatalog(fsys, byID, problems)
			continue
		}
		m, prompt, rules, err := loadFS(fsys, ent.Name(), ent.Name())
		if err != nil {
			*problems = append(*problems, err.Error())
			continue
		}
		byID[m.ID] = entry{m: m, prompt: prompt, rules: rules}
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
		byID[m.ID] = entry{m: m, prompt: prompt, rules: rules}
	}
}

// signatureLocked names the folder's state by name, size and mtime. A sync that
// rewrites a prompt changes it, so the next request reloads.
func (r *Registry) signatureLocked() string {
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
