package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/bundle"
	"github.com/vickingur/fleet-orchestrator/internal/docs"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/starter"
)

// --- init ------------------------------------------------------------------

func (a *App) cmdInit(ctx context.Context, args []string) error {
	fs := newFlags("init")
	dirFlag := fs.String("dir", "", "where to put the config (default: FLEET_CONFIG, else ~/.local/share/fleet/config)")
	name := fs.String("name", "", "this machine's node name in a new inventory (default: its host name)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usagef("usage: fleet init [--dir <path>] [<git-url>]")
	}
	if fs.NArg() == 1 && *name != "" {
		return usagef("--name names this machine in a new inventory; a cloned config has its own")
	}
	dir := *dirFlag
	if dir == "" {
		dir = a.Getenv("FLEET_CONFIG")
	}
	if dir == "" {
		dir = a.DefaultConfigDir()
	}
	switch entries, err := os.ReadDir(dir); {
	case err == nil && len(entries) > 0:
		return fmt.Errorf("%s already exists and is not empty", dir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	if fs.NArg() == 1 {
		return a.cloneConfig(ctx, fs.Arg(0), dir)
	}
	return a.scaffoldConfig(ctx, dir, *name)
}

// cloneConfig sets up a further controller from an existing config.
func (a *App) cloneConfig(ctx context.Context, url, dir string) error {
	if err := a.git(ctx, "clone", "--quiet", "--", url, dir); err != nil {
		return err
	}
	if !bundle.IsConfig(dir) {
		return fmt.Errorf("cloned %s into %s, but it is not a fleet config: it needs fleet.toml and home/manifest.toml", url, dir)
	}
	a.printf("cloned the fleet config into %s\n", dir)
	a.println("next: fleet check, then fleet drift to see what a push would change")
	return nil
}

// scaffoldConfig creates a config whose inventory holds this machine as the
// controller and whose shared layer is the starter.
func (a *App) scaffoldConfig(ctx context.Context, dir, name string) error {
	host, err := a.hostLabel(ctx)
	if err != nil {
		return err
	}
	if name == "" {
		name = host
	}
	user := a.Getenv("USER")
	if user == "" {
		return errors.New("USER is not set, so this machine's SSH user is unknown")
	}
	inv := fmt.Sprintf(`# The fleet: every machine, how to reach it, and what each must run.
# FLEET.md is generated from this file (fleet docs); edit here, not there.

[[node]]
name = %q
host = %q
user = %q
os = %q
role = "controller"

[[tool]]
name = "claude"
version = "claude --version"

[[tool]]
name = "codex"
version = "codex --version"
`, name, host, user, runtime.GOOS)
	parsed, err := inventory.Parse([]byte(inv))
	if err != nil {
		return fmt.Errorf("this machine does not make a valid inventory (%w); pass --name", err)
	}
	files := map[string][]byte{"fleet.toml": []byte(inv), "FLEET.md": docs.Render(parsed)}
	err = fs.WalkDir(starter.Home, "home", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := starter.Home.ReadFile(p)
		files[p] = data
		return err
	})
	if err != nil {
		return err
	}
	for rel, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
	}
	if err := a.git(ctx, "init", "--quiet", dir); err != nil {
		return err
	}
	a.printf("created a fleet config in %s with %s as the controller\n", dir, name)
	a.println("next: add your other machines to fleet.toml, run fleet docs and fleet check,")
	a.println("commit, and push only to a private remote: the inventory names your machines")
	return nil
}

// hostLabel is the first label of this machine's host name, which is also its
// MagicDNS name on a tailnet.
func (a *App) hostLabel(ctx context.Context) (string, error) {
	var out, stderr bytes.Buffer
	res, err := a.Runner.Run(ctx, execx.Cmd{Name: "uname", Args: []string{"-n"}, Stdout: &out, Stderr: &stderr, Timeout: 10 * time.Second})
	if err != nil {
		return "", fmt.Errorf("uname -n: %w", err)
	}
	// macOS reports "Name-MacBook.local"; MagicDNS names are lowercase.
	label, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(out.String())), ".")
	if res.ExitCode != 0 || label == "" {
		return "", fmt.Errorf("uname -n failed: %s", strings.TrimSpace(stderr.String()))
	}
	return label, nil
}

func (a *App) git(ctx context.Context, args ...string) error {
	var stderr bytes.Buffer
	res, err := a.Runner.Run(ctx, execx.Cmd{Name: "git", Args: args, Stdout: a.Stderr, Stderr: &stderr, Timeout: 5 * time.Minute})
	if err != nil {
		return fmt.Errorf("git %s: %w", args[0], err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	return nil
}
