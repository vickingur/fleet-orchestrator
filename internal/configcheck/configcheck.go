// Package configcheck validates a fleet config before it is pushed: fleet can
// read it, its shared layer holds only files fleet ships, and its agent
// settings keep the guarantees fleet relies on.
package configcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vickingur/fleet-orchestrator/internal/docs"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/syncer"
)

// Problem is one failed check.
type Problem struct {
	Check  string `json:"check"`
	Detail string `json:"detail"`
}

// Run checks the fleet config in dir and returns every problem found.
func Run(dir string) []Problem {
	c := &checker{dir: dir, home: filepath.Join(dir, "home")}
	inv := c.inventory()
	man := c.manifest()
	if inv != nil {
		c.guide(inv)
	}
	c.layerFiles()
	if man != nil {
		c.sources(man)
		c.agentSettings(man)
	}
	c.terminfo()
	c.policy()
	return c.problems
}

type checker struct {
	dir, home string
	problems  []Problem
}

func (c *checker) fail(check, format string, a ...any) {
	c.problems = append(c.problems, Problem{Check: check, Detail: fmt.Sprintf(format, a...)})
}

func (c *checker) inventory() *inventory.Inventory {
	data, err := os.ReadFile(filepath.Join(c.dir, "fleet.toml"))
	if err != nil {
		c.fail("inventory", "%v", err)
		return nil
	}
	inv, err := inventory.Parse(data)
	if err != nil {
		c.fail("inventory", "fleet.toml: %v", err)
		return nil
	}
	return inv
}

func (c *checker) manifest() *syncer.Manifest {
	data, err := os.ReadFile(filepath.Join(c.home, "manifest.toml"))
	if err != nil {
		c.fail("manifest", "%v", err)
		return nil
	}
	man, err := syncer.ParseManifest(data)
	if err != nil {
		c.fail("manifest", "home/manifest.toml: %v", err)
		return nil
	}
	return man
}

// guide keeps the committed FLEET.md equal to what the inventory renders, so
// the guide read in the config never differs from the one nodes get.
func (c *checker) guide(inv *inventory.Inventory) {
	have, err := os.ReadFile(filepath.Join(c.dir, "FLEET.md"))
	if err != nil || !bytes.Equal(have, docs.Render(inv)) {
		c.fail("guide", "FLEET.md is missing or stale; run: fleet docs")
	}
}

// layerFiles rejects what must never be committed into the shared layer.
// Nested VCS metadata once slipped in when a skill was copied with its .git
// directory, which also turned it into a git submodule. Caches fleet already
// skips when shipping are local litter, not errors.
func (c *checker) layerFiles() {
	banned := map[string]bool{".git": true, ".plans": true, ".sessions": true}
	err := filepath.WalkDir(c.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(c.dir, p)
		if banned[d.Name()] || strings.HasSuffix(d.Name(), ".zip") {
			c.fail("layer", "%s is machine state, not shared config", rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		if d.Type()&fs.ModeSymlink != 0 {
			c.fail("layer", "%s is a symlink; fleet ships only regular files", rel)
		}
		return nil
	})
	if err != nil {
		c.fail("layer", "%v", err)
	}
}

// sources requires every manifest source to exist. FLEET.md is rendered into
// the bundle from the inventory, so it has no source file.
func (c *checker) sources(man *syncer.Manifest) {
	for i, f := range man.Files {
		if f.Source == "FLEET.md" {
			continue
		}
		if info, err := os.Stat(filepath.Join(c.home, f.Source)); err != nil || !info.Mode().IsRegular() {
			c.fail("manifest", "file[%d]: source home/%s is not a file", i, f.Source)
		}
	}
	for i, t := range man.Trees {
		if info, err := os.Stat(filepath.Join(c.home, t.Source)); err != nil || !info.IsDir() {
			c.fail("manifest", "tree[%d]: source home/%s is not a directory", i, t.Source)
		}
	}
}

// inertCommands are the only fleet subcommands an agent may run without a
// prompt: doctor runs tool version commands from fleet.toml and drift runs
// manifest ensure checks, so neither may be allow-listed. Only commands with
// fixed behavior are.
var inertCommands = map[string]bool{"ls": true, "containers": true, "version": true}

func (c *checker) agentSettings(man *syncer.Manifest) {
	for _, f := range man.Files {
		switch {
		case f.Target == "~/.claude/settings.json":
			c.claudeSettings(man, f.Source)
		case strings.HasPrefix(f.Target, "~/.codex/rules/"):
			c.codexRules(f.Source)
		}
	}
}

func (c *checker) claudeSettings(man *syncer.Manifest, source string) {
	data, err := os.ReadFile(filepath.Join(c.home, source))
	if err != nil {
		return // reported by sources
	}
	var s struct {
		EnabledPlugins map[string]bool          `json:"enabledPlugins"`
		Permissions    struct{ Allow []string } `json:"permissions"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		c.fail("settings", "home/%s: %v", source, err)
		return
	}
	for _, rule := range s.Permissions.Allow {
		rest, ok := strings.CutPrefix(rule, "Bash(fleet ")
		if !ok {
			continue
		}
		if sub := strings.Fields(strings.TrimSuffix(rest, ")")); len(sub) == 0 || !inertCommands[sub[0]] {
			c.fail("pre-approval", "home/%s pre-approves %q, which can run config-defined code or change nodes", source, rule)
		}
	}
	// A plugin enabled but not installed silently does nothing. A fresh
	// Claude Code has not fetched a marketplace's catalog, so the ensure that
	// adds a plugin's marketplace must come before the one that installs it.
	installs := func(id string) int {
		for i, e := range man.Ensures {
			if strings.HasSuffix(e.Fix, "claude plugin install "+id) {
				return i
			}
		}
		return -1
	}
	addsMarketplace := func(market string) int {
		for i, e := range man.Ensures {
			if strings.Contains(e.Check, market) && strings.Contains(e.Fix, "claude plugin marketplace add ") {
				return i
			}
		}
		return -1
	}
	for id, enabled := range s.EnabledPlugins {
		if !enabled {
			continue
		}
		at := installs(id)
		if at < 0 {
			c.fail("plugins", "plugin %s is enabled in home/%s but no [[ensure]] installs it", id, source)
			continue
		}
		_, market, _ := strings.Cut(id, "@")
		added := addsMarketplace(market)
		switch {
		case added < 0:
			c.fail("plugins", "no [[ensure]] adds marketplace %s, needed by %s on a fresh install", market, id)
		case at < added:
			c.fail("plugins", "plugin %s is installed before its marketplace %s is added", id, market)
		}
	}
}

func (c *checker) codexRules(source string) {
	data, err := os.ReadFile(filepath.Join(c.home, source))
	if err != nil {
		return // reported by sources
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, `prefix_rule(pattern=["fleet", "`)
		if !ok {
			continue
		}
		if sub, _, _ := strings.Cut(rest, `"`); !inertCommands[sub] {
			c.fail("pre-approval", "home/%s pre-approves fleet %s, which can run config-defined code or change nodes", source, sub)
		}
	}
}

// terminfo compiles every terminal definition the layer ships, with the tic
// flags a node's ensure would use.
func (c *checker) terminfo() {
	var files []string
	_ = filepath.WalkDir(c.home, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && strings.HasSuffix(p, ".terminfo") {
			files = append(files, p)
		}
		return nil
	})
	if len(files) == 0 {
		return
	}
	tic, err := exec.LookPath("tic")
	if err != nil {
		c.fail("terminfo", "tic is not installed, so %d shipped terminal definition(s) cannot be checked", len(files))
		return
	}
	out, err := os.MkdirTemp("", "fleet-check-terminfo-")
	if err != nil {
		c.fail("terminfo", "%v", err)
		return
	}
	defer func() { _ = os.RemoveAll(out) }()
	for _, f := range files {
		rel, _ := filepath.Rel(c.dir, f)
		if msg, err := exec.Command(tic, "-x", "-o", out, f).CombinedOutput(); err != nil {
			c.fail("terminfo", "%s does not compile: %v: %s", rel, err, strings.TrimSpace(string(msg)))
		}
	}
}
