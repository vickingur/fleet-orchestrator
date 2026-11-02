package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeConfig lays out a minimal fleet config in dir.
func makeConfig(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"fleet.toml": testInventory, "home/manifest.toml": ""} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fleet docs writes FLEET.md into whichever config fleet found, which makes
// the lookup order observable: FLEET_CONFIG, then the config fleet runs in,
// then the default location.
func TestConfigLookupOrder(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "FLEET_CONFIG")
	inCwd := filepath.Join(t.TempDir(), "cwd-config")
	makeConfig(t, inCwd)
	def := h.app.DefaultConfigDir()
	makeConfig(t, def)
	wrote := func(dir string) bool {
		_, err := os.Stat(filepath.Join(dir, "FLEET.md"))
		return err == nil
	}

	h.app.Cwd = filepath.Join(inCwd, "home")
	h.env["FLEET_CONFIG"] = h.cfg
	if code := h.run("docs"); code != 0 || !wrote(h.cfg) || wrote(inCwd) || wrote(def) {
		t.Fatalf("FLEET_CONFIG did not win: exit %d %s", code, h.errb)
	}

	delete(h.env, "FLEET_CONFIG")
	if code := h.run("docs"); code != 0 || !wrote(inCwd) || wrote(def) {
		t.Fatalf("the config fleet runs in did not win over the default: exit %d %s", code, h.errb)
	}

	h.app.Cwd = t.TempDir()
	if code := h.run("docs"); code != 0 || !wrote(def) {
		t.Fatalf("default config not used: exit %d %s", code, h.errb)
	}

	if err := os.RemoveAll(def); err != nil {
		t.Fatal(err)
	}
	if code := h.run("docs"); code == 0 || !strings.Contains(h.errb.String(), "run fleet init, or set FLEET_CONFIG") {
		t.Fatalf("missing config not explained: exit %d %s", code, h.errb)
	}
}

func TestExplicitLocationsMustBeWhatTheyName(t *testing.T) {
	h := newHarness(t)
	h.env["FLEET_CONFIG"] = h.src
	if code := h.run("docs"); code == 0 || !strings.Contains(h.errb.String(), "is not a fleet config") {
		t.Fatalf("a source checkout was accepted as FLEET_CONFIG: exit %d %s", code, h.errb)
	}
	h = newHarness(t)
	h.env["FLEET_SRC"] = h.cfg
	if code := h.run("push", "fw"); code == 0 || !strings.Contains(h.errb.String(), "is not a fleet source checkout") {
		t.Fatalf("a config was accepted as FLEET_SRC: exit %d %s", code, h.errb)
	}
}

// Members have the installed bundle but neither a config nor the source, so
// they read the bundle's inventory and cannot push.
func TestWithoutConfigFleetReadsTheInstalledBundle(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "FLEET_CONFIG")
	delete(h.env, "FLEET_SRC")
	bundleDir := filepath.Join(h.app.Home, ".local", "share", "fleet", "bundle")
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "fleet.toml"), []byte(testInventory), 0o644); err != nil {
		t.Fatal(err)
	}
	h.runner.handler = tailscaleHandler(h.runner.handler)
	if code := h.run("ls"); code != 0 || !strings.Contains(h.out.String(), "fw") {
		t.Fatalf("ls from the bundle: exit %d %s %s", code, h.out, h.errb)
	}
	if h.app.IsController() {
		t.Fatal("a member without config or source claims to be the controller")
	}
	if code := h.run("push", "fw"); code == 0 || !strings.Contains(h.errb.String(), "push runs on the controller") {
		t.Fatalf("push without a config: exit %d %s", code, h.errb)
	}
}

func TestCheckCommand(t *testing.T) {
	h := newHarness(t)
	if code := h.run("check"); code != 1 || !strings.Contains(h.out.String(), "FLEET.md is missing or stale") {
		t.Fatalf("stale guide in the located config: exit %d %s %s", code, h.out, h.errb)
	}
	if code := h.run("docs"); code != 0 {
		t.Fatalf("docs: exit %d %s", code, h.errb)
	}
	if code := h.run("check"); code != 0 || !strings.Contains(h.out.String(), "config is valid") {
		t.Fatalf("valid config: exit %d %s %s", code, h.out, h.errb)
	}
	other := t.TempDir()
	makeConfig(t, other)
	if code := h.run("check", "--json", other); code != 1 {
		t.Fatalf("explicit dir: exit %d %s", code, h.errb)
	}
	var got struct {
		Config   string
		Problems []struct{ Check, Detail string }
	}
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || got.Config != other || len(got.Problems) != 1 || got.Problems[0].Check != "guide" {
		t.Fatalf("json = %s, %v", h.out, err)
	}
	if code := h.run("check", "a", "b"); code != exitUsage {
		t.Fatalf("two dirs: exit %d", code)
	}
}
