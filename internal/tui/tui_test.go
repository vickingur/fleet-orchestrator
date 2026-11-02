package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/vickingur/fleet-orchestrator/internal/audit"
	"github.com/vickingur/fleet-orchestrator/internal/cli"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

type fake struct {
	mu         sync.Mutex
	controller bool
	toolsOK    bool // otherwise codex is reported missing everywhere
	containers []Container
	calls      []string
	prefs      map[string]string
	delegated  []string // agent + "\n" + prompt per launch
	noAgent    bool
}

func (f *fake) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fake) called(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

var testFleet = Fleet{Nodes: []Node{
	{Name: "mac", Host: "laptop-example", OS: "darwin", Role: "controller", State: "this machine", Address: "192.0.2.10"},
	{Name: "fw", Host: "worker-example", OS: "linux", Role: "member", State: "online", Address: "192.0.2.11", Runtime: "podman"},
	{Name: "mini", Host: "desktop-example", OS: "darwin", Role: "member", State: "not on tailnet"},
}}

func (f *fake) Fleet(context.Context) (Fleet, error) { f.record("fleet"); return testFleet, nil }
func (f *fake) Health(_ context.Context, node string) (Health, error) {
	f.record("health " + node)
	codex := "missing"
	if f.toolsOK {
		codex = "0.160.0"
	}
	return Health{Target: node, Reach: "ok", Platform: "Linux x86_64", Fleet: "v1", Current: node != "fw",
		Tools: map[string]string{"claude": "2.1.280", "codex": codex}}, nil
}
func (f *fake) Containers(context.Context) ([]Container, error) {
	f.record("containers")
	return f.containers, nil
}
func (f *fake) Drift(_ context.Context, nodes []string) ([]Sync, error) {
	f.record("drift " + strings.Join(nodes, ","))
	var out []Sync
	for _, n := range nodes {
		p := &Plan{Counts: map[string]int{"ok": 97}, Ensures: []Ensure{{Name: "e", Satisfied: true}}}
		if n == "fw" {
			p.Counts["conflict"] = 2
			p.Changes = []Change{{Target: "~/.claude/x", Action: "conflict", Detail: "edited locally"}}
		}
		out = append(out, Sync{Node: n, Plan: p})
	}
	return out, nil
}
func (f *fake) Push(_ context.Context, node string, force bool) (Sync, error) {
	f.record(fmt.Sprintf("push %s force=%t", node, force))
	return Sync{Node: node, Plan: &Plan{Counts: map[string]int{"ok": 97}}}, nil
}
func (f *fake) Adopt(_ context.Context, node string, paths []string) (string, error) {
	f.record("adopt " + node + " " + strings.Join(paths, " "))
	return "adopted", nil
}
func (f *fake) Exec(_ context.Context, target, command string) (RunResult, error) {
	f.record("exec " + target + " " + command)
	return RunResult{Target: target, Stdout: "hello from " + target + "\n", Millis: 12}, nil
}
func (f *fake) Spawn(_ context.Context, node, agent string, tailnet bool) (string, error) {
	f.record(fmt.Sprintf("spawn %s %s tailnet=%t", node, agent, tailnet))
	return "agent " + agent + " is running", nil
}
func (f *fake) Remove(_ context.Context, node, agent string) (string, error) {
	f.record("remove " + node + " " + agent)
	f.mu.Lock()
	f.containers = nil
	f.mu.Unlock()
	return "removed " + node + ":" + agent, nil
}
func (f *fake) Activity(int) ([]audit.Record, error) {
	return []audit.Record{{Time: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC), Actor: "codex", Command: "exec", Target: "fw", ExitCode: 255, Millis: 10, Detail: "uptime"}}, nil
}
func (f *fake) Guide() (string, error) { return "# Fleet\n\nRules for agents.\n", nil }
func (f *fake) IsController() bool     { return f.controller }
func (f *fake) Delegate(agent, prompt string) (*exec.Cmd, error) {
	if f.noAgent {
		return nil, errors.New(agent + " is not installed on this machine")
	}
	f.mu.Lock()
	f.delegated = append(f.delegated, agent+"\n"+prompt)
	f.mu.Unlock()
	return exec.Command("true"), nil
}
func (f *fake) Preference(key string) string { return f.prefs[key] }
func (f *fake) SetPreference(key, value string) error {
	if f.prefs == nil {
		f.prefs = map[string]string{}
	}
	f.prefs[key] = value
	return nil
}
func (f *fake) Shell(target string) (*exec.Cmd, error) {
	f.record("shell " + target)
	return exec.Command("true"), nil
}

// drive applies messages and runs the commands they return, following batches
// but never timers, until the model settles.
func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	queue := append([]tea.Msg(nil), msgs...)
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 200 {
			t.Fatal("model did not settle")
		}
		msg := queue[0]
		queue = queue[1:]
		next, cmd := m.Update(msg)
		m = next.(Model)
		queue = append(queue, run(cmd)...)
	}
	return m
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		switch msg := msg.(type) {
		case tea.BatchMsg:
			var out []tea.Msg
			for _, c := range msg {
				out = append(out, run(c)...)
			}
			return out
		case nil:
			return nil
		default:
			return []tea.Msg{msg}
		}
	case <-time.After(100 * time.Millisecond):
		return nil // a timer (spinner, flavor) or a blink: not part of the settle
	}
}

func key(k string) tea.Msg {
	switch k {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

func typed(s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return out
}

func screen(m Model) string { return ansi.Strip(m.render()) }

func start(t *testing.T, f *fake) Model {
	t.Helper()
	m := New(context.Background(), f)
	return drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30}, m.loadFleet()(), m.loadContainers()(), m.loadActivity()(), m.loadGuide()())
}

func TestLoadsProgressivelyAndShowsEveryState(t *testing.T) {
	f := &fake{controller: true}
	m := New(context.Background(), f)
	m = drive(t, m, tea.WindowSizeMsg{Width: 110, Height: 30})
	if s := screen(m); !strings.Contains(s, "…") || !strings.Contains(s, "fleet") {
		t.Fatalf("no loading state:\n%s", s)
	}
	m = drive(t, m, m.loadFleet()(), m.loadContainers()())
	if !f.called("health mac") || !f.called("health fw") || f.called("health mini") {
		t.Fatalf("health must load for reachable nodes only: %v", f.calls)
	}
	if !f.called("drift mac,fw") {
		t.Fatalf("drift must run once for reachable nodes: %v", f.calls)
	}
	s := screen(m)
	for _, want := range []string{"2 of 3 online", "› ● mac", "this machine", "v1 ✓ current", "in sync · 97 files · 1/1 ensures", "not on tailnet"} {
		if !strings.Contains(s, want) {
			t.Errorf("screen lacks %q:\n%s", want, s)
		}
	}
	m = drive(t, m, key("j"))
	s = screen(m)
	for _, want := range []string{"› ● fw", "stale, press p to push", "missing", "2 conflict(s) · press d", "none yet · press n"} {
		if !strings.Contains(s, want) {
			t.Errorf("fw detail lacks %q:\n%s", want, s)
		}
	}
	m = drive(t, m, key("j"))
	if s := screen(m); !strings.Contains(s, "Not on the tailnet. Run tailscale up") {
		t.Fatalf("unreachable node detail:\n%s", s)
	}
}

func TestRunCommandFlow(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("j"), key("x"))
	if s := screen(m); !strings.Contains(s, "run on fw ›") {
		t.Fatalf("prompt not shown:\n%s", s)
	}
	m = drive(t, m, append(typed("uptime"), key("enter"))...)
	if !f.called("exec fw uptime") {
		t.Fatalf("exec not called: %v", f.calls)
	}
	if s := screen(m); !strings.Contains(s, "hello from fw") || !strings.Contains(s, "exit 0 · 12ms") {
		t.Fatalf("output not shown:\n%s", s)
	}
	m = drive(t, m, key("esc"))
	if m.mode != modeBrowse {
		t.Fatal("esc did not leave the output")
	}
	m = drive(t, m, key("x"), key("esc"))
	if m.mode != modeBrowse || strings.Count(strings.Join(f.calls, "|"), "exec") != 1 {
		t.Fatal("esc in the prompt must cancel without running")
	}
}

func TestChangesNeedConfirmation(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("j"), key("p"))
	if s := screen(m); !strings.Contains(s, "Push the shared layer to fw?") {
		t.Fatalf("no confirmation:\n%s", s)
	}
	m = drive(t, m, key("n"))
	if f.called("push") {
		t.Fatal("pushed without a yes")
	}
	drive(t, m, key("p"), key("y"))
	if !f.called("push fw force=false") {
		t.Fatal("yes did not push")
	}
}

func TestDiffShowsThePlan(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("j"), key("d"))
	if s := screen(m); !strings.Contains(s, "what a push would change on fw") || !strings.Contains(s, "conflict  ~/.claude/x") {
		t.Fatalf("diff view:\n%s", s)
	}
	m = drive(t, m, key("tab"))
	if m.mode != modeBrowse || m.tab != tabActivity {
		t.Fatal("tab from a finished output should switch views")
	}
}

func TestSelectionFollowsRemovedAgentToItsHost(t *testing.T) {
	f := &fake{controller: true, containers: []Container{{Node: "fw", Agent: "builder", State: "running", Status: "Up"}}}
	m := start(t, f)
	m = drive(t, m, key("j"), key("j"))
	if it, _ := m.selected(); it.target() != "fw:builder" {
		t.Fatalf("selected %q", it.target())
	}
	m = drive(t, m, key("D"), key("y"))
	if !f.called("remove fw builder") {
		t.Fatalf("remove not called: %v", f.calls)
	}
	if it, _ := m.selected(); it.target() != "fw" {
		t.Fatalf("after removal selected %q, want its host", it.target())
	}
	if s := screen(m); !strings.Contains(s, "volumes are kept") {
		t.Fatalf("removal should say the volumes survive:\n%s", s)
	}
}

func TestMemberNodeHasNoPushOrDiff(t *testing.T) {
	f := &fake{controller: false}
	m := start(t, f)
	if f.called("drift") {
		t.Fatal("drift ran away from the controller")
	}
	m = drive(t, m, key("p"))
	if m.mode == modeConfirm || !strings.Contains(screen(m), "push runs on the controller") {
		t.Fatalf("push offered on a member:\n%s", screen(m))
	}
	if strings.Contains(screen(m), "d diff") {
		t.Fatal("diff hint shown on a member")
	}
}

func TestSpawnAndShellRouting(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("n"))
	if strings.Contains(screen(m), "new agent") || m.mode != modeBrowse {
		t.Fatal("spawn offered on a node without a runtime")
	}
	m = drive(t, m, key("j"), key("n"))
	m = drive(t, m, append(typed("demo"), key("enter"))...)
	if !f.called("spawn fw demo tailnet=false") {
		t.Fatalf("spawn must default to tailnet off: %v", f.calls)
	}
	m = drive(t, m, key("esc"), key("n"), key("tab"))
	m = drive(t, m, append(typed("demo2"), key("enter"))...)
	if !f.called("spawn fw demo2 tailnet=true") {
		t.Fatalf("spawn not called: %v", f.calls)
	}
	m = drive(t, m, key("esc"), key("j"), key("enter"))
	if f.called("shell mini") || !strings.Contains(screen(m), "not on the tailnet") {
		t.Fatal("shell attempted on an unreachable node")
	}
}

func TestTabsAndTinyTerminals(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("2"))
	if s := screen(m); !strings.Contains(s, "codex") || !strings.Contains(s, "exit 255") || strings.Contains(s, "exit 25510") {
		t.Fatalf("activity feed:\n%s", s)
	}
	m = drive(t, m, key("3"))
	if !strings.Contains(screen(m), "Rules for agents") {
		t.Fatalf("guide:\n%s", screen(m))
	}
	for _, size := range [][2]int{{60, 20}, {20, 6}, {1, 1}} {
		m = drive(t, m, key("1"), tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		s := screen(m)
		lines := strings.Split(s, "\n")
		if len(lines) != max(size[1]-4, 1)+4 {
			t.Fatalf("%v: %d lines", size, len(lines))
		}
		for _, l := range lines {
			if ansi.StringWidth(l) > size[0] {
				t.Fatalf("%v: line wider than the terminal: %q", size, l)
			}
		}
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 60, Height: 20})
	if s := screen(m); !strings.Contains(s, "1/3") {
		t.Fatalf("narrow layout should show a position selector:\n%s", s)
	}
}

// --- contract with the real CLI ---------------------------------------------------

type scripted struct {
	handler func(c execx.Cmd) (string, int)
}

func (s scripted) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	out, code := s.handler(c)
	if c.Stdout != nil {
		_, _ = c.Stdout.Write([]byte(out))
	}
	return execx.Result{ExitCode: code}, nil
}

func TestBackendParsesRealCLIOutput(t *testing.T) {
	src := t.TempDir()
	inv := `
[[node]]
name = "mac"
host = "laptop-example"
user = "v"
os = "darwin"
role = "controller"

[[node]]
name = "fw"
host = "worker-example"
user = "operator"
os = "linux"
role = "member"
runtime = "podman"
`
	writeConfig(t, src, inv)
	checkout := t.TempDir()
	if err := os.WriteFile(filepath.Join(checkout, "go.mod"), []byte("module github.com/vickingur/fleet-orchestrator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FLEET_CONFIG": src, "FLEET_SRC": checkout, "FLEET_SELF": "mac"}
	app := &cli.App{Version: "t", Getenv: func(k string) string { return env[k] }, Home: t.TempDir(), Cwd: src, Now: time.Now,
		Runner: scripted{func(c execx.Cmd) (string, int) {
			switch c.Name {
			case "tailscale":
				return `{"BackendState":"Running","Self":{"DNSName":"laptop-example.x.","TailscaleIPs":["192.0.2.10"]},
					"Peer":{"a":{"DNSName":"worker-example.x.","Online":true,"TailscaleIPs":["192.0.2.11"]}}}`, 0
			case "ssh":
				return `[{"Names":["fleet-agent-b"],"State":"running","Status":"Up 1 minute","Image":"img","Labels":{"fleet.agent":"b"}}]`, 0
			}
			return "", 0
		}}}
	b := CLI{App: app}
	f, err := b.Fleet(context.Background())
	if err != nil || len(f.Nodes) != 2 || f.Nodes[0].State != "this machine" || f.Nodes[1].Runtime != "podman" || f.Nodes[1].Address != "192.0.2.11" {
		t.Fatalf("fleet = %+v, %v", f, err)
	}
	cs, err := b.Containers(context.Background())
	if err != nil || len(cs) != 1 || cs[0].Node != "fw" || cs[0].Agent != "b" || cs[0].State != "running" || cs[0].Image != "img" {
		t.Fatalf("containers = %+v, %v", cs, err)
	}
	if !b.IsController() {
		t.Fatal("controller not detected from its config and source checkout")
	}
	if _, err := (CLI{App: &cli.App{Getenv: func(string) string { return "" }, Home: t.TempDir(), Cwd: t.TempDir(), Now: time.Now,
		Runner: scripted{func(execx.Cmd) (string, int) { return "", 1 }}}}).Fleet(context.Background()); err == nil {
		t.Fatal("a failing command must surface as an error, not an empty fleet")
	} else if errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestResolveConflictsFromTheDiff(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("j"), key("d"))
	if s := screen(m); !strings.Contains(s, "f take shared version") || !strings.Contains(s, "a keep fw's version") {
		t.Fatalf("no resolution keys on a conflicting diff:\n%s", s)
	}
	m = drive(t, m, key("f"))
	if s := screen(m); !strings.Contains(s, "Overwrite 1 edited file(s) on fw with the shared version?") {
		t.Fatalf("no confirmation:\n%s", s)
	}
	m = drive(t, m, key("n"))
	if f.called("push") || m.mode != modeOutput {
		t.Fatal("no must cancel and return to the diff")
	}
	m = drive(t, m, key("f"), key("y"))
	if !f.called("push fw force=true") {
		t.Fatalf("take shared did not force-push: %v", f.calls)
	}
	m = drive(t, m, key("esc"), key("d"), key("a"), key("y"))
	if !f.called("adopt fw ~/.claude/x") {
		t.Fatalf("keep did not adopt the conflicting paths: %v", f.calls)
	}
	if !strings.Contains(screen(m), "commit, then push to the other machines") {
		t.Fatalf("adopt should explain the next step:\n%s", screen(m))
	}
	m = drive(t, m, key("esc"), key("k"), key("d"))
	if s := screen(m); strings.Contains(s, "take shared version") {
		t.Fatalf("resolution keys shown for a node without conflicts:\n%s", s)
	}
}

func TestProblemsAreDetectedFromLoadedState(t *testing.T) {
	f := &fake{controller: true, containers: []Container{{Node: "fw", Agent: "old", State: "exited", Status: "Exited (1)"}}}
	m := start(t, f)
	got := strings.Join(m.problems("fw"), " | ")
	for _, want := range []string{"older than the repo's", "codex is not installed or not on PATH",
		"conflict on ~/.claude/x (edited locally)", "agent container old is exited (Exited (1))"} {
		if !strings.Contains(got, want) {
			t.Errorf("fw problems lack %q: %s", want, got)
		}
	}
	if got := m.problems("mini"); len(got) != 1 || !strings.Contains(got[0], "not on the tailnet") {
		t.Errorf("mini problems = %v", got)
	}
	clean := start(t, &fake{controller: true, toolsOK: true})
	if got := clean.problems("mac"); len(got) != 0 {
		t.Errorf("a healthy, in-sync node reports problems: %v", got)
	}
	if s := screen(m); !strings.Contains(s, "F fix with an agent") {
		t.Errorf("fix action not offered:\n%s", s)
	}
	m = drive(t, m, key("j"))
	if s := screen(m); !strings.Contains(s, "4 problem(s)") || !strings.Contains(s, "· conflict on ~/.claude/x") {
		t.Errorf("problems not listed in the detail pane:\n%s", s)
	}
}

func TestDelegateLaunchesTheChosenAgentWithEvidence(t *testing.T) {
	f := &fake{controller: true}
	m := start(t, f)
	m = drive(t, m, key("j"), key("F"))
	if s := screen(m); !strings.Contains(s, "fix fw with Claude ›") || !strings.Contains(s, "problem(s) detected") {
		t.Fatalf("delegate prompt:\n%s", s)
	}
	m = drive(t, m, append(typed("also bump codex"), key("enter"))...)
	if len(f.delegated) != 1 {
		t.Fatalf("launches = %d", len(f.delegated))
	}
	agent, prompt, _ := strings.Cut(f.delegated[0], "\n")
	if agent != "claude" {
		t.Fatalf("agent = %s", agent)
	}
	for _, want := range []string{"Fix fleet problems on fw.", "Use the fleet skill", "fleet config on the controller",
		"Ask me before anything destructive", "fleet doctor and fleet drift", "- node fw: linux, member, online, 192.0.2.11, containers via podman",
		"- conflict on ~/.claude/x (edited locally)", "I also want: also bump codex"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, prompt)
		}
	}
	if f.prefs["delegate"] != "claude" {
		t.Fatal("agent choice not remembered")
	}
	// Coming back refreshes, so the user sees what the agent changed.
	before := strings.Count(strings.Join(f.calls, "|"), "fleet")
	m = drive(t, m, delegateDoneMsg{agent: "claude"})
	if strings.Count(strings.Join(f.calls, "|"), "fleet") <= before || !strings.Contains(screen(m), "back from Claude") {
		t.Fatal("returning from the agent did not refresh")
	}

	m = drive(t, m, key("F"), key("tab"))
	if !strings.Contains(screen(m), "fix fw with Codex") {
		t.Fatalf("tab did not switch agent:\n%s", screen(m))
	}
	drive(t, m, key("enter"))
	if agent, _, _ := strings.Cut(f.delegated[1], "\n"); agent != "codex" || f.prefs["delegate"] != "codex" {
		t.Fatalf("second launch %q, pref %q", agent, f.prefs["delegate"])
	}
	again := start(t, f)
	again = drive(t, again, key("F"))
	if !strings.Contains(screen(again), "with Codex") {
		t.Fatal("a new session does not default to the last agent")
	}
}

func TestDelegateGuards(t *testing.T) {
	f := &fake{controller: true, toolsOK: true}
	m := start(t, f)
	m = drive(t, m, key("F"), key("enter"))
	if len(f.delegated) != 0 || m.mode != modePrompt || !strings.Contains(screen(m), "nothing detected on mac") {
		t.Fatalf("launched with nothing to do:\n%s", screen(m))
	}
	f.noAgent = true
	m = drive(t, m, key("esc"), key("F"))
	m = drive(t, m, append(typed("check disk space"), key("enter"))...)
	if !strings.Contains(screen(m), "claude is not installed on this machine") {
		t.Fatalf("missing agent not explained:\n%s", screen(m))
	}
	off := start(t, &fake{controller: false})
	if p := off.delegatePrompt("mac", "", nil, false); !strings.Contains(p, "not the controller") || !strings.Contains(p, "no problems detected") {
		t.Fatalf("member prompt:\n%s", p)
	}
}

// writeConfig makes dir a fleet config holding inv and an empty shared layer.
func writeConfig(t *testing.T, dir, inv string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"fleet.toml": inv, "home/manifest.toml": ""} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Delegated fixes run in the config, where the files they change live.
func TestCLIDelegateAndPreferences(t *testing.T) {
	src := t.TempDir()
	writeConfig(t, src, "x")
	home := t.TempDir()
	env := map[string]string{"FLEET_CONFIG": src}
	b := CLI{App: &cli.App{Getenv: func(k string) string { return env[k] }, Home: home, Cwd: home, Now: time.Now}}
	if _, err := b.Delegate("gemini", "x"); err == nil {
		t.Fatal("unknown agent accepted")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	cmd, err := b.Delegate("claude", "fix it")
	if err != nil || cmd.Dir != src || cmd.Args[len(cmd.Args)-1] != "fix it" {
		t.Fatalf("cmd = %+v, err = %v", cmd, err)
	}
	if _, err := b.Delegate("codex", "x"); err == nil || !strings.Contains(err.Error(), "codex is not installed") {
		t.Fatalf("missing codex: %v", err)
	}
	if b.Preference("delegate") != "" {
		t.Fatal("preference set before anything was stored")
	}
	if err := b.SetPreference("delegate", "codex"); err != nil || b.Preference("delegate") != "codex" {
		t.Fatalf("preference round trip failed: %v", err)
	}
	if err := os.WriteFile(b.prefsPath(), []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.Preference("delegate") != "" || b.SetPreference("delegate", "claude") != nil || b.Preference("delegate") != "claude" {
		t.Fatal("a corrupt preferences file must be survivable")
	}
}
