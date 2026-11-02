package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/bundle"
	"github.com/vickingur/fleet-orchestrator/internal/container"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/remote"
)

// --- doctor ----------------------------------------------------------------

type doctorRow struct {
	Target   string            `json:"target"`
	Reach    string            `json:"reach"`
	Platform string            `json:"platform,omitempty"`
	Fleet    string            `json:"fleet,omitempty"`
	Current  bool              `json:"fleet_current"`
	Tools    map[string]string `json:"tools,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func doctorScript(inv *inventory.Inventory) string {
	var b strings.Builder
	b.WriteString("printf 'platform=%s\\n' \"$(uname -sm)\"\n")
	b.WriteString("v=$(fleet version 2>/dev/null); printf 'fleet=%s\\n' \"${v:-missing}\"\n")
	for _, t := range inv.Tools {
		fmt.Fprintf(&b, "v=$( { %s ; } 2>/dev/null | head -n1 ); printf 'tool:%s=%%s\\n' \"${v:-missing}\"\n", t.Version, t.Name)
	}
	return b.String()
}

func parseDoctor(out string, row *doctorRow) {
	row.Tools = map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch {
		case k == "platform":
			row.Platform = v
		case k == "fleet":
			row.Fleet = v
		case strings.HasPrefix(k, "tool:"):
			row.Tools[strings.TrimPrefix(k, "tool:")] = v
		}
	}
}

func (a *App) expectedVersion() string {
	if src, err := a.sourceDir(); err == nil {
		if v, err := bundle.Version(src); err == nil {
			return v
		}
	}
	if v, err := bundle.InstalledVersion(a.bundleDir()); err == nil {
		return v
	}
	return a.Version
}

func (a *App) cmdDoctor(ctx context.Context, args []string) error {
	fs := newFlags("doctor")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	target := "all"
	if fs.NArg() == 1 {
		target = fs.Arg(0)
	} else if fs.NArg() > 1 {
		return usagef("expects at most one target")
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	targets, err := inv.Resolve(target)
	if err != nil {
		return usagef("%v", err)
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	want := a.expectedVersion()
	script := doctorScript(inv)
	rows := make([]doctorRow, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			row := doctorRow{Target: t.String()}
			out, err := ex.output(ctx, t, "doctor", script, 30*time.Second)
			if err != nil {
				row.Reach, row.Error = "unreachable", err.Error()
			} else {
				row.Reach = "ok"
				parseDoctor(out, &row)
				row.Current = row.Fleet == want
			}
			rows[i] = row
		}()
	}
	wg.Wait()
	problems := 0
	for _, r := range rows {
		if r.Reach != "ok" || r.Fleet == "missing" || !r.Current {
			problems++
		}
		for _, t := range inv.Tools {
			if r.Tools[t.Name] == "missing" {
				problems++
			}
		}
	}
	problemErr := codeError{code: exitError, err: fmt.Errorf("%d problem(s) found", problems)}
	if *asJSON {
		if err := writeJSON(a.out, rows); err != nil {
			return err
		}
		if problems > 0 {
			return problemErr
		}
		return nil
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	header := []string{"TARGET", "REACH", "PLATFORM", "FLEET"}
	for _, t := range inv.Tools {
		header = append(header, strings.ToUpper(t.Name))
	}
	row(tw, header...)
	for _, r := range rows {
		fleet := r.Fleet
		switch {
		case r.Reach != "ok":
			fleet = "-"
		case !r.Current && r.Fleet != "missing":
			fleet = r.Fleet + " (stale, want " + want + ")"
		}
		cells := []string{r.Target, r.Reach, dash(r.Platform), fleet}
		for _, t := range inv.Tools {
			cells = append(cells, dash(r.Tools[t.Name]))
		}
		row(tw, cells...)
	}
	a.out.flush(tw)
	for _, r := range rows {
		if r.Error != "" {
			a.printf("%s: %s\n", r.Target, r.Error)
		}
	}
	if problems > 0 {
		return problemErr
	}
	return nil
}

// --- containers ------------------------------------------------------------

func (a *App) runtimeNode(inv *inventory.Inventory, name string) (inventory.Target, error) {
	n, err := inv.Node(name)
	if err != nil {
		return inventory.Target{}, usagef("%v", err)
	}
	if n.Runtime == "" {
		return inventory.Target{}, usagef("node %s has no container runtime", name)
	}
	return inventory.Target{Node: n}, nil
}

func (a *App) cmdImage(ctx context.Context, args []string) error {
	name, err := oneArg(args, "node")
	if err != nil {
		return err
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	t, err := a.runtimeNode(inv, name)
	if err != nil {
		return err
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	script := `[ -f "$HOME/.local/share/fleet/bundle/container/Containerfile" ] || { echo "no fleet bundle on this node; run fleet push ` + name + ` first" >&2; exit 3; }
` + container.BuildScript()
	res, err := ex.run(ctx, runSpec{target: t, script: script, label: "image", stdout: a.out, stderr: a.Stderr, timeout: 30 * time.Minute})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("image build on %s exited %d", name, res.ExitCode)
	}
	a.printf("built %s on %s\n", container.Image, name)
	return nil
}

func (a *App) cmdSpawn(ctx context.Context, args []string) error {
	fs := newFlags("spawn")
	tailnetJoin := fs.Bool("tailnet", true, "join the tailnet as agent-<name> (needs the fleet-ts-authkey podman secret)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return usagef("usage: fleet spawn [--tailnet=false] <node> <agent>")
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	t, err := a.runtimeNode(inv, fs.Arg(0))
	if err != nil {
		return err
	}
	agent := fs.Arg(1)
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	probeOut, err := ex.output(ctx, t, "spawn-probe", container.ProbeScript(), 30*time.Second)
	if err != nil {
		return err
	}
	probe, err := container.ParseProbe(probeOut)
	if err != nil {
		return err
	}
	rep, err := container.RunArgs(container.Spec{Agent: agent, Tailnet: *tailnetJoin}, probe)
	if err != nil {
		return err
	}
	if _, err := ex.output(ctx, t, "spawn", container.RunScript(rep.Args), 2*time.Minute); err != nil {
		return err
	}
	name := remote.ContainerName(agent)
	waitScript := `i=0
while [ $i -lt 60 ]; do
  if podman logs ` + name + ` 2>&1 | grep -q "agent ` + agent + ` ready"; then echo ready; exit 0; fi
  if [ "$(podman inspect -f '{{.State.Status}}' ` + name + `)" = exited ]; then break; fi
  sleep 1; i=$((i+1))
done
echo "container did not become ready; last log lines:" >&2
podman logs --tail 30 ` + name + ` >&2
exit 1
`
	if _, err := ex.output(ctx, t, "spawn-wait", waitScript, 2*time.Minute); err != nil {
		return err
	}
	a.printf("agent %s is running on %s as %s\n", agent, t.Node.Name, name)
	for _, n := range rep.Notes {
		a.printf("  %s\n", strings.ReplaceAll(n, "<node>", t.Node.Name))
	}
	a.printf("run commands in it with: fleet exec %s:%s -- <command>\n", t.Node.Name, agent)
	return nil
}

type containerRow struct {
	Node string `json:"node"`
	container.Container
	Error string `json:"error,omitempty"`
}

func (a *App) cmdContainers(ctx context.Context, args []string) error {
	fs := newFlags("containers")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	var hosts []inventory.Target
	switch fs.NArg() {
	case 0:
		for _, n := range inv.Nodes {
			if n.Runtime != "" {
				hosts = append(hosts, inventory.Target{Node: n})
			}
		}
	case 1:
		t, err := a.runtimeNode(inv, fs.Arg(0))
		if err != nil {
			return err
		}
		hosts = []inventory.Target{t}
	default:
		return usagef("expects at most one node")
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	rows := []containerRow{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, h := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := ex.output(ctx, h, "containers", container.ListScript(), 30*time.Second)
			var cs []container.Container
			if err == nil {
				cs, err = container.ParseList([]byte(out))
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				rows = append(rows, containerRow{Node: h.Node.Name, Error: err.Error()})
				return
			}
			for _, c := range cs {
				rows = append(rows, containerRow{Node: h.Node.Name, Container: c})
			}
		}()
	}
	wg.Wait()
	var errs []error
	for _, r := range rows {
		if r.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", r.Node, r.Error))
		}
	}
	if *asJSON {
		if err := writeJSON(a.out, rows); err != nil {
			return err
		}
		return errors.Join(errs...)
	}
	if len(rows) == 0 {
		a.println("no agent containers")
		return nil
	}
	tw := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	row(tw, "TARGET", "STATE", "STATUS")
	for _, r := range rows {
		if r.Error != "" {
			continue
		}
		row(tw, r.Node+":"+r.Agent, r.State, r.Status)
	}
	a.out.flush(tw)
	return errors.Join(errs...)
}

func (a *App) cmdRm(ctx context.Context, args []string) error {
	fs := newFlags("rm")
	volumes := fs.Bool("volumes", false, "also delete the agent's work, claude and codex volumes")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return usagef("usage: fleet rm [--volumes] <node> <agent>")
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	t, err := a.runtimeNode(inv, fs.Arg(0))
	if err != nil {
		return err
	}
	if !inventory.ValidAgentName(fs.Arg(1)) {
		return usagef("invalid agent name %q", fs.Arg(1))
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	if _, err := ex.output(ctx, t, "rm", container.RemoveScript(fs.Arg(1), *volumes), 2*time.Minute); err != nil {
		return err
	}
	a.printf("removed %s:%s\n", t.Node.Name, fs.Arg(1))
	return nil
}
