package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/bundle"
	"github.com/vickingur/fleet-orchestrator/internal/docs"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
	"github.com/vickingur/fleet-orchestrator/internal/syncer"
)

// --- apply -----------------------------------------------------------------

type applyOutput struct {
	Version string          `json:"version"`
	Pending bool            `json:"pending"`
	Counts  map[string]int  `json:"counts"`
	Changes []syncer.Change `json:"changes"`
	Ensures []ensureResult  `json:"ensures"`
	Applied bool            `json:"applied"`
	// Conflicts and Failed are set by a real (non dry-run) apply.
	Conflicts []string `json:"conflicts,omitempty"`
	Failed    []string `json:"failed,omitempty"`
}

type ensureResult struct {
	Name      string `json:"name"`
	Satisfied bool   `json:"satisfied"`
	Fixed     bool   `json:"fixed,omitempty"`
	// Snapshot lists the files saved into the backup set before the fix ran.
	Snapshot []string `json:"snapshot,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// runEnsures checks each ensure locally and, unless dryRun, fixes the unmet
// ones and checks again. A fix that does not make its check pass is an error.
// Before a fix runs, the files it declares in snapshot are copied into
// backupDir; a snapshot that cannot be taken stops the fix.
func (a *App) runEnsures(ctx context.Context, ensures []syncer.Ensure, dryRun bool, backupDir string) []ensureResult {
	sh := func(script string, timeout time.Duration) (int, string, error) {
		var out bytes.Buffer
		res, err := a.Runner.Run(ctx, execx.Cmd{Name: "sh", Args: []string{"-c", script}, Stdout: &out, Stderr: &out, Timeout: timeout})
		return res.ExitCode, strings.TrimSpace(out.String()), err
	}
	results := make([]ensureResult, len(ensures))
	for i, e := range ensures {
		r := ensureResult{Name: e.Name}
		code, _, err := sh(e.Check, time.Minute)
		switch {
		case err != nil:
			r.Error = err.Error()
		case code == 0:
			r.Satisfied = true
		case !dryRun:
			if err := a.snapshot(e.Snapshot, backupDir, &r); err != nil {
				r.Error = "snapshot: " + err.Error()
			} else if code, out, err := sh(e.Fix, 10*time.Minute); err != nil {
				r.Error = "fix: " + err.Error()
			} else if code != 0 {
				r.Error = fmt.Sprintf("fix exited %d: %s", code, out)
			} else if code, _, err := sh(e.Check, time.Minute); err != nil || code != 0 {
				r.Error = "fix ran but the check still fails"
			} else {
				r.Satisfied, r.Fixed = true, true
			}
		}
		results[i] = r
	}
	return results
}

func (a *App) snapshot(keys []string, backupDir string, r *ensureResult) error {
	for _, key := range keys {
		abs := filepath.Join(a.Home, filepath.FromSlash(strings.TrimPrefix(key, "~/")))
		saved, err := syncer.BackupFile(abs, key, backupDir)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if saved {
			r.Snapshot = append(r.Snapshot, key)
		}
	}
	return nil
}

func (a *App) cmdApply(ctx context.Context, args []string) error {
	fs := newFlags("apply")
	dryRun := fs.Bool("dry-run", false, "show the plan without changing anything")
	force := fs.Bool("force", false, "overwrite locally edited files (they are backed up first)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	failOnPending := fs.Bool("fail-on-pending", false, "with --dry-run, exit 4 when anything would change")
	bundleFlag := fs.String("bundle", "", "bundle directory (default: the installed bundle)")
	if err := parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("takes no arguments")
	}
	dir := a.bundleDir()
	if *bundleFlag != "" {
		dir = *bundleFlag
	}
	version, err := bundle.InstalledVersion(dir)
	if err != nil {
		return err
	}
	manifestData, err := os.ReadFile(filepath.Join(dir, "home", "manifest.toml"))
	if err != nil {
		return err
	}
	man, err := syncer.ParseManifest(manifestData)
	if err != nil {
		return err
	}
	statePath := filepath.Join(a.stateDir(), "state.json")
	st, err := syncer.LoadState(statePath, filepath.Join(a.stateDir(), "backup"))
	if err != nil {
		return err
	}
	plan, err := syncer.Build(man, st, syncer.Options{Bundle: filepath.Join(dir, "home"), Home: a.Home, Force: *force})
	if err != nil {
		return err
	}
	out := applyOutput{Version: version, Pending: plan.Pending(), Counts: map[string]int{}, Changes: plan.Changes}
	for k, v := range plan.Counts() {
		out.Counts[string(k)] = v
	}
	// One backup set per apply, shared by file changes and ensure snapshots.
	// Nanosecond names keep two applies in the same second from sharing a set.
	backups := filepath.Join(a.stateDir(), "backup", a.Now().UTC().Format("20060102T150405.000000000Z"))
	var applyErr error
	if !*dryRun {
		var res syncer.ApplyResult
		res, applyErr = plan.Apply(syncer.ApplyOptions{Home: a.Home, StateFile: statePath, BackupDir: backups})
		out.Conflicts, out.Failed, out.Applied = res.Conflicts, res.Failed, applyErr == nil
	}
	out.Ensures = a.runEnsures(ctx, man.Ensures, *dryRun, backups)
	var ensureErrs []error
	for _, e := range out.Ensures {
		if !e.Satisfied {
			out.Pending = true
		}
		if e.Error != "" {
			ensureErrs = append(ensureErrs, fmt.Errorf("ensure %s: %s", e.Name, e.Error))
		}
	}
	if *asJSON {
		if err := writeJSON(a.out, out); err != nil {
			return err
		}
	} else {
		printPlan(a.out, plan, *dryRun)
		for _, t := range out.Failed {
			a.printf("%-9s %s\n", "failed", t)
		}
		printEnsures(a.out, out.Ensures, *dryRun)
	}
	conflicts := ""
	if len(out.Conflicts) > 0 {
		conflicts = fmt.Sprintf("%d conflict(s) left untouched: %s; resolve with fleet adopt on the controller, or apply --force to take the shared version",
			len(out.Conflicts), strings.Join(out.Conflicts, ", "))
	}
	switch {
	case applyErr != nil || len(ensureErrs) > 0:
		err := errors.Join(applyErr, errors.Join(ensureErrs...))
		if conflicts != "" {
			err = errors.Join(err, errors.New(conflicts))
		}
		return err
	case conflicts != "":
		return codeError{code: exitConflict, err: errors.New(conflicts)}
	case *dryRun && *failOnPending && out.Pending:
		return codeError{code: exitDrift, err: errors.New("")}
	}
	return nil
}

// printPlan shows every change except unchanged files, collapsing a new
// directory of files into one line.
func printPlan(w *output, plan syncer.Plan, dryRun bool) {
	type group struct {
		count int
		first int
	}
	groups := map[string]*group{}
	var order []string
	lines := map[string]string{}
	for i, c := range plan.Changes {
		if c.Action == syncer.Unchanged {
			continue
		}
		key := c.Target
		if c.Action == syncer.Create && strings.HasPrefix(c.Rule, "tree:") {
			_, treeTarget, _ := strings.Cut(c.Rule, "->")
			rel := strings.TrimPrefix(c.Target, treeTarget+"/")
			if first, _, nested := strings.Cut(rel, "/"); nested {
				key = treeTarget + "/" + first + "/"
			}
		}
		g, ok := groups[key]
		if !ok {
			g = &group{first: i}
			groups[key] = g
			order = append(order, key)
		}
		g.count++
		detail := ""
		if c.Detail != "" {
			detail = "  (" + c.Detail + ")"
		}
		lines[key] = fmt.Sprintf("%-9s %s%s", c.Action, c.Target, detail)
	}
	for _, key := range order {
		g := groups[key]
		if g.count > 1 {
			w.printf("%-9s %s  (%d files)\n", syncer.Create, key, g.count)
		} else {
			w.println(lines[key])
		}
	}
	counts := plan.Counts()
	verb := "applied"
	if dryRun {
		verb = "planned"
	}
	var parts []string
	for _, act := range []syncer.Action{syncer.Create, syncer.Update, syncer.Merge, syncer.Delete, syncer.Release, syncer.Conflict, syncer.Unchanged} {
		if counts[act] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[act], act))
		}
	}
	w.printf("%s: %s\n", verb, strings.Join(parts, ", "))
}

func printEnsures(w *output, results []ensureResult, dryRun bool) {
	met := 0
	for _, r := range results {
		switch {
		case r.Error != "":
			w.printf("%-9s %s  (%s)\n", "failed", r.Name, r.Error)
		case r.Fixed:
			w.printf("%-9s %s\n", "ensured", r.Name)
		case !r.Satisfied && dryRun:
			w.printf("%-9s %s\n", "ensure", r.Name)
		default:
			met++
		}
	}
	if len(results) > 0 {
		w.printf("ensures: %d of %d already met\n", met, len(results))
	}
}

// --- push / drift ----------------------------------------------------------

const installScript = `set -eu
d="$HOME/.local/share/fleet"
mkdir -p "$d" "$HOME/.local/bin"
rm -rf "$d/bundle.new"
mkdir -p "$d/bundle.new"
tar -xzf - -C "$d/bundle.new"
if [ -d "$d/bundle" ]; then rm -rf "$d/bundle.old"; mv "$d/bundle" "$d/bundle.old"; fi
mv "$d/bundle.new" "$d/bundle"
rm -rf "$d/bundle.old"
ln -sfn "$d/bundle/bin/fleet" "$HOME/.local/bin/fleet"
"$d/bundle/bin/fleet" apply %s
`

const previewScript = `set -eu
t=$(mktemp -d)
trap 'rm -rf "$t"' EXIT
tar -xzf - -C "$t"
"$t/bin/fleet" apply --dry-run --fail-on-pending --bundle "$t" %s
`

type pushResult struct {
	Node     string `json:"node"`
	ExitCode int    `json:"exit_code"`
	// Output is the node's apply output: JSON when push ran with --json.
	Output string `json:"output"`
	Stderr string `json:"stderr,omitempty"`
	Error  string `json:"error,omitempty"`
}

func (a *App) cmdPush(ctx context.Context, args []string) error {
	fs := newFlags("push")
	dryRun := fs.Bool("dry-run", false, "show what would change without installing anything")
	force := fs.Bool("force", false, "overwrite locally edited files on the target (backed up first)")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	target, err := oneArg(fs.Args(), "target")
	if err != nil {
		return err
	}
	return a.push(ctx, target, *dryRun, *force, *asJSON)
}

func (a *App) cmdDrift(ctx context.Context, args []string) error {
	fs := newFlags("drift")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	target := "all"
	if fs.NArg() > 1 {
		return usagef("expects at most one target")
	}
	if fs.NArg() == 1 {
		target = fs.Arg(0)
	}
	return a.push(ctx, target, true, false, *asJSON)
}

func (a *App) push(ctx context.Context, target string, dryRun, force, asJSON bool) error {
	src, err := a.sourceDir()
	if err != nil {
		return fmt.Errorf("push runs on the controller from the fleet repo: %w", err)
	}
	inv, err := bundle.LoadInventory(src)
	if err != nil {
		return err
	}
	targets, err := inv.Resolve(target)
	if err != nil {
		return usagef("%v", err)
	}
	for _, t := range targets {
		if t.Agent != "" {
			return usagef("agent containers get the shared layer from their image; run fleet image %s", t.Node.Name)
		}
	}
	version, err := bundle.Version(src)
	if err != nil {
		return err
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	packs := &packCache{app: a, src: src, version: version, byPlatform: map[string]*packEntry{}}
	flags := ""
	if force {
		flags += " --force"
	}
	if asJSON {
		flags += " --json"
	}
	results := make([]pushResult, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = a.pushOne(ctx, ex, packs, t, dryRun, flags)
		}()
	}
	wg.Wait()

	if asJSON {
		if err := writeJSON(a.out, results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			a.printf("── %s\n", r.Node)
			if r.Output != "" {
				a.print(r.Output)
			}
			if r.Stderr != "" {
				a.print(r.Stderr)
			}
			if r.Error != "" {
				a.printf("error: %s\n", r.Error)
			}
		}
	}
	worst := exitOK
	for _, r := range results {
		switch {
		case r.Error != "" || (r.ExitCode != 0 && r.ExitCode != exitConflict && r.ExitCode != exitDrift):
			worst = exitError
		case r.ExitCode == exitConflict && worst != exitError:
			worst = exitConflict
		case r.ExitCode == exitDrift && worst == exitOK:
			worst = exitDrift
		}
	}
	switch worst {
	case exitOK:
		return nil
	case exitDrift:
		return codeError{code: exitDrift, err: errors.New("drift found; run fleet push to converge")}
	case exitConflict:
		return codeError{code: exitConflict, err: errors.New("conflicts left untouched; see above")}
	default:
		return codeError{code: exitError, err: errors.New("some targets failed; see above")}
	}
}

func (a *App) pushOne(ctx context.Context, ex *executor, packs *packCache, t inventory.Target, dryRun bool, flags string) pushResult {
	r := pushResult{Node: t.Node.Name}
	uname, err := ex.output(ctx, t, "probe", "uname -sm", 20*time.Second)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	platform, err := bundle.ParsePlatform(uname)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	data, err := packs.get(ctx, platform)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	script, label := fmt.Sprintf(installScript, strings.TrimSpace(flags)), "push"
	if dryRun {
		script, label = fmt.Sprintf(previewScript, strings.TrimSpace(flags)), "drift"
	}
	var out, errb bytes.Buffer
	res, err := ex.run(ctx, runSpec{target: t, script: script, label: label, detail: label + " " + packs.version,
		stdin: bytes.NewReader(data), stdout: &out, stderr: &errb, timeout: 5 * time.Minute})
	r.ExitCode, r.Output, r.Stderr = res.ExitCode, out.String(), errb.String()
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

type packEntry struct {
	once sync.Once
	data []byte
	err  error
}

type packCache struct {
	app        *App
	src        string
	version    string
	mu         sync.Mutex
	byPlatform map[string]*packEntry
}

func (p *packCache) get(ctx context.Context, platform bundle.Platform) ([]byte, error) {
	p.mu.Lock()
	e, ok := p.byPlatform[platform.String()]
	if !ok {
		e = &packEntry{}
		p.byPlatform[platform.String()] = e
	}
	p.mu.Unlock()
	e.once.Do(func() {
		bin, err := bundle.Binary(ctx, p.app.Runner, p.src, platform, p.version)
		if err != nil {
			e.err = err
			return
		}
		e.data, e.err = bundle.Pack(p.src, bin, p.version)
	})
	return e.data, e.err
}

// --- adopt -----------------------------------------------------------------

func (a *App) cmdAdopt(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return usagef("usage: fleet adopt <node> <~/path>...")
	}
	src, err := a.sourceDir()
	if err != nil {
		return fmt.Errorf("adopt writes into the fleet repo: %w", err)
	}
	inv, err := bundle.LoadInventory(src)
	if err != nil {
		return err
	}
	targets, err := inv.Resolve(args[0])
	if err != nil || len(targets) != 1 {
		return usagef("adopt needs exactly one source target")
	}
	manifestData, err := os.ReadFile(filepath.Join(src, "home", "manifest.toml"))
	if err != nil {
		return err
	}
	man, err := syncer.ParseManifest(manifestData)
	if err != nil {
		return err
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	for _, path := range args[1:] {
		dest, err := adoptDestination(man, filepath.Join(src, "home"), path)
		if err != nil {
			return err
		}
		content, err := ex.output(ctx, targets[0], "adopt", `cat "$HOME"/`+shellQuote(strings.TrimPrefix(path, "~/")), 30*time.Second)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, dest)
		a.printf("adopted %s from %s into %s\n", path, targets[0], rel)
	}
	a.println("review with git diff, commit, then run: fleet push all")
	return nil
}

// adoptDestination maps a managed target path back to its file in the repo.
// Only whole-file rules can be adopted; merged files are edited in the overlay.
func adoptDestination(man *syncer.Manifest, home, target string) (string, error) {
	if !strings.HasPrefix(target, "~/") || path.Clean(target) != target || strings.Contains(target, "/../") || strings.HasSuffix(target, "/..") {
		return "", usagef("path %q must be a clean path starting with ~/", target)
	}
	for _, f := range man.Files {
		if f.Target != target {
			continue
		}
		if f.Merge != syncer.MergeReplace {
			return "", fmt.Errorf("%s is merged (%s), not owned; edit home/%s in the repo instead", target, f.Merge, f.Source)
		}
		return filepath.Join(home, f.Source), nil
	}
	var matches []string
	for _, t := range man.Trees {
		rel, ok := strings.CutPrefix(target, t.Target+"/")
		if !ok {
			continue
		}
		candidate := filepath.Join(home, t.Source, filepath.FromSlash(rel))
		if _, err := os.Stat(candidate); err == nil {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("%s is not a file in the shared layer; to add a new file, copy it into the right home/ directory", target)
	default:
		return "", fmt.Errorf("%s maps to several repo files: %s", target, strings.Join(matches, ", "))
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// --- docs ------------------------------------------------------------------

func (a *App) cmdDocs(_ context.Context, args []string) error {
	fs := newFlags("docs")
	check := fs.Bool("check", false, "fail if FLEET.md is out of date instead of writing it")
	if err := parse(fs, args); err != nil {
		return err
	}
	src, err := a.sourceDir()
	if err != nil {
		return err
	}
	inv, err := bundle.LoadInventory(src)
	if err != nil {
		return err
	}
	path := filepath.Join(src, "FLEET.md")
	want := docs.Render(inv)
	if *check {
		have, _ := os.ReadFile(path)
		if !bytes.Equal(have, want) {
			return errors.New("FLEET.md is out of date; run: fleet docs")
		}
		a.println("FLEET.md is current")
		return nil
	}
	if err := os.WriteFile(path, want, 0o644); err != nil {
		return err
	}
	a.println("wrote FLEET.md")
	return nil
}
