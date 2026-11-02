// Package tui is fleet's interactive terminal view. It reads and acts only
// through the fleet commands' --json output, so it can never disagree with
// the command line agents use.
package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/vickingur/fleet-orchestrator/internal/audit"
	"github.com/vickingur/fleet-orchestrator/internal/cli"
)

// Node is one inventory node with its tailnet state (fleet ls --json).
type Node struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	OS      string `json:"os"`
	Role    string `json:"role"`
	State   string `json:"state"`
	Address string `json:"address"`
	Runtime string `json:"runtime"`
}

// Fleet is the result of fleet ls --json.
type Fleet struct {
	Nodes        []Node `json:"nodes"`
	TailnetError string `json:"tailnet_error"`
}

// Health is one row of fleet doctor --json.
type Health struct {
	Target   string            `json:"target"`
	Reach    string            `json:"reach"`
	Platform string            `json:"platform"`
	Fleet    string            `json:"fleet"`
	Current  bool              `json:"fleet_current"`
	Tools    map[string]string `json:"tools"`
	Error    string            `json:"error"`
}

// Container is one agent container (fleet containers --json).
type Container struct {
	Node   string `json:"node"`
	Agent  string `json:"Agent"`
	State  string `json:"State"`
	Status string `json:"Status"`
	Image  string `json:"Image"`
	Error  string `json:"error"`
}

// Change is one planned file operation.
type Change struct {
	Target string `json:"target"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// Ensure is one ensure rule's state.
type Ensure struct {
	Name      string `json:"name"`
	Satisfied bool   `json:"satisfied"`
	Fixed     bool   `json:"fixed"`
	Error     string `json:"error"`
}

// Plan is a node's apply result (embedded in push and drift output).
type Plan struct {
	Pending   bool           `json:"pending"`
	Counts    map[string]int `json:"counts"`
	Changes   []Change       `json:"changes"`
	Ensures   []Ensure       `json:"ensures"`
	Conflicts []string       `json:"conflicts"`
	Failed    []string       `json:"failed"`
}

// Sync is one node's push or drift outcome.
type Sync struct {
	Node     string `json:"node"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
	Plan     *Plan  `json:"-"`
}

// RunResult is one target's exec result.
type RunResult struct {
	Target   string `json:"target"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
	Millis   int64  `json:"ms"`
}

// Backend is everything the TUI does to the fleet.
type Backend interface {
	Fleet(ctx context.Context) (Fleet, error)
	Health(ctx context.Context, node string) (Health, error)
	Containers(ctx context.Context) ([]Container, error)
	Drift(ctx context.Context, nodes []string) ([]Sync, error)
	// Push installs the shared layer; force takes the shared version over
	// files edited on the node (they are backed up there first).
	Push(ctx context.Context, node string, force bool) (Sync, error)
	// Adopt copies the node's version of managed files into the fleet config.
	Adopt(ctx context.Context, node string, paths []string) (string, error)
	Exec(ctx context.Context, target, command string) (RunResult, error)
	Spawn(ctx context.Context, node, agent string, tailnet bool) (string, error)
	Remove(ctx context.Context, node, agent string) (string, error)
	Activity(limit int) ([]audit.Record, error)
	Guide() (string, error)
	IsController() bool
	// Shell returns the command that opens an interactive session on target.
	Shell(target string) (*exec.Cmd, error)
	// Delegate returns an interactive agent session ("claude" or "codex")
	// that opens with prompt, in the fleet config when this machine has one.
	Delegate(agent, prompt string) (*exec.Cmd, error)
	// Preference reads and SetPreference stores a small per-machine TUI
	// setting, such as the agent last delegated to.
	Preference(key string) string
	SetPreference(key, value string) error
}

// CLI is the Backend over the real fleet commands.
type CLI struct{ App *cli.App }

func (c CLI) json(ctx context.Context, into any, args ...string) error {
	stdout, stderr, _ := c.App.Capture(ctx, args...)
	if err := json.Unmarshal(stdout, into); err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		return fmt.Errorf("fleet %s: %s", args[0], msg)
	}
	return nil
}

// Fleet lists nodes with tailnet state.
func (c CLI) Fleet(ctx context.Context) (Fleet, error) {
	var f Fleet
	return f, c.json(ctx, &f, "ls", "--json")
}

// Health checks one node.
func (c CLI) Health(ctx context.Context, node string) (Health, error) {
	var rows []Health
	if err := c.json(ctx, &rows, "doctor", "--json", node); err != nil {
		return Health{}, err
	}
	if len(rows) != 1 {
		return Health{}, fmt.Errorf("doctor returned %d rows for %s", len(rows), node)
	}
	return rows[0], nil
}

// Containers lists agent containers on every runtime host.
func (c CLI) Containers(ctx context.Context) ([]Container, error) {
	var cs []Container
	return cs, c.json(ctx, &cs, "containers", "--json")
}

func parsePlans(results []Sync) []Sync {
	for i := range results {
		var p Plan
		if json.Unmarshal([]byte(results[i].Output), &p) == nil {
			results[i].Plan = &p
		}
	}
	return results
}

// Drift previews a push to nodes in one run, so binaries build once.
func (c CLI) Drift(ctx context.Context, nodes []string) ([]Sync, error) {
	var rs []Sync
	if err := c.json(ctx, &rs, "drift", "--json", strings.Join(nodes, ",")); err != nil {
		return nil, err
	}
	return parsePlans(rs), nil
}

// Push installs the shared layer on one node.
func (c CLI) Push(ctx context.Context, node string, force bool) (Sync, error) {
	args := []string{"push", "--json", node}
	if force {
		args = []string{"push", "--json", "--force", node}
	}
	var rs []Sync
	if err := c.json(ctx, &rs, args...); err != nil {
		return Sync{}, err
	}
	if len(rs) != 1 {
		return Sync{}, fmt.Errorf("push returned %d results for %s", len(rs), node)
	}
	return parsePlans(rs)[0], nil
}

// Adopt pulls the node's copies of paths into the repo.
func (c CLI) Adopt(ctx context.Context, node string, paths []string) (string, error) {
	return c.text(ctx, append([]string{"adopt", node}, paths...)...)
}

// Exec runs a shell command on one target.
func (c CLI) Exec(ctx context.Context, target, command string) (RunResult, error) {
	var rs []RunResult
	if err := c.json(ctx, &rs, "exec", "--json", target, "--", command); err != nil {
		return RunResult{}, err
	}
	if len(rs) != 1 {
		return RunResult{}, fmt.Errorf("exec returned %d results", len(rs))
	}
	return rs[0], nil
}

func (c CLI) text(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, code := c.App.Capture(ctx, args...)
	out := strings.TrimSpace(string(stdout) + "\n" + string(stderr))
	if code != 0 {
		return out, fmt.Errorf("%s", out)
	}
	return out, nil
}

// Spawn starts an agent container.
func (c CLI) Spawn(ctx context.Context, node, agent string, tailnet bool) (string, error) {
	return c.text(ctx, "spawn", fmt.Sprintf("--tailnet=%t", tailnet), node, agent)
}

// Remove removes an agent container and keeps its volumes, so a respawn
// with the same name brings its homes and login back.
func (c CLI) Remove(ctx context.Context, node, agent string) (string, error) {
	return c.text(ctx, "rm", node, agent)
}

// Activity returns the newest finished actions from this machine's audit log.
func (c CLI) Activity(limit int) ([]audit.Record, error) {
	data, err := os.ReadFile(c.App.AuditPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var r audit.Record
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Phase == "start" {
			continue
		}
		out = append(out, r)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, sc.Err()
}

// Guide returns FLEET.md.
func (c CLI) Guide() (string, error) {
	data, err := os.ReadFile(c.App.GuidePath())
	return string(data), err
}

// IsController reports whether push and drift are available here.
func (c CLI) IsController() bool { return c.App.IsController() }

// Delegate opens claude or codex interactively with prompt as the first
// message. The agent runs in the fleet config on the controller, so it can change
// the shared layer, and from the home directory elsewhere.
func (c CLI) Delegate(agent, prompt string) (*exec.Cmd, error) {
	if _, ok := agentNames[agent]; !ok {
		return nil, fmt.Errorf("unknown agent %q", agent)
	}
	bin, err := exec.LookPath(agent)
	if err != nil {
		return nil, fmt.Errorf("%s is not installed on this machine", agent)
	}
	cmd := exec.Command(bin, prompt)
	if cfg, ok := c.App.ConfigDir(); ok {
		cmd.Dir = cfg
	} else {
		cmd.Dir = c.App.Home
	}
	return cmd, nil
}

func (c CLI) prefsPath() string { return c.App.StatePath("tui.json") }

// Preference reads a TUI setting; a missing or unreadable file means unset.
func (c CLI) Preference(key string) string {
	data, err := os.ReadFile(c.prefsPath())
	if err != nil {
		return ""
	}
	var prefs map[string]string
	if json.Unmarshal(data, &prefs) != nil {
		return ""
	}
	return prefs[key]
}

// SetPreference stores a TUI setting.
func (c CLI) SetPreference(key, value string) error {
	prefs := map[string]string{}
	if data, err := os.ReadFile(c.prefsPath()); err == nil {
		_ = json.Unmarshal(data, &prefs) // a corrupt file is replaced, not fatal: these are conveniences
	}
	prefs[key] = value
	data, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.prefsPath()), 0o700); err != nil {
		return err
	}
	return os.WriteFile(c.prefsPath(), append(data, '\n'), 0o600)
}

// Shell runs this same fleet binary's ssh command in the foreground.
func (c CLI) Shell(target string) (*exec.Cmd, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return exec.Command(self, "ssh", target), nil
}

// Run shows the TUI until the user quits.
func Run(ctx context.Context, app *cli.App) error {
	_, err := tea.NewProgram(New(ctx, CLI{App: app}), tea.WithContext(ctx)).Run()
	return err
}
