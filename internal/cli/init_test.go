package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vickingur/fleet-orchestrator/internal/configcheck"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

// initRunner answers uname -n with host and records git invocations.
func initRunner(h *harness, host string, git func(args []string) (string, int, error)) *[][]string {
	var calls [][]string
	h.runner.handler = func(c execx.Cmd) (string, int, error) {
		switch c.Name {
		case "uname":
			return host + "\n", 0, nil
		case "git":
			calls = append(calls, c.Args)
			return git(c.Args)
		}
		return "", 1, nil
	}
	return &calls
}

func TestInitCreatesAValidConfigAtTheDefaultLocation(t *testing.T) {
	h := newHarness(t)
	delete(h.env, "FLEET_CONFIG")
	h.env["USER"] = "operator"
	calls := initRunner(h, "Laptop-Example.local", func([]string) (string, int, error) { return "", 0, nil })
	if code := h.run("init"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errb)
	}
	dir := h.app.DefaultConfigDir()
	if problems := configcheck.Run(dir); len(problems) != 0 {
		t.Fatalf("the starter fails its own check: %v", problems)
	}
	inv, err := os.ReadFile(filepath.Join(dir, "fleet.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name = "laptop-example"`, `host = "laptop-example"`, `user = "operator"`, `os = "` + runtime.GOOS + `"`, `role = "controller"`} {
		if !strings.Contains(string(inv), want) {
			t.Errorf("fleet.toml lacks %s:\n%s", want, inv)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "home/skills/shared/fleet/SKILL.md")); err != nil {
		t.Error("the starter has no fleet skill")
	}
	if len(*calls) != 1 || strings.Join((*calls)[0], " ") != "init --quiet "+dir {
		t.Fatalf("git calls = %v", *calls)
	}
	if code := h.run("check"); code != 0 {
		t.Fatalf("fleet check does not find the new config: exit %d %s %s", code, h.out, h.errb)
	}
}

func TestInitNamesTheControllerAndRefusesToOverwrite(t *testing.T) {
	h := newHarness(t)
	h.env["USER"] = "operator"
	initRunner(h, "box", func([]string) (string, int, error) { return "", 0, nil })
	dir := filepath.Join(t.TempDir(), "cfg")
	if code := h.run("init", "--dir", dir, "--name", "Not Valid"); code == 0 || !strings.Contains(h.errb.String(), "pass --name") {
		t.Fatalf("invalid name: exit %d %s", code, h.errb)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if code := h.run("init", "--dir", dir, "--name", "desk"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errb)
	}
	if inv, _ := os.ReadFile(filepath.Join(dir, "fleet.toml")); !strings.Contains(string(inv), `name = "desk"`) || !strings.Contains(string(inv), `host = "box"`) {
		t.Fatalf("fleet.toml = %s", inv)
	}
	if code := h.run("init", "--dir", dir); code == 0 || !strings.Contains(h.errb.String(), "not empty") {
		t.Fatalf("existing config overwritten: exit %d %s", code, h.errb)
	}
}

func TestInitClonesAnExistingConfig(t *testing.T) {
	h := newHarness(t)
	dir := filepath.Join(t.TempDir(), "cfg")
	cloned := false
	calls := initRunner(h, "box", func(args []string) (string, int, error) {
		if args[0] == "clone" && cloned {
			makeConfig(t, args[len(args)-1])
		}
		return "", 0, nil
	})
	if code := h.run("init", "--dir", dir, "git@example.invalid:me/not-a-config.git"); code == 0 || !strings.Contains(h.errb.String(), "not a fleet config") {
		t.Fatalf("a clone without a config was accepted: exit %d %s", code, h.errb)
	}
	cloned = true
	dir = filepath.Join(t.TempDir(), "cfg")
	if code := h.run("init", "--dir", dir, "git@example.invalid:me/fleet-config.git"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errb)
	}
	if got := strings.Join((*calls)[1], " "); got != "clone --quiet -- git@example.invalid:me/fleet-config.git "+dir {
		t.Fatalf("git call = %s", got)
	}
	if code := h.run("init", "--name", "x", "git@example.invalid:me/fleet-config.git"); code != exitUsage {
		t.Fatalf("--name with a clone: exit %d", code)
	}
}
