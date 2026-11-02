package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestBinaryWiring builds the real binary and checks exit codes and output
// streams, covering what the composition root alone is responsible for.
func TestBinaryWiring(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fleet")
	if out, err := exec.Command("go", "build", "-ldflags", "-X main.version=wired", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil || string(out) != "wired\n" {
		t.Fatalf("version = %q, %v", out, err)
	}
	cmd := exec.Command(bin, "nope")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	stderr, _ := cmd.CombinedOutput()
	if cmd.ProcessState.ExitCode() != 2 || !strings.Contains(string(stderr), `unknown command "nope"`) {
		t.Fatalf("exit %d, output %q", cmd.ProcessState.ExitCode(), stderr)
	}
}
