package syncer

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	t      *testing.T
	bundle string
	home   string
	state  string
	backup string
}

func newFixture(t *testing.T) *fixture {
	root := t.TempDir()
	f := &fixture{t: t, bundle: filepath.Join(root, "bundle"), home: filepath.Join(root, "home"),
		state: filepath.Join(root, "state.json"), backup: filepath.Join(root, "backup")}
	return f
}

func (f *fixture) write(base, rel, content string, mode os.FileMode) {
	f.t.Helper()
	p := filepath.Join(base, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(rel string) string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, rel))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.home, rel))
	return err == nil
}

const manifestTOML = `
[[file]]
source = "instructions.md"
target = "~/.claude/CLAUDE.md"
merge = "block"

[[file]]
source = "claude-settings.json"
target = "~/.claude/settings.json"
merge = "json"

[[file]]
source = "codex-config.toml"
target = "~/.codex/config.toml"
merge = "toml"

[[tree]]
source = "skills"
target = "~/.claude/skills"
exclude = ["*.zip"]
`

func (f *fixture) seedBundle() {
	f.write(f.bundle, "instructions.md", "Be concise.\n", 0o644)
	f.write(f.bundle, "claude-settings.json", `{"remoteControlAtStartup": true, "permissions": {"allow": ["Bash(fleet ls)"]}}`, 0o644)
	f.write(f.bundle, "codex-config.toml", "model_reasoning_effort = \"high\"\n[features]\nmulti_agent = true\n", 0o644)
	f.write(f.bundle, "skills/demo/SKILL.md", "demo skill v1\n", 0o644)
	f.write(f.bundle, "skills/demo/run.sh", "#!/bin/sh\necho hi\n", 0o755)
	f.write(f.bundle, "skills/demo/.git/HEAD", "ref\n", 0o644)
	f.write(f.bundle, "skills/demo/archive.zip", "zip\n", 0o644)
	f.write(f.bundle, "skills/demo/scripts/__pycache__/x.cpython-313.pyc", "bytecode", 0o644)
	f.write(f.bundle, "skills/demo/scripts/y.pyc", "bytecode", 0o644)
}

func (f *fixture) plan(force bool) Plan {
	f.t.Helper()
	man, err := ParseManifest([]byte(manifestTOML))
	if err != nil {
		f.t.Fatal(err)
	}
	return f.planWith(man, force)
}

func (f *fixture) planWith(man *Manifest, force bool) Plan {
	f.t.Helper()
	st, err := LoadState(f.state, f.backup)
	if err != nil {
		f.t.Fatal(err)
	}
	p, err := Build(man, st, Options{Bundle: f.bundle, Home: f.home, Force: force})
	if err != nil {
		f.t.Fatal(err)
	}
	return p
}

// apply returns only real failures; conflicts are inspected via applyResult.
func (f *fixture) apply(p Plan) error {
	_, err := f.applyResult(p)
	return err
}

func (f *fixture) applyResult(p Plan) (ApplyResult, error) {
	return p.Apply(ApplyOptions{Home: f.home, StateFile: f.state, BackupDir: f.backup})
}

func actions(p Plan) map[string]Action {
	out := map[string]Action{}
	for _, c := range p.Changes {
		out[c.Target] = c.Action
	}
	return out
}

func TestFreshApplyThenIdempotent(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	p := f.plan(false)
	got := actions(p)
	for _, target := range []string{"~/.claude/CLAUDE.md", "~/.claude/settings.json", "~/.codex/config.toml",
		"~/.claude/skills/demo/SKILL.md", "~/.claude/skills/demo/run.sh"} {
		if got[target] != Create {
			t.Errorf("%s: %s, want create", target, got[target])
		}
	}
	if _, ok := got["~/.claude/skills/demo/.git/HEAD"]; ok {
		t.Error(".git shipped")
	}
	if _, ok := got["~/.claude/skills/demo/archive.zip"]; ok {
		t.Error("excluded *.zip shipped")
	}
	for target := range got {
		if strings.Contains(target, "__pycache__") || strings.HasSuffix(target, ".pyc") {
			t.Errorf("build cache shipped: %s", target)
		}
	}
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(f.home, ".claude/skills/demo/run.sh"))
	if info.Mode().Perm() != 0o755 {
		t.Errorf("exec bit lost: %o", info.Mode().Perm())
	}
	if again := f.plan(false); again.Pending() {
		t.Fatalf("second plan not idempotent: %+v", again.Changes)
	}
}

func TestStructuredMergeKeepsLocalKeys(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/settings.json", `{"theme": "dark", "permissions": {"allow": ["Bash(ls)"]}}`, 0o600)
	f.write(f.home, ".codex/config.toml", "model = \"gpt-6-sol\"\n[projects.\"/home/u/x\"]\ntrust_level = \"trusted\"\n", 0o644)
	p := f.plan(false)
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	settings := f.read(".claude/settings.json")
	for _, want := range []string{`"theme": "dark"`, `"Bash(ls)"`, `"Bash(fleet ls)"`, `"remoteControlAtStartup": true`} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings missing %s:\n%s", want, settings)
		}
	}
	info, _ := os.Stat(filepath.Join(f.home, ".claude/settings.json"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("merge changed file mode to %o", info.Mode().Perm())
	}
	codex := f.read(".codex/config.toml")
	for _, want := range []string{`model = "gpt-6-sol"`, `model_reasoning_effort = "high"`, `multi_agent = true`, `[projects."/home/u/x"]`} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex config missing %s:\n%s", want, codex)
		}
	}
	if backup, err := os.ReadFile(filepath.Join(f.backup, ".claude/settings.json")); err != nil || !strings.Contains(string(backup), `"theme": "dark"`) {
		t.Errorf("no backup of original settings: %v", err)
	}
}

func TestOverlayKeyRemovalIsWithdrawn(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/settings.json", `{"theme": "dark"}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	f.write(f.bundle, "claude-settings.json", `{"permissions": {"allow": ["Bash(fleet ls)"]}}`, 0o644)
	p := f.plan(false)
	var detail string
	for _, c := range p.Changes {
		if c.Target == "~/.claude/settings.json" {
			detail = c.Detail
		}
	}
	if detail != "-remoteControlAtStartup" {
		t.Fatalf("detail = %q", detail)
	}
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	if s := f.read(".claude/settings.json"); strings.Contains(s, "remoteControl") || !strings.Contains(s, "dark") {
		t.Fatalf("settings = %s", s)
	}
}

func TestLocalEditIsAConflictNotOverwritten(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	f.write(f.home, ".claude/skills/demo/SKILL.md", "my local tweak\n", 0o644)
	f.write(f.bundle, "skills/demo/SKILL.md", "demo skill v2\n", 0o644)
	p := f.plan(false)
	if got := actions(p)["~/.claude/skills/demo/SKILL.md"]; got != Conflict {
		t.Fatalf("action = %s, want conflict", got)
	}
	res, err := f.applyResult(p)
	if err != nil || len(res.Conflicts) != 1 || len(res.Failed) != 0 {
		t.Fatalf("res = %+v, err = %v; want exactly one conflict and no failure", res, err)
	}
	if got := f.read(".claude/skills/demo/SKILL.md"); got != "my local tweak\n" {
		t.Fatalf("local edit overwritten: %q", got)
	}
	// The conflict persists until resolved: state still records fleet's last write.
	if again := actions(f.plan(false))["~/.claude/skills/demo/SKILL.md"]; again != Conflict {
		t.Fatalf("conflict forgotten after apply: %s", again)
	}
	forced := f.plan(true)
	if err := f.apply(forced); err != nil {
		t.Fatal(err)
	}
	if got := f.read(".claude/skills/demo/SKILL.md"); got != "demo skill v2\n" {
		t.Fatalf("force did not overwrite: %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(f.backup, ".claude/skills/demo/SKILL.md")); string(b) != "my local tweak\n" {
		t.Fatalf("forced overwrite not backed up: %q", b)
	}
}

func TestUntrackedDifferentFileIsAConflict(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/skills/demo/SKILL.md", "someone else's skill\n", 0o644)
	p := f.plan(false)
	for _, c := range p.Changes {
		if c.Target == "~/.claude/skills/demo/SKILL.md" && (c.Action != Conflict || !strings.Contains(c.Detail, "did not write")) {
			t.Fatalf("change = %+v", c)
		}
	}
}

func TestIdenticalExistingFileIsAdopted(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/skills/demo/SKILL.md", "demo skill v1\n", 0o644)
	p := f.plan(false)
	if got := actions(p)["~/.claude/skills/demo/SKILL.md"]; got != Unchanged {
		t.Fatalf("identical file: %s", got)
	}
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	f.write(f.bundle, "skills/demo/SKILL.md", "demo skill v2\n", 0o644)
	if got := actions(f.plan(false))["~/.claude/skills/demo/SKILL.md"]; got != Update {
		t.Fatalf("adopted file should update cleanly, got %s", got)
	}
}

func TestUnmanagedSiblingsSurviveAndRemovedFilesArePruned(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/skills/local-only/SKILL.md", "mine\n", 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.bundle, "skills/demo/run.sh")); err != nil {
		t.Fatal(err)
	}
	p := f.plan(false)
	if got := actions(p)["~/.claude/skills/demo/run.sh"]; got != Delete {
		t.Fatalf("removed file action = %s", got)
	}
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	if f.exists(".claude/skills/demo/run.sh") {
		t.Fatal("pruned file still present")
	}
	if !f.exists(".claude/skills/local-only/SKILL.md") {
		t.Fatal("unmanaged sibling deleted")
	}
	if f.plan(false).Pending() {
		t.Fatal("not idempotent after prune")
	}
}

func TestPruneKeepsLocallyEditedFile(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	f.write(f.home, ".claude/skills/demo/run.sh", "#!/bin/sh\necho edited\n", 0o755)
	if err := os.Remove(filepath.Join(f.bundle, "skills/demo/run.sh")); err != nil {
		t.Fatal(err)
	}
	p := f.plan(false)
	if got := actions(p)["~/.claude/skills/demo/run.sh"]; got != Release {
		t.Fatalf("action = %s, want release", got)
	}
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	if !f.exists(".claude/skills/demo/run.sh") {
		t.Fatal("locally edited file deleted")
	}
	if f.plan(false).Pending() {
		t.Fatal("released file still tracked")
	}
}

func TestRemovedRulesUndoTheirMerges(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/CLAUDE.md", "# my notes\n", 0o644)
	f.write(f.home, ".claude/settings.json", `{"theme": "dark"}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.read(".claude/CLAUDE.md"), "Be concise.") {
		t.Fatal("block not written")
	}
	empty, _ := ParseManifest([]byte(""))
	p := f.planWith(empty, false)
	if err := f.apply(p); err != nil {
		t.Fatal(err)
	}
	if got := f.read(".claude/CLAUDE.md"); got != "# my notes\n" {
		t.Fatalf("block not removed: %q", got)
	}
	if got := f.read(".claude/settings.json"); strings.Contains(got, "remoteControl") || !strings.Contains(got, "dark") {
		t.Fatalf("settings after rule removal = %s", got)
	}
	if f.exists(".claude/skills/demo/SKILL.md") {
		t.Fatal("skill from removed tree rule kept")
	}
	if f.planWith(empty, false).Pending() {
		t.Fatal("state not cleared after rule removal")
	}
}

func TestInvalidTargetFailsLoud(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/settings.json", `{not json`, 0o644)
	man, _ := ParseManifest([]byte(manifestTOML))
	st, _ := LoadState(f.state, f.backup)
	_, err := Build(man, st, Options{Bundle: f.bundle, Home: f.home})
	if err == nil || !strings.Contains(err.Error(), "settings.json: target not valid JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestDuplicateTargetsRejected(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	man, _ := ParseManifest([]byte(manifestTOML + `
[[file]]
source = "instructions.md"
target = "~/.claude/skills/demo/SKILL.md"
merge = "replace"
`))
	st, _ := LoadState(f.state, f.backup)
	_, err := Build(man, st, Options{Bundle: f.bundle, Home: f.home})
	if err == nil || !strings.Contains(err.Error(), "claimed by both") {
		t.Fatalf("err = %v", err)
	}
}

func TestSymlinkInBundleRejected(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	if err := os.Symlink("/etc/passwd", filepath.Join(f.bundle, "skills/demo/link")); err != nil {
		t.Fatal(err)
	}
	man, _ := ParseManifest([]byte(manifestTOML))
	st, _ := LoadState(f.state, f.backup)
	if _, err := Build(man, st, Options{Bundle: f.bundle, Home: f.home}); err == nil || !strings.Contains(err.Error(), "only regular files") {
		t.Fatalf("err = %v", err)
	}
}

func TestManifestValidation(t *testing.T) {
	for name, src := range map[string]string{
		"merge":      "[[file]]\nsource=\"a\"\ntarget=\"~/a\"\nmerge=\"yaml\"\n",
		"escape":     "[[file]]\nsource=\"../a\"\ntarget=\"~/a\"\nmerge=\"replace\"\n",
		"abs target": "[[tree]]\nsource=\"a\"\ntarget=\"/etc/a\"\n",
		"dotdot":     "[[tree]]\nsource=\"a\"\ntarget=\"~/x/../../etc\"\n",
		"unknown":    "[[tree]]\nsource=\"a\"\ntarget=\"~/a\"\nexclud=[]\n",
		"ensure":     "[[ensure]]\nname=\"x\"\ncheck=\"true\"\n",
		"bad glob":   "[[tree]]\nsource=\"a\"\ntarget=\"~/a\"\nexclude=[\"secrets[.json\"]\n",
		"dup ensure": "[[ensure]]\nname=\"x\"\ncheck=\"a\"\nfix=\"b\"\n[[ensure]]\nname=\"x\"\ncheck=\"a\"\nfix=\"b\"\n",
		"snapshot":   "[[ensure]]\nname=\"x\"\ncheck=\"a\"\nfix=\"b\"\nsnapshot=[\"/etc/passwd\"]\n",
	} {
		if _, err := ParseManifest([]byte(src)); err == nil {
			t.Errorf("%s: invalid manifest accepted", name)
		}
	}
}

func TestAlwaysExcludedPatternsAreValid(t *testing.T) {
	for _, pat := range alwaysExcluded {
		if _, err := path.Match(pat, ""); err != nil {
			t.Errorf("%q: %v", pat, err)
		}
	}
	if !IsMachineState("x.pyc") || !IsMachineState("node_modules") || IsMachineState("SKILL.md") {
		t.Fatal("IsMachineState misclassifies")
	}
}

func TestStateCorruptOrFutureFailsLoud(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(bad, dir); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt: %v", err)
	}
	future := filepath.Join(dir, "future.json")
	if err := os.WriteFile(future, []byte(`{"version": 9, "files": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(future, dir); err == nil || !strings.Contains(err.Error(), "version 9") {
		t.Fatalf("future: %v", err)
	}
}

// realisticCodex mirrors the shapes found in real ~/.codex/config.toml files.
const realisticCodex = `model = "gpt-6-sol"
approval_policy = "never"
notify = ["/Applications/X.app/Contents/MacOS/client", "turn-ended"]

[features]
js_repl = false

[plugins."github@openai-curated"]
enabled = true

[projects."/home/example/workspace/a"]
trust_level = "trusted"

[mcp_servers.node_repl]
command = "/usr/lib/node_repl"
args = []
startup_timeout_sec = 120

[mcp_servers.node_repl.env]
NODE_OPTIONS = "--max-old-space-size=4096"

[mcp_servers.computer-use.tools.hypr]
approval_mode = "approve"

[tui]
status_line = ["model-with-reasoning", "git-branch"]

[[skills.config]]
path = "/a/SKILL.md"
enabled = false
`

func TestTOMLRoundTripPreservesEverything(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".codex/config.toml", realisticCodex, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	before, _ := decode(MergeTOML, []byte(realisticCodex))
	after, err := decode(MergeTOML, []byte(f.read(".codex/config.toml")))
	if err != nil {
		t.Fatal(err)
	}
	am := after.(map[string]any)
	if am["model_reasoning_effort"] != "high" || am["features"].(map[string]any)["multi_agent"] != true {
		t.Fatalf("overlay not merged: %v", am)
	}
	delete(am, "model_reasoning_effort")
	delete(am["features"].(map[string]any), "multi_agent")
	if d := diffPaths(before, after); len(d) != 0 {
		t.Fatalf("round trip changed local config: %v", d)
	}
}

func TestSymlinkedTargetIsUpdatedThroughTheLink(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	dotfiles := filepath.Join(t.TempDir(), "dotfiles")
	f.write(dotfiles, "settings.json", `{"theme": "dark"}`, 0o644)
	if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, ".claude/settings.json")
	if err := os.Symlink(filepath.Join(dotfiles, "settings.json"), link); err != nil {
		t.Fatal(err)
	}
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced by a regular file: %v", info.Mode())
	}
	real, _ := os.ReadFile(filepath.Join(dotfiles, "settings.json"))
	if !strings.Contains(string(real), "remoteControlAtStartup") || !strings.Contains(string(real), "dark") {
		t.Fatalf("link target not merged: %s", real)
	}
	if f.plan(false).Pending() {
		t.Fatal("not idempotent through the link")
	}
}

func TestDanglingSymlinkTargetFailsLoud(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	if err := os.MkdirAll(filepath.Join(f.home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent/settings.json", filepath.Join(f.home, ".claude/settings.json")); err != nil {
		t.Fatal(err)
	}
	man, _ := ParseManifest([]byte(manifestTOML))
	st, _ := LoadState(f.state, f.backup)
	if _, err := Build(man, st, Options{Bundle: f.bundle, Home: f.home}); err == nil || !strings.Contains(err.Error(), "symlink that cannot be followed") {
		t.Fatalf("err = %v", err)
	}
}

// Regression: a write failure alongside a conflict used to be reported only
// as the conflict, hiding the failure and exiting with the conflict code.
func TestWriteFailureIsNotHiddenByAConflict(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/skills/demo/SKILL.md", "someone else's\n", 0o644)
	locked := filepath.Join(f.home, ".codex")
	if err := os.MkdirAll(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	res, err := f.applyResult(f.plan(false))
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "~/.claude/skills/demo/SKILL.md" {
		t.Fatalf("conflicts = %v", res.Conflicts)
	}
	if err == nil || !strings.Contains(err.Error(), "~/.codex/config.toml") || len(res.Failed) == 0 {
		t.Fatalf("write failure hidden: res = %+v, err = %v", res, err)
	}
	// Everything that could be written was, and state records it.
	if !f.exists(".claude/CLAUDE.md") {
		t.Fatal("writable targets were not applied")
	}
	if actions(f.plan(false))["~/.claude/CLAUDE.md"] != Unchanged {
		t.Fatal("state not saved for the changes that succeeded")
	}
}

// Regression: a key the machine already had with the overlay's value was
// deleted when the overlay later dropped it, because fleet assumed it owned it.
func TestPreexistingValuesSurviveOverlayRemoval(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.home, ".claude/settings.json", `{"remoteControlAtStartup": true, "permissions": {"allow": ["Bash(fleet ls)", "Bash(ls)"]}}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	f.write(f.bundle, "claude-settings.json", `{"effortLevel": "high"}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	got := f.read(".claude/settings.json")
	for _, want := range []string{`"remoteControlAtStartup": true`, `"Bash(fleet ls)"`, `"Bash(ls)"`, `"effortLevel": "high"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s after overlay dropped it; it was the machine's own:\n%s", want, got)
		}
	}
}

func TestIntroducedSubtreeDropsOnlyFleetSubkeys(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	f.write(f.bundle, "claude-settings.json", `{"env": {"A": "1", "B": "2"}}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	// The user adds their own variable inside the block fleet created.
	f.write(f.home, ".claude/settings.json", `{"env": {"A": "1", "B": "2", "MINE": "x"}}`, 0o644)
	f.write(f.bundle, "claude-settings.json", `{"env": {"A": "1"}}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	got := f.read(".claude/settings.json")
	if strings.Contains(got, `"B"`) || !strings.Contains(got, `"MINE": "x"`) || !strings.Contains(got, `"A": "1"`) {
		t.Fatalf("settings = %s", got)
	}
	f.write(f.bundle, "claude-settings.json", `{}`, 0o644)
	if err := f.apply(f.plan(false)); err != nil {
		t.Fatal(err)
	}
	if got := f.read(".claude/settings.json"); strings.Contains(got, `"A"`) || !strings.Contains(got, `"MINE": "x"`) {
		t.Fatalf("withdrawing env removed the user's key or kept fleet's: %s", got)
	}
}

func TestUpgradeV1StateFromBackups(t *testing.T) {
	f := newFixture(t)
	f.seedBundle()
	overlay := `{"remoteControlAtStartup": true, "permissions": {"allow": ["Bash(fleet ls)"]}}`
	// What the machine had before fleet's first apply, kept as that apply's backup.
	f.write(filepath.Join(f.backup, "20261002T000000Z"), ".claude/settings.json", `{"theme": "dark", "remoteControlAtStartup": true}`, 0o600)
	f.write(f.home, ".claude/settings.json", `{"theme": "dark", "remoteControlAtStartup": true, "permissions": {"allow": ["Bash(fleet ls)"]}}`, 0o644)
	v1 := map[string]any{"version": 1, "files": map[string]any{
		"~/.claude/settings.json": map[string]any{"rule": "json:claude-settings.json->~/.claude/settings.json", "merge": "json", "overlay": overlay},
		"~/.codex/config.toml":    map[string]any{"rule": "toml:codex-config.toml->~/.codex/config.toml", "merge": "toml", "overlay": "model_reasoning_effort = \"high\"\n"},
	}}
	data, _ := json.Marshal(v1)
	f.write(filepath.Dir(f.state), filepath.Base(f.state), string(data), 0o600)

	st, err := LoadState(f.state, f.backup)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != stateVersion {
		t.Fatalf("version = %d", st.Version)
	}
	settings := fmt.Sprint(st.Files["~/.claude/settings.json"].Introduced)
	if strings.Contains(settings, "remoteControlAtStartup") || !strings.Contains(settings, "permissions") {
		t.Fatalf("settings contributions = %s; the pre-existing key must not count as fleet's", settings)
	}
	codex := st.Files["~/.codex/config.toml"].Introduced
	if len(codex) != 1 || codex[0].Path[0] != "model_reasoning_effort" {
		t.Fatalf("codex contributions = %+v; with no backup fleet created the file, so it owns every key", codex)
	}
}
