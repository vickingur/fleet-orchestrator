// Package cli implements the fleet command. App holds every external
// dependency so commands can be exercised against fakes or a test node.
package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/vickingur/fleet-orchestrator/internal/audit"
	"github.com/vickingur/fleet-orchestrator/internal/bundle"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/remote"
	"github.com/vickingur/fleet-orchestrator/internal/tailnet"
)

// Exit codes.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitConflict = 3
	exitDrift    = 4
)

// App is the fleet command with its environment.
type App struct {
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Runner  execx.Runner
	Getenv  func(string) string
	Home    string
	Cwd     string
	Now     func() time.Time

	out *output
}

// output wraps stdout and records the first write error, so commands can
// print freely and still fail when their output was lost (closed pipe, full disk).
type output struct {
	w   io.Writer
	err error
}

func (o *output) Write(p []byte) (int, error) {
	if o.err != nil {
		return 0, o.err
	}
	n, err := o.w.Write(p)
	if err != nil {
		o.err = err
	}
	return n, err
}

// The write error is kept in o.err and checked once in App.Run.
func (o *output) printf(format string, a ...any) { _, _ = fmt.Fprintf(o, format, a...) }
func (o *output) println(a ...any)               { _, _ = fmt.Fprintln(o, a...) }
func (o *output) print(a ...any)                 { _, _ = fmt.Fprint(o, a...) }

// row writes one tab-separated table row. tabwriter buffers in memory and the
// eventual write to o fails into o.err, so the per-row error carries nothing new.
func row(tw *tabwriter.Writer, cells ...string) { _, _ = fmt.Fprintln(tw, strings.Join(cells, "\t")) }

// flush writes buffered tabwriter output; a failure lands in o.err.
func (o *output) flush(tw *tabwriter.Writer) { _ = tw.Flush() }

func (a *App) printf(format string, args ...any) { a.out.printf(format, args...) }
func (a *App) println(args ...any)               { a.out.println(args...) }
func (a *App) print(args ...any)                 { a.out.print(args...) }

// eprintf reports to stderr; if stderr itself fails there is nowhere left to report.
func (a *App) eprintf(format string, args ...any) { _, _ = fmt.Fprintf(a.Stderr, format, args...) }

type command struct {
	name    string
	summary string
	run     func(a *App, ctx context.Context, args []string) error
}

func commands() []command {
	return []command{
		{"ls", "list nodes with live tailnet status", (*App).cmdLs},
		{"doctor", "check reachability, tool versions and installed fleet version", (*App).cmdDoctor},
		{"drift", "show what a push would change, without changing it", (*App).cmdDrift},
		{"exec", "run a shell command on a target: fleet exec <target> -- <command>", (*App).cmdExec},
		{"ssh", "open an interactive session on a target", (*App).cmdSSH},
		{"push", "install the shared layer and fleet on a target (controller)", (*App).cmdPush},
		{"apply", "apply the installed shared layer to this machine", (*App).cmdApply},
		{"adopt", "copy edited shared files from a node back into the config (controller)", (*App).cmdAdopt},
		{"restore", "list this machine's backup sets, or put one back: fleet restore [<set> [~/path ...]]", (*App).cmdRestore},
		{"image", "build the agent container image on a node", (*App).cmdImage},
		{"spawn", "start an agent container: fleet spawn <node> <agent>", (*App).cmdSpawn},
		{"containers", "list agent containers on a node or all nodes", (*App).cmdContainers},
		{"rm", "remove an agent container: fleet rm <node> <agent>", (*App).cmdRm},
		{"codex", "attach this machine's Codex TUI to Codex on a node", (*App).cmdCodex},
		{"docs", "regenerate FLEET.md in the fleet config from its fleet.toml", (*App).cmdDocs},
		{"init", "create a fleet config, or clone one: fleet init [<git-url>]", (*App).cmdInit},
		{"check", "validate a fleet config before pushing it: fleet check [dir]", (*App).cmdCheck},
		{"version", "print the fleet version", (*App).cmdVersion},
	}
}

// usageError is a mistake in how fleet was called.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// exitError carries a specific exit code with an already-printed outcome.
type codeError struct {
	code int
	err  error
}

func (e codeError) Error() string { return e.err.Error() }

// Run executes args (without the program name) and returns the exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	a.out = &output{w: a.Stdout}
	code := a.dispatch(ctx, args)
	if a.out.err != nil {
		a.eprintf("fleet: writing output: %v\n", a.out.err)
		if code == exitOK {
			code = exitError
		}
	}
	return code
}

func (a *App) dispatch(ctx context.Context, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		a.print(a.usage())
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	for _, c := range commands() {
		if c.name != args[0] {
			continue
		}
		err := c.run(a, ctx, args[1:])
		var ue usageError
		var ce codeError
		switch {
		case err == nil:
			return exitOK
		case errors.As(err, &ue):
			a.eprintf("fleet %s: %s\n", c.name, ue.msg)
			return exitUsage
		case errors.As(err, &ce):
			if ce.err.Error() != "" {
				a.eprintf("fleet %s: %s\n", c.name, ce.err)
			}
			return ce.code
		default:
			a.eprintf("fleet %s: %s\n", c.name, err)
			return exitError
		}
	}
	a.eprintf("fleet: unknown command %q\n\n%s", args[0], a.usage())
	return exitUsage
}

// Capture runs one fleet command in-process with its own output buffers and
// returns stdout, stderr and the exit code. Frontends such as the TUI use it
// with --json so they share every code path with the command line.
func (a *App) Capture(ctx context.Context, args ...string) (stdout, stderr []byte, code int) {
	var out, errb bytes.Buffer
	sub := *a
	sub.Stdin, sub.Stdout, sub.Stderr = strings.NewReader(""), &out, &errb
	code = sub.Run(ctx, args)
	return out.Bytes(), errb.Bytes(), code
}

// IsController reports whether this process can push: it finds both a fleet
// config and the source checkout to build fleet from.
func (a *App) IsController() bool {
	_, srcErr := a.sourceDir()
	_, cfgErr := a.configDir()
	return srcErr == nil && cfgErr == nil
}

// ConfigDir returns the fleet config when this machine has one.
func (a *App) ConfigDir() (string, bool) {
	cfg, err := a.configDir()
	return cfg, err == nil
}

// StatePath is a file in fleet's per-machine state directory.
func (a *App) StatePath(name string) string { return filepath.Join(a.stateDir(), name) }

// AuditPath is this machine's audit log.
func (a *App) AuditPath() string { return filepath.Join(a.stateDir(), "audit.jsonl") }

// GuidePath is the agent guide to show: the config's where there is one,
// else the installed copy.
func (a *App) GuidePath() string {
	if cfg, err := a.configDir(); err == nil {
		return filepath.Join(cfg, "FLEET.md")
	}
	return filepath.Join(a.Home, ".config", "fleet", "FLEET.md")
}

func (a *App) usage() string {
	var b strings.Builder
	b.WriteString("fleet: operate machines and agent containers over SSH; optional Tailscale discovery\n")
	b.WriteString("\nUsage: fleet <command> [flags] [args]\n\nCommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "  %-11s %s\n", c.name, c.summary)
	}
	b.WriteString("\nRun fleet with no arguments in a terminal for the interactive view.\n")
	b.WriteString("Targets: a node name, node:agent for an agent container, a comma list, or all.\n")
	b.WriteString("Without Tailscale: set FLEET_SELF to this local machine's inventory name.\n")
	b.WriteString("Agent guide: ~/.config/fleet/FLEET.md\n")
	return b.String()
}

func (a *App) cmdVersion(_ context.Context, args []string) error {
	if len(args) != 0 {
		return usagef("takes no arguments")
	}
	a.println(a.Version)
	return nil
}

// --- locations -------------------------------------------------------------

func (a *App) bundleDir() string {
	if d := a.Getenv("FLEET_BUNDLE"); d != "" {
		return d
	}
	return filepath.Join(a.Home, ".local", "share", "fleet", "bundle")
}

func (a *App) stateDir() string {
	return filepath.Join(a.Home, ".local", "state", "fleet")
}

// DefaultConfigDir is where fleet init puts the config and fleet looks for it,
// next to the installed bundle.
func (a *App) DefaultConfigDir() string {
	return filepath.Join(a.Home, ".local", "share", "fleet", "config")
}

// sourceDir finds the source checkout push builds fleet from: FLEET_SRC, the
// checkout fleet runs in, or the one the installed bundle was pushed from.
func (a *App) sourceDir() (string, error) {
	if d := a.Getenv("FLEET_SRC"); d != "" {
		if !bundle.IsSource(d) {
			return "", fmt.Errorf("FLEET_SRC=%s is not a fleet source checkout", d)
		}
		return d, nil
	}
	if src, err := bundle.FindSource(a.Cwd); err == nil {
		return src, nil
	}
	if recorded, ok := bundle.InstalledSource(a.bundleDir()); ok {
		return recorded, nil
	}
	return "", errors.New("no fleet source checkout: run from one or set FLEET_SRC")
}

// configDir finds the fleet config: FLEET_CONFIG, the config fleet runs in,
// or the default location.
func (a *App) configDir() (string, error) {
	if d := a.Getenv("FLEET_CONFIG"); d != "" {
		if !bundle.IsConfig(d) {
			return "", fmt.Errorf("FLEET_CONFIG=%s is not a fleet config: it needs fleet.toml and home/manifest.toml", d)
		}
		return d, nil
	}
	if cfg, err := bundle.FindConfig(a.Cwd); err == nil {
		return cfg, nil
	}
	if d := a.DefaultConfigDir(); bundle.IsConfig(d) {
		return d, nil
	}
	return "", fmt.Errorf("no fleet config at %s: run fleet init, or set FLEET_CONFIG", a.DefaultConfigDir())
}

// inventory loads the inventory the command operates on: the config's where
// there is one (so edits take effect before a push), else the installed bundle's.
func (a *App) inventory() (*inventory.Inventory, error) {
	if cfg, err := a.configDir(); err == nil {
		return bundle.LoadInventory(cfg)
	}
	inv, err := bundle.LoadInventory(a.bundleDir())
	if err != nil {
		return nil, fmt.Errorf("no inventory: no fleet config and no installed bundle (%w)", err)
	}
	return inv, nil
}

// --- execution -------------------------------------------------------------

func (a *App) sshOptions() remote.SSHOptions {
	return remote.SSHOptions{
		KnownHostsFile: filepath.Join(a.stateDir(), "known_hosts"),
		ConnectTimeout: 8 * time.Second,
		ProxyCommand:   a.Getenv("FLEET_SSH_PROXY"),
	}
}

func (a *App) tailnetStatus(ctx context.Context) (tailnet.Status, error) {
	return tailnet.Reader{Runner: a.Runner, Socket: a.Getenv("FLEET_TAILSCALE_SOCKET")}.Status(ctx)
}

// selfNode returns the inventory node this process runs on, if any: FLEET_SELF
// when set, else the node whose host is this machine's MagicDNS name.
func (a *App) selfNode(ctx context.Context, inv *inventory.Inventory) (string, error) {
	if s := a.Getenv("FLEET_SELF"); s != "" {
		if _, err := inv.Node(s); err != nil {
			return "", fmt.Errorf("FLEET_SELF: %w", err)
		}
		return s, nil
	}
	if a.Getenv("FLEET_AGENT") != "" {
		return "", nil // inside an agent container: no inventory node is local
	}
	st, err := a.tailnetStatus(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot tell which node this machine is (set FLEET_SELF to say): %w", err)
	}
	self, _ := st.Self()
	for _, n := range inv.Nodes {
		if strings.EqualFold(n.Host, self.Name) {
			return n.Name, nil
		}
	}
	return "", nil
}

// executor runs scripts on targets, locally or over SSH, and audits each run.
type executor struct {
	app  *App
	self string
	log  *audit.Log
}

func (a *App) executor(ctx context.Context, inv *inventory.Inventory) (*executor, error) {
	self, err := a.selfNode(ctx, inv)
	if err != nil {
		return nil, err
	}
	return &executor{app: a, self: self, log: &audit.Log{Path: filepath.Join(a.stateDir(), "audit.jsonl")}}, nil
}

type runSpec struct {
	target  inventory.Target
	script  string
	label   string // command name for the audit log
	detail  string // what to record; defaults to the script
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	timeout time.Duration
}

func (e *executor) cmdFor(t inventory.Target, script string) execx.Cmd {
	if t.Agent != "" {
		script = remote.InContainer(t.Agent, script)
	}
	if t.Node.Name == e.self {
		return execx.Cmd{Name: "sh", Args: []string{"-c", remote.ShellCommand(script)}}
	}
	return execx.Cmd{Name: "ssh", Args: e.app.sshOptions().ExecArgs(t.Node, script)}
}

func (e *executor) run(ctx context.Context, s runSpec) (execx.Result, error) {
	if err := os.MkdirAll(e.app.stateDir(), 0o700); err != nil {
		return execx.Result{}, err
	}
	c := e.cmdFor(s.target, s.script)
	c.Stdin, c.Stdout, c.Stderr, c.Timeout = s.stdin, s.stdout, s.stderr, s.timeout
	if c.Stdin == nil {
		c.Stdin = strings.NewReader("")
	}
	detail := s.detail
	if detail == "" {
		detail = s.script
	}
	end, err := e.audited(s.label, s.target.String(), detail)
	if err != nil {
		return execx.Result{}, err
	}
	res, err := e.app.Runner.Run(ctx, c)
	if logErr := end(res, err); logErr != nil {
		return res, errors.Join(err, logErr)
	}
	if err == nil && res.ExitCode == 255 && c.Name == "ssh" {
		err = fmt.Errorf("ssh to %s (%s@%s:%d) failed; check it is online with fleet ls", s.target, s.target.Node.User, s.target.Node.Host, s.target.Node.SSHPort())
	}
	return res, err
}

// output runs a script and returns stdout, failing on non-zero exit.
func (e *executor) output(ctx context.Context, t inventory.Target, label, script string, timeout time.Duration) (string, error) {
	var out, errb bytes.Buffer
	res, err := e.run(ctx, runSpec{target: t, script: script, label: label, stdout: &out, stderr: &errb, timeout: timeout})
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s on %s exited %d: %s", label, t, res.ExitCode, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// --- small helpers ---------------------------------------------------------

// splitTarget validates that exactly one positional target was given.
func oneArg(args []string, what string) (string, error) {
	if len(args) != 1 {
		return "", usagef("expects exactly one %s", what)
	}
	return args[0], nil
}

func ago(t time.Time, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// audited writes a start record before an action and returns the function
// that writes its end record. If the start cannot be recorded the action must
// not run: an unaudited remote action is worse than a refused one.
func (e *executor) audited(label, target, detail string) (func(execx.Result, error) error, error) {
	id, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	rec := audit.Record{ID: id, Phase: "start", Time: e.app.Now().UTC(), Actor: audit.Actor(), Command: label,
		Target: target, Detail: truncate(detail, 500)}
	if err := e.log.Append(rec); err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	started := e.app.Now()
	return func(res execx.Result, runErr error) error {
		end := rec
		end.Phase, end.Time, end.ExitCode = "end", e.app.Now().UTC(), res.ExitCode
		end.Millis = e.app.Now().Sub(started).Milliseconds()
		if res.Duration > 0 {
			end.Millis = res.Duration.Milliseconds()
		}
		if runErr != nil {
			end.Error = runErr.Error()
		}
		if err := e.log.Append(end); err != nil {
			return fmt.Errorf("audit log: %w", err)
		}
		return nil
	}, nil
}

// truncate shortens s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
