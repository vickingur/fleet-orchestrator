package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

// The set an apply writes under the harness clock.
const harnessSet = "20261002T120000.000000000Z"

func TestEnsureSnapshotsRegistriesBeforeTheFix(t *testing.T) {
	h := newHarness(t)
	h.app.Runner = execx.OS{}
	bundle := filepath.Join(t.TempDir(), "bundle")
	writeBundle(t, bundle, "v1\n")
	registry := filepath.Join(h.app.Home, ".claude/plugins/installed_plugins.json")
	if err := os.MkdirAll(filepath.Dir(registry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registry, []byte(`{"plugins":"before"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	mark, mark2 := filepath.Join(t.TempDir(), "installed"), filepath.Join(t.TempDir(), "installed2")
	// Two ensures touch the same registry; the set must keep the content from before either fix.
	manifest := fmt.Sprintf(`[[ensure]]
name = "plugin"
check = "test -f %[1]s"
fix = "printf '{\"plugins\":\"after\"}' > %[2]s && touch %[1]s"
snapshot = ["~/.claude/plugins/installed_plugins.json", "~/.claude/plugins/known_marketplaces.json"]

[[ensure]]
name = "plugin two"
check = "test -f %[3]s"
fix = "printf '{\"plugins\":\"after two\"}' > %[2]s && touch %[3]s"
snapshot = ["~/.claude/plugins/installed_plugins.json"]
`, mark, registry, mark2)
	if err := os.WriteFile(filepath.Join(bundle, "home/manifest.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	h.env["FLEET_BUNDLE"] = bundle

	if code := h.run("apply", "--dry-run"); code != exitOK {
		t.Fatalf("dry run: %d %s", code, h.errb)
	}
	if _, err := os.Stat(filepath.Join(h.app.Home, ".local/state/fleet/backup", harnessSet)); err == nil {
		t.Fatal("dry run took a snapshot")
	}
	if code := h.run("apply", "--json"); code != exitOK {
		t.Fatalf("apply: %d %s", code, h.errb)
	}
	var out applyOutput
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// Only the file that existed is listed; the missing registry is not an error.
	if len(out.Ensures) != 2 || !out.Ensures[0].Fixed || !out.Ensures[1].Fixed || strings.Join(out.Ensures[0].Snapshot, ",") != "~/.claude/plugins/installed_plugins.json" {
		t.Fatalf("ensure result: %+v", out.Ensures)
	}
	saved, err := os.ReadFile(filepath.Join(h.app.Home, ".local/state/fleet/backup", harnessSet, ".claude/plugins/installed_plugins.json"))
	if err != nil || string(saved) != `{"plugins":"before"}` {
		t.Fatalf("snapshot = %q, %v (the second fix must not overwrite the first snapshot)", saved, err)
	}
	if now, _ := os.ReadFile(registry); string(now) != `{"plugins":"after two"}` {
		t.Fatalf("fixes did not run in order: %q", now)
	}
}

func TestRestoreListsSetsAndPutsOneBack(t *testing.T) {
	h := newHarness(t)
	root := filepath.Join(h.app.Home, ".local/state/fleet/backup")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code := h.run("restore"); code != exitOK || !strings.Contains(h.out.String(), "no backups yet") {
		t.Fatalf("empty listing: %d %q", code, h.out)
	}
	write(filepath.Join(root, "setA/.claude/settings.json"), "old\n")
	write(filepath.Join(h.app.Home, ".claude/settings.json"), "current\n")

	if code := h.run("restore"); code != exitOK || !strings.Contains(h.out.String(), "setA  1 file(s)\n  ~/.claude/settings.json") {
		t.Fatalf("listing: %d\n%s", code, h.out)
	}
	if code := h.run("restore", "setA", "settings.json"); code != exitUsage {
		t.Fatalf("a bare path was accepted: %d", code)
	}
	if code := h.run("restore", "setA", "~/.claude/nope"); code != exitError || !strings.Contains(h.errb.String(), "does not hold") {
		t.Fatalf("unknown key: %d %q", code, h.errb)
	}
	// A shell-expanded home path selects the same key as its ~/ form.
	if code := h.run("restore", "setA", filepath.Join(h.app.Home, ".claude/settings.json")); code != exitOK || !strings.Contains(h.out.String(), "restored  ~/.claude/settings.json") {
		t.Fatalf("absolute path: %d\n%s%s", code, h.out, h.errb)
	}
	if err := os.WriteFile(filepath.Join(h.app.Home, ".claude/settings.json"), []byte("current\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := h.run("restore", "--json", "setA"); code != exitOK {
		t.Fatalf("restore: %d %s", code, h.errb)
	}
	var out restoreOutput
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.Set != "setA" || len(out.Restored) != 1 || !strings.HasSuffix(out.SavedTo, harnessSet) {
		t.Fatalf("json = %+v, %v", out, err)
	}
	if got, _ := os.ReadFile(filepath.Join(h.app.Home, ".claude/settings.json")); string(got) != "old\n" {
		t.Fatalf("after restore: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(root, harnessSet, ".claude/settings.json")); string(got) != "current\n" {
		t.Fatalf("replaced content not saved: %q", got)
	}
	if code := h.run("restore", "setA"); code != exitOK || !strings.Contains(h.out.String(), "restored  ~/.claude/settings.json") || !strings.Contains(h.out.String(), "fleet adopt") {
		t.Fatalf("plain output: %d\n%s", code, h.out)
	}

	// A failure on the second file still reports the first, which is already in place.
	write(filepath.Join(root, "setP/.claude/a"), "a\n")
	write(filepath.Join(root, "setP/.claude/b"), "b\n")
	if err := os.Symlink("/nonexistent/b", filepath.Join(h.app.Home, ".claude/b")); err != nil {
		t.Fatal(err)
	}
	if code := h.run("restore", "--json", "setP"); code != exitError {
		t.Fatalf("partial restore: %d %q", code, h.errb)
	}
	var partial restoreOutput
	if err := json.Unmarshal(h.out.Bytes(), &partial); err != nil || strings.Join(partial.Restored, ",") != "~/.claude/a" || partial.SavedTo == "" {
		t.Fatalf("partial json = %+v, %v (stderr %q)", partial, err, h.errb)
	}
	if code := h.run("restore", "setP"); code != exitError || !strings.Contains(h.out.String(), "restored  ~/.claude/a") || !strings.Contains(h.out.String(), "stopped after 1 file(s)") {
		t.Fatalf("partial plain output: %d\n%s", code, h.out)
	}
}
