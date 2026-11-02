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
	cfg   string // controller's fleet config, at the default location under HOME
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
		cfg:   filepath.Join(root, "home", ".local", "share", "fleet", "config"),
		fleet: filepath.Join(root, "fleet"), key: filepath.Join(root, "id_ed25519"), node: fmt.Sprintf("fleet-e2e-%d", time.Now().UnixNano())}
	if err := os.MkdirAll(e.home, 0o755); err != nil {
		t.Fatal(err)
	}

	// Deliberately deny discovery even if the host happens to have Tailscale.
	bin := filepath.Join(e.home, "test-bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	guard := "#!/bin/sh\nprintf called >> \"$HOME/tailscale-calls\"\necho 'Tailscale deliberately unavailable' >&2\nexit 127\n"
	if err := os.WriteFile(filepath.Join(bin, "tailscale"), []byte(guard), 0o755); err != nil {
		t.Fatal(err)
	}
	// HOME does not isolate OpenSSH's per-user configuration. Keep this
	// disposable test independent of developer SSH aliases, proxies and keys.
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := "#!/bin/sh\nexec '" + strings.ReplaceAll(ssh, "'", "'\\''") + "' -F /dev/null \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}

	run(t, repo, "go", "build", "-o", e.fleet, "./cmd/fleet")
	run(t, repo, "rsync", "-a", "--exclude", ".git", "--exclude", "dist", repo+"/", e.src+"/")
	if err := os.MkdirAll(e.cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "rsync", "-a", repo+"/examples/config/", e.cfg+"/")
	run(t, root, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", e.key)

	run(t, repo, "docker", "build", "-q", "-t", "fleet-e2e-node", "-f", "test/e2e/node.Containerfile", "test/e2e")
	run(t, root, "docker", "run", "-d", "--name", e.node, "-p", "127.0.0.1::22",
		"-v", e.key+".pub:/tmp/fleet-e2e.pub:ro", "fleet-e2e-node")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", e.node).Run() })
	port := strings.TrimSpace(run(t, root, "docker", "port", e.node, "22/tcp"))
	e.port = port[strings.LastIndex(port, ":")+1:]

	example, err := os.ReadFile(filepath.Join(repo, "examples/ssh/fleet.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	inventory := strings.NewReplacer(
		`host = "worker.example.invalid"`, `host = "127.0.0.1"`,
		`port = 2222`, "port = "+e.port,
		`identity = "/path/to/private/test-key"`, fmt.Sprintf("identity = %q", e.key),
	).Replace(string(example))
	if err := os.WriteFile(filepath.Join(e.cfg, "fleet.toml"), []byte(inventory), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for {
		out, code := e.fleetRun("exec", "worker", "--", "true")
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

// fleetRun runs the fleet binary as the controller, from its source checkout
// with the config at the default location, and returns output and exit code.
func (e *env) fleetRun(args ...string) (string, int) {
	e.t.Helper()
	cmd := exec.Command(e.fleet, args...)
	cmd.Dir = e.src
	gocache, _ := exec.Command("go", "env", "GOCACHE").Output()
	gomod, _ := exec.Command("go", "env", "GOMODCACHE").Output()
	cmd.Env = []string{
		"HOME=" + e.home, "PATH=" + filepath.Join(e.home, "test-bin") + string(os.PathListSeparator) + os.Getenv("PATH"), "USER=e2e",
		"FLEET_SRC=" + e.src, "FLEET_SELF=controller", "FLEET_ACTOR=e2e-test",
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

func (e *env) onWorker(script string) string {
	e.t.Helper()
	return e.must(0, "exec", "worker", "--", script)
}

func TestFleetEndToEnd(t *testing.T) {
	e := setup(t)

	t.Run("worker has no Tailscale installed", func(t *testing.T) {
		out := e.onWorker("if command -v tailscale >/dev/null 2>&1; then printf present; else printf absent; fi")
		if out != "absent" {
			t.Fatalf("unexpected Tailscale on worker: %q", out)
		}
	})

	t.Run("exec runs POSIX sh under a fish login shell", func(t *testing.T) {
		out := e.onWorker(`printf '%s|%s\n' "$0" "it's $((6*7))"`)
		if out != "sh|it's 42\n" {
			t.Fatalf("out = %q", out)
		}
		_, code := e.fleetRun("exec", "worker", "--", "exit 7")
		if code != 7 {
			t.Fatalf("remote exit code not propagated: %d", code)
		}
	})

	t.Run("seed local config the push must preserve", func(_ *testing.T) {
		e.onWorker(`mkdir -p ~/.claude ~/.codex && printf '{"theme":"dark","permissions":{"allow":["Bash(ls)"]}}' > ~/.claude/settings.json && printf '# my own notes\n' > ~/.claude/CLAUDE.md && printf 'model = "local-model"\n' > ~/.codex/config.toml`)
	})

	t.Run("drift before first push reports pending changes", func(t *testing.T) {
		out := e.must(4, "drift", "worker")
		for _, want := range []string{"create    ~/.config/fleet/FLEET.md", "merge     ~/.codex/config.toml", "~/.claude/skills/demo/"} {
			if !strings.Contains(out, want) {
				t.Errorf("drift output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(e.onWorker(`ls ~/.local/share/fleet 2>/dev/null || echo none`), "bundle") {
			t.Fatal("drift installed a bundle")
		}
	})

	t.Run("push installs fleet and the shared layer", func(t *testing.T) {
		e.must(0, "push", "worker")
		claudeMD := e.onWorker(`cat ~/.claude/CLAUDE.md`)
		if !strings.HasPrefix(claudeMD, "# my own notes\n\n<!-- fleet:begin") || !strings.Contains(claudeMD, "## Fleet") {
			t.Fatalf("CLAUDE.md = %q", claudeMD)
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(e.onWorker(`cat ~/.claude/settings.json`)), &settings); err != nil {
			t.Fatal(err)
		}
		allow := fmt.Sprint(settings["permissions"].(map[string]any)["allow"])
		if settings["theme"] != "dark" || !strings.Contains(allow, "Bash(ls)") {
			t.Fatalf("settings = %v", settings)
		}
		codex := e.onWorker(`cat ~/.codex/config.toml`)
		if !strings.Contains(codex, `model = "local-model"`) || !strings.Contains(codex, `model_reasoning_effort = "high"`) {
			t.Fatalf("codex config = %s", codex)
		}
		skills := e.onWorker(`ls ~/.claude/skills ~/.codex/skills`)
		if !strings.Contains(skills, "claude-example") || !strings.Contains(skills, "codex-example") {
			t.Fatalf("skills = %s", skills)
		}
		if strings.Count(skills, "claude-example") != 1 {
			t.Fatalf("claude-only skill leaked into codex: %s", skills)
		}
		if v := e.onWorker(`fleet version`); strings.TrimSpace(v) != strings.TrimSpace(e.onWorker(`cat ~/.local/share/fleet/bundle/VERSION`)) {
			t.Fatalf("installed fleet version %q does not match bundle", v)
		}
	})

	t.Run("node is converged after push", func(t *testing.T) {
		e.must(0, "drift", "worker")
		out := e.must(0, "doctor", "worker")
		if !strings.Contains(out, "worker") || strings.Contains(out, "stale") || !strings.Contains(out, "fish, version") {
			t.Fatalf("doctor = %s", out)
		}
	})

	skill := "~/.claude/skills/demo/SKILL.md"
	repoSkill := filepath.Join(e.cfg, "home/skills/shared/demo/SKILL.md")

	t.Run("local edit plus repo change is a conflict and the edit survives", func(t *testing.T) {
		e.onWorker(`printf 'edited on the node\n' > ` + skill)
		if err := os.WriteFile(repoSkill, []byte("changed in the repo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out := e.must(3, "push", "worker")
		if !strings.Contains(out, "conflict  "+skill) {
			t.Fatalf("push output:\n%s", out)
		}
		if got := e.onWorker(`cat ` + skill); got != "edited on the node\n" {
			t.Fatalf("local edit overwritten: %q", got)
		}
	})

	t.Run("adopt pulls the node's edit into the repo and push converges", func(t *testing.T) {
		e.must(0, "adopt", "worker", skill)
		if got, _ := os.ReadFile(repoSkill); string(got) != "edited on the node\n" {
			t.Fatalf("repo file = %q", got)
		}
		e.must(0, "push", "worker")
		e.must(0, "drift", "worker")
	})

	t.Run("removing a skill from the repo prunes it from the node", func(t *testing.T) {
		if err := os.RemoveAll(filepath.Join(e.cfg, "home/skills/shared/removable")); err != nil {
			t.Fatal(err)
		}
		out := e.must(0, "push", "worker")
		if !strings.Contains(out, "delete    ~/.claude/skills/removable/SKILL.md") {
			t.Fatalf("push output:\n%s", out)
		}
		if got := e.onWorker(`ls ~/.claude/skills/removable 2>/dev/null | wc -l`); strings.TrimSpace(got) != "0" {
			t.Fatalf("pruned skill still has files: %s", got)
		}
	})

	t.Run("every remote action is audited on the controller", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join(e.home, ".local/state/fleet/audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		log := string(data)
		for _, want := range []string{`"command":"push"`, `"command":"drift"`, `"command":"adopt"`, `"actor":"e2e-test"`, `"target":"worker"`} {
			if !strings.Contains(log, want) {
				t.Errorf("audit log missing %s", want)
			}
		}
		// ssh-keygen resolves both plaintext and hashed known_hosts entries.
		hosts := filepath.Join(e.home, ".local/state/fleet/known_hosts")
		match := run(t, e.home, "ssh-keygen", "-F", "[127.0.0.1]:"+e.port, "-f", hosts)
		if strings.TrimSpace(match) == "" {
			t.Error("host key not pinned in fleet's own known_hosts")
		}
	})

	t.Run("ordinary SSH operations never attempted Tailscale discovery", func(t *testing.T) {
		if _, err := os.Stat(filepath.Join(e.home, "tailscale-calls")); !os.IsNotExist(err) {
			t.Fatalf("core SSH operations invoked Tailscale, or guard state could not be checked: %v", err)
		}
	})

	t.Run("ls tolerates unavailable Tailscale without claiming SSH failure", func(t *testing.T) {
		var listing struct {
			Nodes        []struct{ Name, State string }
			TailnetError string `json:"tailnet_error"`
		}
		if err := json.Unmarshal([]byte(e.must(0, "ls", "--json")), &listing); err != nil {
			t.Fatal(err)
		}
		if len(listing.Nodes) != 2 || listing.TailnetError == "" {
			t.Fatalf("listing = %+v", listing)
		}
		for _, node := range listing.Nodes {
			if node.State != "unknown" {
				t.Errorf("%s: state %q, want unknown", node.Name, node.State)
			}
		}
	})

}
