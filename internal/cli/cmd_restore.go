package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/vickingur/fleet-orchestrator/internal/syncer"
)

// --- restore ---------------------------------------------------------------

type restoreOutput struct {
	Set      string   `json:"set"`
	Restored []string `json:"restored"`
	SavedTo  string   `json:"saved_to"`
}

// cmdRestore lists the backup sets fleet has taken on this machine, or copies
// one set's files back into place. The files it overwrites are saved into a new
// set first, so the restore itself can be restored.
func (a *App) cmdRestore(_ context.Context, args []string) error {
	fs := newFlags("restore")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	root := filepath.Join(a.stateDir(), "backup")
	if fs.NArg() == 0 {
		sets, err := syncer.ListBackups(root)
		if err != nil {
			return err
		}
		if *asJSON {
			return writeJSON(a.out, sets)
		}
		if len(sets) == 0 {
			a.println("no backups yet; fleet apply saves a set whenever it changes a file")
			return nil
		}
		for _, s := range sets {
			a.printf("%s  %d file(s)\n", s.Name, len(s.Files))
			for _, f := range s.Files {
				a.printf("  %s\n", f)
			}
		}
		a.println("restore one with: fleet restore <set> [~/path ...]")
		return nil
	}
	set, only := fs.Arg(0), fs.Args()[1:]
	for i, k := range only {
		// The shell expands ~ before fleet sees it; map the absolute form back
		// onto the "~/..." key a listing shows.
		if rel, ok := strings.CutPrefix(k, a.Home+"/"); ok {
			only[i] = "~/" + rel
			continue
		}
		if !strings.HasPrefix(k, "~/") {
			return usagef("paths are the ~/... keys a listing shows, not %q", k)
		}
	}
	saved := filepath.Join(root, a.Now().UTC().Format("20060102T150405.000000000Z"))
	restored, err := syncer.Restore(root, set, a.Home, only, saved)
	// Files restored before a failure are already in place; report them and
	// where their replaced content went, so the partial restore can be undone.
	out := restoreOutput{Set: set, Restored: restored, SavedTo: saved}
	if *asJSON {
		if werr := writeJSON(a.out, out); werr != nil {
			return werr
		}
		return err
	}
	for _, k := range restored {
		a.printf("%-9s %s\n", "restored", k)
	}
	if err != nil {
		a.printf("restore stopped after %d file(s); the replaced content is in %s\n", len(restored), saved)
		return err
	}
	a.printf("restored %d file(s) from %s; the replaced content is in %s\n", len(restored), set, saved)
	a.println("next apply: a restored whole-file target (skills, rules, FLEET.md) reads as a local edit and is left alone; keep it with fleet adopt, or take the shared version with fleet apply --force")
	a.println("a restored merged file (settings.json, config.toml, the CLAUDE.md block) gets the overlay's values applied again; to keep restored values, change the overlay in the repo first")
	a.println("plugins run from an install-time cache, not from these files; the ensures rebuild the caches on the next fleet apply")
	return nil
}
