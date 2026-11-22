package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/gateway/skill"
	"rllmx/app/git"
	"rllmx/app/platform/file"
	"rllmx/app/platform/network"
	"rllmx/app/store/data"
)

// Bounds on one import, so a large source cannot make the command walk or copy
// without limit.
const (
	maxSkillFolders = 500
	maxSkillDepth   = 4
)

// SkillsSync imports the catalog named by --source into the embedded
// registry's operator folder.
type SkillsSync struct{ R *DatabaseRuntime }

func (c *SkillsSync) Signature() string { return "skills:sync" }

func (c *SkillsSync) Description() string {
	return "Import Agent Skills folders into DATA_DIR/skills/" + skill.CatalogDir
}

func (c *SkillsSync) Extend() command.Extend {
	return command.Extend{Category: "skills", Flags: []command.Flag{
		&command.StringFlag{Name: "source", Usage: "local folder or https repository (required)"},
		&command.StringFlag{Name: "tag", Usage: "branch or tag to read, for a repository (default main)"},
		&command.BoolFlag{Name: "force", Usage: "overwrite a catalog skill the operator edited"},
	}}
}

func (c *SkillsSync) Handle(ctx console.Context) error {
	src := strings.TrimSpace(ctx.Option("source"))
	if src == "" {
		return errors.New("--source is required: a local folder or an https repository")
	}
	h, err := c.R.Open(ctx, data.ModeRead)
	if err != nil {
		return err
	}
	var sources []string
	if isRemote(src) {
		if sources, err = configuredSources(ctx, h); err != nil {
			return err
		}
	}
	ref := strings.TrimSpace(ctx.Option("tag"))
	if ref == "" {
		ref = "main"
	}
	dir, cleanup, err := catalogSource(ctx, h.DataDir, src, ref, sources)
	if err != nil {
		return err
	}
	defer cleanup()
	res, err := skill.SyncECC(ctx, dir, filepath.Join(h.DataDir, "skills", skill.CatalogDir), ctx.OptionBool("force"))
	if err != nil {
		return err
	}
	ctx.Line(fmt.Sprintf("skills: %d of %d copied from %s, %d skipped", res.Updated, res.Total, src, res.Skipped))
	for _, id := range res.Skills {
		ctx.Line("  " + id)
	}
	return nil
}

// isRemote reports whether a source names a repository rather than a folder.
func isRemote(src string) bool { return strings.HasPrefix(src, "https://") }

// catalogSource resolves a source: a local folder is used as it is, a
// repository is cloned into a staging folder, or downloaded when git is missing
// or refuses. The caller removes the folder it gets.
func catalogSource(ctx context.Context, dataDir, src, ref string, sources []string) (string, func(), error) {
	if !isRemote(src) {
		if strings.Contains(src, "://") {
			return "", nil, fmt.Errorf("%w: the source must be a local folder or an https repository", git.ErrRefused)
		}
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			return "", nil, fmt.Errorf("skills: %s is not a folder", src)
		}
		return src, func() {}, nil
	}
	cfg := git.Config{Parent: skill.FetchDir(dataDir), Sources: sources}
	client := network.NewClient(network.Options{ConnectTimeout: git.Timeout, BodyCap: git.MaxSize})
	return git.NewGate().Fetch(ctx, cfg, client, src, ref)
}

// configuredSources reads the operator's https allowlist. A missing or
// unreadable row is an empty list, which refuses every clone.
func configuredSources(ctx context.Context, h *Handle) ([]string, error) {
	doc, err := h.Store.Settings().Effective(ctx)
	if err != nil {
		return nil, err
	}
	return skill.Sources(doc.GetRaw("skillSources")), nil
}

// importSkills copies the valid skill folders under src into DATA_DIR/skills
// and reports the ones it skipped. dryRun only lists.
func importSkills(ctx console.Context, dataDir, src, ref string, sources []string, dryRun bool) error {
	dir, cleanup, err := catalogSource(ctx, dataDir, src, ref, sources)
	if err != nil {
		return err
	}
	defer cleanup()
	folders := findSkillDirs(dir)
	dest := filepath.Join(dataDir, "skills")
	copied := 0
	for _, folder := range folders {
		m, prompt, err := skill.LoadDir(folder)
		if err != nil {
			ctx.Warning("skipped " + folder + ": " + err.Error())
			continue
		}
		if dryRun {
			ctx.Line("would copy " + m.ID + " from " + folder)
			copied++
			continue
		}
		if err := copySkill(folder, dest, m, prompt); err != nil {
			return err
		}
		copied++
		if len(m.Unsupported) > 0 {
			ctx.Warning(m.ID + ": ignored " + strings.Join(m.Unsupported, ", "))
		}
	}
	ctx.Line(fmt.Sprintf("skills: %d of %d copied", copied, len(folders)))
	return nil
}

// findSkillDirs lists the folders holding a manifest, bounded in count and
// depth and skipping hidden working folders.
func findSkillDirs(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			if strings.Count(filepath.ToSlash(strings.TrimPrefix(p, root)), "/") > maxSkillDepth {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == "manifest.json" && len(out) < maxSkillFolders {
			out = append(out, filepath.Dir(p))
		}
		return nil
	})
	return out
}

// copySkill writes the validated folder as plain files. It reads the manifest
// and the rules through a root, so a link in a downloaded folder cannot make
// either one another file of this machine.
func copySkill(src, dest string, m skill.Manifest, prompt string) error {
	root, err := os.OpenRoot(src)
	if err != nil {
		return err
	}
	defer root.Close()
	target := filepath.Join(dest, m.ID)
	for _, name := range []string{"manifest.json", "rules.json"} {
		raw, err := readFromRoot(root, name)
		if errors.Is(err, fs.ErrNotExist) && name == "rules.json" {
			continue
		}
		if err != nil {
			return err
		}
		if err := file.WritePrivate(target, name, raw); err != nil {
			return err
		}
	}
	if prompt == "" {
		return nil
	}
	return file.WritePrivate(target, "prompt.txt", []byte(prompt))
}

// readFromRoot reads one small file of a skill folder under the folder's own
// byte bound.
func readFromRoot(root *os.Root, name string) ([]byte, error) {
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return file.ReadCapped(f, skill.MaxFolderFile)
}
