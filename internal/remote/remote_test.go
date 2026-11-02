package remote

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/inventory"
)

var node = inventory.Node{Name: "fw", Host: "fw-host", User: "operator", Port: 2222, OS: inventory.Linux, Role: inventory.Member}

var opts = SSHOptions{KnownHostsFile: "/state/known_hosts", ConnectTimeout: 8 * time.Second}

// tricky exercises every character class that breaks naive quoting in fish or POSIX.
const tricky = `printf '%s|%s\n' "a\\b" 'it'"'"'s'; echo "$((2+3)) $HOME" > /dev/null; echo done`

// runVia runs ShellCommand(script) through the named shell the way sshd would
// hand it to the user's login shell.
func runVia(t *testing.T, shell, script string) string {
	t.Helper()
	if _, err := exec.LookPath(shell); err != nil {
		t.Skipf("%s not installed", shell)
	}
	out, err := exec.Command(shell, "-c", ShellCommand(script)).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", shell, err, out)
	}
	return string(out)
}

func TestShellCommandIsShellNeutral(t *testing.T) {
	want := "a\\b|it's\ndone\n"
	for _, shell := range []string{"sh", "bash", "zsh", "dash", "fish"} {
		t.Run(shell, func(t *testing.T) {
			if got := runVia(t, shell, tricky); got != want {
				t.Fatalf("%s output = %q, want %q", shell, got, want)
			}
		})
	}
}

func TestShellCommandLeavesStdinToScript(t *testing.T) {
	cmd := exec.Command("sh", "-c", ShellCommand("tr a-z A-Z"))
	cmd.Stdin = strings.NewReader("piped")
	out, err := cmd.Output()
	if err != nil || string(out) != "PIPED" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

// Regression: prepending ~/.local/bin made a stale codex there shadow the
// mise-managed one on the Framework. The node's own PATH must keep precedence.
func TestShellCommandAppendsUserBinsWithoutShadowing(t *testing.T) {
	cmd := exec.Command("sh", "-c", ShellCommand(`echo "$PATH"`))
	cmd.Env = []string{"PATH=/first:/usr/bin:/bin", "HOME=/h"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "/first:/usr/bin:/bin:/h/.local/bin:/opt/homebrew/bin:/usr/local/bin" {
		t.Fatalf("PATH = %q", got)
	}
}

// Regression: without a guard, a node lacking `base64 -d` evaluated an empty
// string and reported success, so push and drift silently did nothing.
func TestShellCommandFailsLoudWhenDecodeFails(t *testing.T) {
	fake := t.TempDir()
	if err := os.WriteFile(filepath.Join(fake, "base64"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", ShellCommand("echo should-not-run"))
	cmd.Env = []string{"PATH=" + fake + ":/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != DecodeFailure || !strings.Contains(string(out), "cannot decode") || strings.Contains(string(out), "should-not-run") {
		t.Fatalf("err = %v, out = %q", err, out)
	}
}

func TestShellCommandHasNoQuoteBreakers(t *testing.T) {
	line := ShellCommand(tricky)
	inner := strings.TrimSuffix(strings.TrimPrefix(line, "sh -c '"), "'")
	if strings.ContainsAny(inner, `'\`) {
		t.Fatalf("single-quoted payload contains a quote or backslash: %s", inner)
	}
}

func TestExecArgsHardening(t *testing.T) {
	args := opts.ExecArgs(node, "uptime")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"BatchMode=yes", "ForwardAgent=no", "ForwardX11=no", "ClearAllForwardings=yes",
		"StrictHostKeyChecking=accept-new", "UserKnownHostsFile=/state/known_hosts",
		"ConnectTimeout=8", "-p 2222", "-T",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, banned := range []string{"StrictHostKeyChecking=no", "ForwardAgent=yes", " -A ", "UserKnownHostsFile=/dev/null"} {
		if strings.Contains(" "+joined+" ", banned) {
			t.Errorf("unsafe flag %q present", banned)
		}
	}
	if strings.Join(args[len(args)-5:len(args)-1], " ") != "-l operator -- fw-host" {
		t.Fatalf("destination = %q; user must go through -l and the host after --", args[len(args)-5:len(args)-1])
	}
	if args[len(args)-1] != ShellCommand("uptime") {
		t.Fatal("remote command is not the shell-neutral wrapper")
	}
}

func TestIdentityAndProxy(t *testing.T) {
	n := node
	n.Identity = "/keys/id"
	o := opts
	o.ProxyCommand = "tailscale nc %h %p"
	args := o.ExecArgs(n, "true")
	if !slices.Contains(args, "/keys/id") || !slices.Contains(args, "IdentitiesOnly=yes") {
		t.Fatalf("identity flags missing: %v", args)
	}
	if !slices.Contains(args, "ProxyCommand=tailscale nc %h %p") {
		t.Fatalf("proxy missing: %v", args)
	}
}

func TestInteractiveArgs(t *testing.T) {
	args := opts.InteractiveArgs(node, "htop")
	if args[len(args)-1] != ShellCommand("htop") || strings.Join(args[len(args)-4:len(args)-1], " ") != "operator -- fw-host" || !slices.Contains(args, "-t") {
		t.Fatalf("interactive args = %v", args)
	}
	if !slices.Contains(args, "ServerAliveInterval=15") || !slices.Contains(args, "ServerAliveCountMax=4") {
		t.Fatalf("no keepalive: %v", args)
	}
}

func TestPersistentSessionAttachesFleetTmux(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "args")
	tmux := "#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done > " + log + "\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(tmux), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", ShellCommand(PersistentSession))
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if want := "new-session\n-A\n-s\nfleet\n;\nset-option\nmouse\non\n"; string(got) != want {
		t.Fatalf("tmux got %q, want %q", got, want)
	}
}

func TestTunnelArgs(t *testing.T) {
	args := opts.TunnelArgs(node, 41000, 4500, "serve")
	if !slices.Contains(args, "127.0.0.1:41000:127.0.0.1:4500") || !slices.Contains(args, "ExitOnForwardFailure=yes") {
		t.Fatalf("tunnel args = %v", args)
	}
	if slices.Contains(args, "ClearAllForwardings=yes") {
		t.Fatal("tunnel must not clear its own forward")
	}
}

func TestInContainer(t *testing.T) {
	got := InContainer("builder", "id -u")
	if !strings.HasPrefix(got, "exec podman exec -i fleet-agent-builder sh -c '") {
		t.Fatalf("InContainer = %s", got)
	}
}
