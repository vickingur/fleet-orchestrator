package bundle

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestSharedLayerHasNoMachineState guards the repo's home/ directory against
// VCS metadata, caches and archives. These once slipped in when a skill was
// copied with its .git directory, which also turned it into a git submodule.
func TestSharedLayerHasNoMachineState(t *testing.T) {
	root := filepath.Join(repoRoot(t), "home")
	banned := map[string]bool{".git": true, "__pycache__": true, ".ruff_cache": true, ".pytest_cache": true,
		"node_modules": true, ".DS_Store": true, ".plans": true, ".sessions": true}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		if banned[d.Name()] || strings.HasSuffix(d.Name(), ".pyc") || strings.HasSuffix(d.Name(), ".zip") {
			t.Errorf("home/%s is machine state, not shared config", rel)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		if d.Type()&fs.ModeSymlink != 0 {
			t.Errorf("home/%s is a symlink; the sync engine only ships regular files", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEveryEnabledPluginIsInstalled keeps the shared settings and the manifest
// in step: a plugin enabled but not installed silently does nothing.
func TestEveryEnabledPluginIsInstalled(t *testing.T) {
	home := filepath.Join(repoRoot(t), "home")
	settings, err := os.ReadFile(filepath.Join(home, "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		EnabledPlugins map[string]bool `json:"enabledPlugins"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(home, "manifest.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Ensure []struct{ Name, Check, Fix string } `toml:"ensure"`
	}
	if _, err := toml.Decode(string(manifest), &m); err != nil {
		t.Fatal(err)
	}
	installs := func(id string) bool {
		for _, e := range m.Ensure {
			if strings.HasSuffix(e.Fix, "claude plugin install "+id) {
				return true
			}
		}
		return false
	}
	addsMarketplace := func(market string) int {
		for i, e := range m.Ensure {
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
		if !installs(id) {
			t.Errorf("plugin %s is enabled in claude-settings.json but no [[ensure]] installs it", id)
		}
		_, market, _ := strings.Cut(id, "@")
		at := addsMarketplace(market)
		if at < 0 {
			t.Errorf("no [[ensure]] adds marketplace %s, needed by %s on a fresh install", market, id)
			continue
		}
		for i, e := range m.Ensure {
			if strings.HasSuffix(e.Fix, "claude plugin install "+id) && i < at {
				t.Errorf("plugin %s is ensured before its marketplace %s is added", id, market)
			}
		}
	}
}

// TestOnlyInertFleetCommandsArePreApproved keeps agents from running repo- or
// inventory-defined code on every node without a prompt: doctor runs tool
// version commands from fleet.toml and drift runs manifest ensure checks, so
// neither may be allow-listed. Only commands with fixed behavior are.
func TestOnlyInertFleetCommandsArePreApproved(t *testing.T) {
	inert := map[string]bool{"ls": true, "containers": true, "version": true}
	home := filepath.Join(repoRoot(t), "home")

	settings, err := os.ReadFile(filepath.Join(home, "claude-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Permissions struct{ Allow []string } `json:"permissions"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		t.Fatal(err)
	}
	for _, rule := range s.Permissions.Allow {
		rest, ok := strings.CutPrefix(rule, "Bash(fleet ")
		if !ok {
			continue
		}
		sub := strings.Fields(strings.TrimSuffix(rest, ")"))[0]
		if !inert[sub] {
			t.Errorf("claude settings pre-approve %q, which can run repo-defined code or change nodes", rule)
		}
	}

	rules, err := os.ReadFile(filepath.Join(home, "codex-fleet.rules"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(rules), "\n") {
		rest, ok := strings.CutPrefix(line, `prefix_rule(pattern=["fleet", "`)
		if !ok {
			continue
		}
		sub, _, _ := strings.Cut(rest, `"`)
		if !inert[sub] {
			t.Errorf("codex rules pre-approve fleet %s, which can run repo-defined code or change nodes", sub)
		}
	}
}
