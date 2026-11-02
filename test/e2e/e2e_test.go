//go:build e2e

// End-to-end tests drive the real fleet binary against a real SSH node in a
// container, from an isolated controller home and source checkout.
// Run with: go test -tags e2e ./test/e2e/
package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type env struct {
	t     *testing.T
	repo  string // real repository root
	src   string // isolated copy used as the controller's source checkout
	home  string // controller HOME
	fleet string // fleet binary under test
	node  string // container name
	key   string
	port  string
}

func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

func setup(t *testing.T) *env {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not available")
	}
	repo, _ := filepath.Abs("../..")
	root := t.TempDir()
	e := &env{t: t, repo: repo, src: filepath.Join(root, "src"), home: filepath.Join(root, "home"),
		fleet: filepath.Join(root, "fleet"), key: filepath.Join(root, "id_ed25519"), node: fmt.Sprintf("fleet-e2e-%d", time.Now().UnixNano())}
	if err := os.MkdirAll(e.home, 0o755); err != nil {
		t.Fatal(err)
	}

	run(t, repo, "go", "build", "-o", e.fleet, "./cmd/fleet")
	run(t, repo, "rsync", "-a", "--exclude", ".git", "--exclude", "dist", repo+"/", e.src+"/")
	run(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", e.key)

	run(t, repo, "docker", "build", "-q", "-t", "fleet-e2e-node", "-f", "test/e2e/node.Containerfile", "test/e2e")
	run(t, root, "docker", "run", "-d", "--name", e.node, "-p", "127.0.0.1::22",
		"-v", e.key+".pub:/tmp/fleet-e2e.pub:ro", "fleet-e2e-node")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", e.node).Run() })
	port := strings.TrimSpace(run(t, root, "docker", "port", e.node, "22/tcp"))
	e.port = port[strings.LastIndex(port, ":")+1:]

	inventory := fmt.Sprintf(`
[[node]]
name = "ctl"
host = "controller.invalid"
user = "nobody"
os = "darwin"
role = "controller"

[[node]]
name = "box"
host = "127.0.0.1"
port = %s
user = "agent"
os = "linux"
role = "member"
identity = %q

[[tool]]
name = "fish"
version = "fish --version"
`, e.port, e.key)
	if err := os.WriteFile(filepath.Join(e.src, "fleet.toml"), []byte(inventory), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		out, code := e.fleetRun("exec", "box", "--", "true")
		if code == 0 {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", e.node).CombinedOutput()
			t.Fatalf("test node never accepted ssh (exit %d):\n%s\nsshd log:\n%s", code, out, logs)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return e
}

// fleetRun runs the fleet binary as the controller and returns output and exit code.
func (e *env) fleetRun(args ...string) (string, int) {
	e.t.Helper()
	cmd := exec.Command(e.fleet, args...)
	cmd.Dir = e.src
	gocache, _ := exec.Command("go", "env", "GOCACHE").Output()
	gomod, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	cmd.Env = []string{
		"HOME=" + e.home, "PATH=" + os.Getenv("PATH"), "USER=e2e",
		"FLEET_SRC=" + e.src, "FLEET_SELF=ctl", "FLEET_ACTOR=e2e-test",
		"GOCACHE=" + strings.TrimSpace(string(gocache)), "GOMODCACHE=" + strings.TrimSpace(string(gomod)),
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("fleet %v: %v", args, err)
	}
	return out.String(), code
}

func (e *env) must(want int, args ...string) string {
	e.t.Helper()
	out, code := e.fleetRun(args...)
	if code != want {
		e.t.Fatalf("fleet %s: exit %d, want %d\n%s", strings.Join(args, " "), code, want, out)
	}
	return out
}

func (e *env) onBox(script string) string {
	e.t.Helper()
	return e.must(0, "exec", "box", "--", script)
}

func TestFleetEndToEnd(t *testing.T) {
	e := setup(t)

	t.Run("exec runs POSIX sh under a fish login shell", func(t *testing.T) {
		out := e.onBox(`printf '%s|%s\n' "$0" "it's $((6*7))"`)
		if out != "sh|it's 42\n" {
			t.Fatalf("out = %q", out)
		}
		_, code := e.fleetRun("exec", "box", "--", "exit 7")
		if code != 7 {
			t.Fatalf("remote exit code not propagated: %d", code)
		}
	})

	t.Run("seed local config the push must preserve", func(_ *testing.T) {
		e.onBox(`mkdir -p ~/.claude ~/.codex && printf '{"theme":"dark","permissions":{"allow":["Bash(ls)"]}}' > ~/.claude/settings.json && printf '# my own notes\n' > ~/.claude/CLAUDE.md && printf 'model = "local-model"\n' > ~/.codex/config.toml`)
	})

	t.Run("drift before first push reports pending changes", func(t *testing.T) {
		out := e.must(4, "drift", "box")
		for _, want := range []string{"create    ~/.config/fleet/FLEET.md", "merge     ~/.claude/settings.json", "~/.claude/skills/demo/"} {
			if !strings.Contains(out, want) {
				t.Errorf("drift output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(e.onBox(`ls ~/.local/share/fleet 2>/dev/null || echo none`), "bundle") {
			t.Fatal("drift installed a bundle")
		}
	})

	t.Run("push installs fleet and the shared layer", func(t *testing.T) {
		e.must(0, "push", "box")
		claudeMD := e.onBox(`cat ~/.claude/CLAUDE.md`)
		if !strings.HasPrefix(claudeMD, "# my own notes\n\n<!-- fleet:begin") || !strings.Contains(claudeMD, "## Fleet") {
			t.Fatalf("CLAUDE.md = %q", claudeMD)
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(e.onBox(`cat ~/.claude/settings.json`)), &settings); err != nil {
			t.Fatal(err)
		}
		allow := fmt.Sprint(settings["permissions"].(map[string]any)["allow"])
		if settings["theme"] != "dark" || !strings.Contains(allow, "Bash(ls)") {
			t.Fatalf("settings = %v", settings)
		}
		codex := e.onBox(`cat ~/.codex/config.toml`)
		if !strings.Contains(codex, `model = "local-model"`) || !strings.Contains(codex, `model_reasoning_effort = "high"`) {
			t.Fatalf("codex config = %s", codex)
		}
		skills := e.onBox(`ls ~/.claude/skills ~/.codex/skills`)
		if !strings.Contains(skills, "claude-example") || !strings.Contains(skills, "codex-example") {
			t.Fatalf("skills = %s", skills)
		}
		if strings.Count(skills, "claude-example") != 1 {
			t.Fatalf("claude-only skill leaked into codex: %s", skills)
		}
		if v := e.onBox(`fleet version`); strings.TrimSpace(v) != strings.TrimSpace(e.onBox(`cat ~/.local/share/fleet/bundle/VERSION`)) {
			t.Fatalf("installed fleet version %q does not match bundle", v)
		}
	})

	t.Run("node is converged after push", func(t *testing.T) {
		e.must(0, "drift", "box")
		out := e.must(0, "doctor", "box")
		if !strings.Contains(out, "box") || strings.Contains(out, "stale") || !strings.Contains(out, "fish, version") {
			t.Fatalf("doctor = %s", out)
		}
	})

	skill := "~/.claude/skills/demo/SKILL.md"
	repoSkill := filepath.Join(e.src, "home/skills/shared/demo/SKILL.md")

	t.Run("local edit plus repo change is a conflict and the edit survives", func(t *testing.T) {
		e.onBox(`printf 'edited on the node\n' > ` + skill)
		if err := os.WriteFile(repoSkill, []byte("changed in the repo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := e.must(3, "push", "box")
		if !strings.Contains(out, "conflict  "+skill) {
			t.Fatalf("push output:\n%s", out)
		}
		if got := e.onBox(`cat ` + skill); got != "edited on the node\n" {
			t.Fatalf("local edit overwritten: %q", got)
		}
	})

	t.Run("adopt pulls the node's edit into the repo and push converges", func(t *testing.T) {
		e.must(0, "adopt", "box", skill)
		if got, _ := os.ReadFile(repoSkill); string(got) != "edited on the node\n" {
			t.Fatalf("repo file = %q", got)
		}
		e.must(0, "push", "box")
		e.must(0, "drift", "box")
	})

	t.Run("removing a skill from the repo prunes it from the node", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(e.src, "home/skills/shared/removable")); err != nil {
			t.Fatal(err)
		}
		out := e.must(0, "push", "box")
		if !strings.Contains(out, "delete    ~/.claude/skills/removable/SKILL.md") {
			t.Fatalf("push output:\n%s", out)
		}
		if got := e.onBox(`ls ~/.claude/skills/removable 2>/dev/null | wc -l`); strings.TrimSpace(got) != "0" {
			t.Fatalf("pruned skill still has files: %s", got)
		}
	})

	t.Run("every remote action is audited on the controller", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(e.home, ".local/state/fleet/audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		log := string(data)
		for _, want := range []string{`"command":"push"`, `"command":"drift"`, `"command":"adopt"`, `"actor":"e2e-test"`, `"target":"box"`} {
			if !strings.Contains(log, want) {
				t.Errorf("audit log missing %s", want)
			}
		}
		if hosts, _ := os.ReadFile(filepath.Join(e.home, ".local/state/fleet/known_hosts")); !strings.Contains(string(hosts), "127.0.0.1]:"+e.port) {
			t.Error("host key not pinned in fleet's own known_hosts")
		}
	})
}
