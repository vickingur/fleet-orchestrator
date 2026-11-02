package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/remote"
)

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		var b strings.Builder
		fs.VisitAll(func(f *flag.Flag) { fmt.Fprintf(&b, "\n  -%s\t%s (default %q)", f.Name, f.Usage, f.DefValue) })
		return usagef("flags:%s", b.String())
	} else if err != nil {
		return usagef("%v", err)
	}
	return nil
}

// --- ls --------------------------------------------------------------------

type lsNode struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	OS       string `json:"os"`
	Role     string `json:"role"`
	State    string `json:"state"`
	Address  string `json:"address,omitempty"`
	LastSeen string `json:"last_seen,omitempty"`
	Runtime  string `json:"runtime,omitempty"`
}

type lsAgent struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Address string `json:"address,omitempty"`
}

type lsOutput struct {
	Nodes   []lsNode  `json:"nodes"`
	Agents  []lsAgent `json:"tailnet_agents"`
	Tailnet string    `json:"tailnet_error,omitempty"`
}

func (a *App) cmdLs(ctx context.Context, args []string) error {
	fs := newFlags("ls")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("takes no arguments")
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	out := lsOutput{Nodes: []lsNode{}, Agents: []lsAgent{}}
	st, stErr := a.tailnetStatus(ctx)
	if stErr != nil {
		out.Tailnet = stErr.Error()
	}
	for _, n := range inv.Nodes {
		row := lsNode{Name: n.Name, Host: n.Host, OS: string(n.OS), Role: string(n.Role), Runtime: n.Runtime}
		switch p, found := st.Find(n.Host); {
		case stErr != nil:
			row.State = "unknown"
		case !found:
			row.State = "not on tailnet"
		case p.Self:
			row.State, row.Address = "this machine", firstIP(p.IPs)
		case p.Online:
			row.State, row.Address = "online", firstIP(p.IPs)
		default:
			row.State, row.Address = "offline", firstIP(p.IPs)
			if !p.LastSeen.IsZero() {
				row.LastSeen = ago(p.LastSeen, a.Now())
				row.State = "offline, last seen " + row.LastSeen
			}
		}
		out.Nodes = append(out.Nodes, row)
	}
	for _, p := range st.Tagged("tag:agent") {
		state := "offline"
		if p.Online {
			state = "online"
		}
		out.Agents = append(out.Agents, lsAgent{Name: p.Name, State: state, Address: firstIP(p.IPs)})
	}
	if *asJSON {
		return writeJSON(a.out, out)
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	row(tw, "NODE", "SSH HOST", "OS", "ROLE", "STATE", "ADDRESS")
	for _, r := range out.Nodes {
		row(tw, r.Name, r.Host, r.OS, r.Role, r.State, dash(r.Address))
	}
	a.out.flush(tw)
	if len(out.Agents) > 0 {
		a.println("\nAgent containers on the tailnet:")
		for _, ag := range out.Agents {
			a.printf("  %s  %s  %s\n", ag.Name, ag.State, ag.Address)
		}
	}
	if stErr != nil {
		a.printf("\ntailnet status unavailable: %s\n", stErr)
	}
	return nil
}

func firstIP(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// --- exec ------------------------------------------------------------------

type execResult struct {
	Target   string `json:"target"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error,omitempty"`
	Millis   int64  `json:"ms"`
}

func (a *App) cmdExec(ctx context.Context, args []string) error {
	fs := newFlags("exec")
	asJSON := fs.Bool("json", false, "machine-readable output (buffers output)")
	timeout := fs.Duration("timeout", 10*time.Minute, "kill the command after this long")
	detach := fs.Bool("detach", false, "start the command in the background, print its log path and return at once")
	if err := parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) < 3 || rest[1] != "--" {
		return usagef("usage: fleet exec [--json] [--timeout 10m] [--detach] <target> -- <command>")
	}
	script := strings.Join(rest[2:], " ")
	if *detach {
		script = detached(script)
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	targets, err := inv.Resolve(rest[0])
	if err != nil {
		return usagef("%v", err)
	}
	if *detach && len(targets) != 1 {
		return usagef("--detach takes one target")
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	if len(targets) == 1 && !*asJSON {
		res, err := ex.run(ctx, runSpec{target: targets[0], script: script, label: "exec",
			stdin: a.Stdin, stdout: a.out, stderr: a.Stderr, timeout: *timeout})
		if err != nil {
			return err
		}
		if res.ExitCode != 0 {
			return codeError{code: res.ExitCode, err: fmt.Errorf("")}
		}
		return nil
	}
	results := a.fanOut(ctx, ex, targets, script, *timeout)
	failed := 0
	for _, r := range results {
		if r.ExitCode != 0 || r.Error != "" {
			failed++
		}
	}
	if *asJSON {
		if err := writeJSON(a.out, results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			status := fmt.Sprintf("exit %d, %s", r.ExitCode, time.Duration(r.Millis)*time.Millisecond)
			if r.Error != "" {
				status = "error: " + r.Error
			}
			a.printf("── %s (%s)\n%s", r.Target, status, r.Stdout)
			if r.Stderr != "" {
				a.print(prefixLines(r.Stderr, "stderr: "))
			}
		}
	}
	if failed > 0 {
		return codeError{code: exitError, err: fmt.Errorf("%d of %d targets failed", failed, len(results))}
	}
	return nil
}

// detached wraps a script to run in the background with its output in a log under ~/.local/state/fleet/jobs, and
// prints the log's path. A plain `nohup cmd &` keeps the ssh session open until cmd ends; started from a backgrounded
// subshell the job is not a child of the session's shell, so the session closes at once.
func detached(script string) string {
	return `d="$HOME/.local/state/fleet/jobs"; mkdir -p "$d"; f="$d/$(date +%Y%m%d-%H%M%S)-$$.log"; ` +
		`(nohup sh -c ` + shellQuote(script) + ` </dev/null >"$f" 2>&1 &); echo "$f"`
}

func (a *App) fanOut(ctx context.Context, ex *executor, targets []inventory.Target, script string, timeout time.Duration) []execResult {
	results := make([]execResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out, errb bytes.Buffer
			res, err := ex.run(ctx, runSpec{target: t, script: script, label: "exec", stdout: &out, stderr: &errb, timeout: timeout})
			r := execResult{Target: t.String(), ExitCode: res.ExitCode, Stdout: out.String(), Stderr: errb.String(), Millis: res.Duration.Milliseconds()}
			if err != nil {
				r.Error = err.Error()
			}
			results[i] = r
		}()
	}
	wg.Wait()
	return results
}

func prefixLines(s, prefix string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// --- ssh -------------------------------------------------------------------

// sessionLimit satisfies execx's rule that every run has a bound. It is far
// beyond any session a person keeps open: they end it, not a clock.
const sessionLimit = 365 * 24 * time.Hour

func (a *App) cmdSSH(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("usage: fleet ssh <target> [command]")
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	targets, err := inv.Resolve(args[0])
	if err != nil || len(targets) != 1 {
		return usagef("ssh needs exactly one node or node:agent target")
	}
	t := targets[0]
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	script := strings.Join(args[1:], " ")
	if t.Agent != "" {
		inner := "exec bash -l"
		if script != "" {
			inner = script
		}
		script = "exec podman exec -it " + remote.ContainerName(t.Agent) + " " + remote.ShellCommand(inner)
	}
	var c execx.Cmd
	if t.Node.Name == ex.self {
		shell := a.Getenv("SHELL")
		if script != "" || shell == "" {
			shell = "sh"
		}
		c = execx.Cmd{Name: shell}
		if script != "" {
			c.Args = []string{"-c", remote.ShellCommand(script)}
		}
	} else {
		if script == "" {
			script = remote.PersistentSession
		}
		c = execx.Cmd{Name: "ssh", Args: a.sshOptions().InteractiveArgs(t.Node, script)}
	}
	c.Stdin, c.Stdout, c.Stderr, c.Timeout = a.Stdin, a.Stdout, a.Stderr, sessionLimit
	end, err := ex.audited("ssh", t.String(), script)
	if err != nil {
		return err
	}
	res, err := a.Runner.Run(ctx, c)
	if logErr := end(res, err); logErr != nil {
		return errors.Join(err, logErr)
	}
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return codeError{code: res.ExitCode, err: fmt.Errorf("")}
	}
	return nil
}
