package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/remote"
	"github.com/vickingur/fleet-orchestrator/internal/syncer"
)

const testInventory = `
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

[[node]]
name = "mini"
host = "desktop-example"
user = "v"
os = "darwin"
role = "member"

[[tool]]
name = "sh"
version = "echo sh-1.0"
`

const tailscaleJSON = `{"BackendState":"Running",
 "Self":{"DNSName":"laptop-example.example.invalid.","OS":"macOS","TailscaleIPs":["192.0.2.10"]},
 "Peer":{"a":{"DNSName":"worker-example.example.invalid.","OS":"linux","TailscaleIPs":["192.0.2.11"],"Online":false,"LastSeen":"2026-10-02T09:00:00Z"},
         "b":{"DNSName":"agent-x.example.invalid.","OS":"linux","TailscaleIPs":["192.0.2.12"],"Online":true,"Tags":["tag:agent"]}}}`

// fakeRunner answers commands from a handler and records them.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []execx.Cmd
	handler func(c execx.Cmd) (stdout string, code int, err error)
}

func (f *fakeRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	out, code, err := f.handler(c)
	if c.Stdout != nil {
		_, _ = c.Stdout.Write([]byte(out))
	}
	return execx.Result{ExitCode: code, Duration: time.Millisecond}, err
}

func (f *fakeRunner) sshCalls() []execx.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []execx.Cmd
	for _, c := range f.calls {
		if c.Name == "ssh" {
			out = append(out, c)
		}
	}
	return out
}

type harness struct {
	app    *App
	out    *bytes.Buffer
	errb   *bytes.Buffer
	env    map[string]string
	src    string // source checkout push builds from
	cfg    string // fleet config: inventory and shared layer
	runner *fakeRunner
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	src, cfg := filepath.Join(root, "src"), filepath.Join(root, "cfg")
	for rel, body := range map[string]string{
		"src/go.mod":             "module github.com/vickingur/fleet-orchestrator\n",
		"cfg/fleet.toml":         testInventory,
		"cfg/home/manifest.toml": "",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{out: &bytes.Buffer{}, errb: &bytes.Buffer{}, src: src, cfg: cfg,
		env:    map[string]string{"FLEET_SRC": src, "FLEET_CONFIG": cfg, "FLEET_ACTOR": "tester"},
		runner: &fakeRunner{handler: func(execx.Cmd) (string, int, error) { return "", 0, nil }}}
	h.app = &App{Version: "v-test", Stdin: strings.NewReader(""), Stdout: h.out, Stderr: h.errb, Runner: h.runner,
		Getenv: func(k string) string { return h.env[k] }, Home: filepath.Join(root, "home"), Cwd: root,
		Now: func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC) }}
	t.Setenv("FLEET_ACTOR", "tester")
	return h
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.errb.Reset()
	return h.app.Run(context.Background(), args)
}

func tailscaleHandler(next func(c execx.Cmd) (string, int, error)) func(c execx.Cmd) (string, int, error) {
	return func(c execx.Cmd) (string, int, error) {
		if c.Name == "tailscale" {
			return tailscaleJSON, 0, nil
		}
		return next(c)
	}
}

func TestUsageAndDispatch(t *testing.T) {
	h := newHarness(t)
	if code := h.run(); code != exitUsage || !strings.Contains(h.out.String(), "Commands:") {
		t.Fatalf("no args: %d %q", code, h.out)
	}
	if code := h.run("help"); code != exitOK {
		t.Fatalf("help: %d", code)
	}
	if code := h.run("frobnicate"); code != exitUsage || !strings.Contains(h.errb.String(), `unknown command "frobnicate"`) {
		t.Fatalf("unknown: %d %q", code, h.errb)
	}
	if code := h.run("version"); code != exitOK || h.out.String() != "v-test\n" {
		t.Fatalf("version: %d %q", code, h.out)
	}
	if code := h.run("version", "extra"); code != exitUsage {
		t.Fatalf("version with args: %d", code)
	}
	if code := h.run("exec", "fw", "uptime"); code != exitUsage || !strings.Contains(h.errb.String(), "<target> -- <command>") {
		t.Fatalf("exec without --: %d %q", code, h.errb)
	}
	if code := h.run("exec", "nope", "--", "true"); code != exitUsage || !strings.Contains(h.errb.String(), "unknown node") {
		t.Fatalf("exec unknown node: %d %q", code, h.errb)
	}
	if code := h.run("ls", "--bogus"); code != exitUsage {
		t.Fatalf("bad flag: %d", code)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestLostOutputFailsTheCommand(t *testing.T) {
	h := newHarness(t)
	h.app.Stdout = failingWriter{}
	if code := h.run("version"); code != exitError || !strings.Contains(h.errb.String(), "writing output: broken pipe") {
		t.Fatalf("code %d, stderr %q", code, h.errb)
	}
}

func TestLs(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(nil)
	if code := h.run("ls"); code != exitOK {
		t.Fatalf("ls: %d %s", code, h.errb)
	}
	out := h.out.String()
	for _, want := range []string{"this machine", "offline, last seen 3h ago", "not on tailnet", "agent-x  online  192.0.2.12"} {
		if !strings.Contains(out, want) {
			t.Errorf("ls missing %q:\n%s", want, out)
		}
	}
	if code := h.run("ls", "--json"); code != exitOK {
		t.Fatal(code)
	}
	var parsed lsOutput
	if err := json.Unmarshal(h.out.Bytes(), &parsed); err != nil || len(parsed.Nodes) != 3 || parsed.Nodes[1].State != "offline, last seen 3h ago" {
		t.Fatalf("json = %+v, %v", parsed, err)
	}
}

func TestLsWithoutTailnetStillListsInventory(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = func(execx.Cmd) (string, int, error) { return "", 1, nil }
	if code := h.run("ls"); code != exitOK {
		t.Fatalf("ls: %d", code)
	}
	if out := h.out.String(); strings.Count(out, "unknown") != 3 || !strings.Contains(out, "tailnet status unavailable") {
		t.Fatalf("out = %s", out)
	}
}

func TestExecLocalRunsRealShellAndAudits(t *testing.T) {
	h := newHarness(t)
	h.env["FLEET_SELF"] = "mac"
	h.app.Runner = execx.OS{}
	if code := h.run("exec", "mac", "--", `printf '%s' "$0"; exit 0`); code != exitOK || h.out.String() != "sh" {
		t.Fatalf("code %d out %q err %q", code, h.out, h.errb)
	}
	if code := h.run("exec", "mac", "--", "exit 9"); code != 9 {
		t.Fatalf("exit code not propagated: %d", code)
	}
	data, err := os.ReadFile(filepath.Join(h.app.Home, ".local/state/fleet/audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a start and an end record per action, got %d lines:\n%s", len(lines), data)
	}
	var start, end struct {
		ID, Phase, Actor, Target string
		ExitCode                 int `json:"exit_code"`
	}
	if json.Unmarshal([]byte(lines[2]), &start) != nil || json.Unmarshal([]byte(lines[3]), &end) != nil {
		t.Fatalf("audit = %s", data)
	}
	if start.Phase != "start" || end.Phase != "end" || start.ID == "" || start.ID != end.ID ||
		end.Actor != "tester" || end.Target != "mac" || end.ExitCode != 9 {
		t.Fatalf("start %+v end %+v", start, end)
	}
}

func TestExecRemoteUsesHardenedSSH(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) { return "ok\n", 0, nil })
	if code := h.run("exec", "fw", "--", "uptime"); code != exitOK || h.out.String() != "ok\n" {
		t.Fatalf("code %d out %q", code, h.out)
	}
	calls := h.runner.sshCalls()
	if len(calls) != 1 {
		t.Fatalf("ssh calls = %d", len(calls))
	}
	args := strings.Join(calls[0].Args, " ")
	if !strings.Contains(args, "-l operator -- worker-example") || !strings.Contains(args, "UserKnownHostsFile="+filepath.Join(h.app.Home, ".local/state/fleet/known_hosts")) {
		t.Fatalf("ssh args = %s", args)
	}
}

func TestSSHShellPersistsOnNodesOnly(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) { return "", 0, nil })
	for _, args := range [][]string{{"ssh", "fw"}, {"ssh", "fw", "htop"}, {"ssh", "fw:builder"}} {
		if code := h.run(args...); code != exitOK {
			t.Fatalf("%v: code %d %s", args, code, h.errb)
		}
	}
	calls := h.runner.sshCalls()
	if len(calls) != 3 {
		t.Fatalf("ssh calls = %d", len(calls))
	}
	if last := calls[0].Args[len(calls[0].Args)-1]; last != remote.ShellCommand(remote.PersistentSession) {
		t.Fatalf("bare shell does not attach the persistent session: %s", last)
	}
	if calls[0].Timeout < 30*24*time.Hour {
		t.Fatalf("an interactive session is cut after %s", calls[0].Timeout)
	}
	for _, c := range calls[1:] {
		if strings.Contains(strings.Join(c.Args, " "), remote.ShellCommand(remote.PersistentSession)) {
			t.Fatalf("command or container session wrapped in tmux: %v", c.Args)
		}
	}
}

func TestExecAgentTargetRunsInsideContainer(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) { return "", 0, nil })
	if code := h.run("exec", "fw:builder", "--", "id"); code != exitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
	if code := h.run("exec", "mac:builder", "--", "id"); code != exitUsage {
		t.Fatalf("agent on runtime-less node: %d", code)
	}
	if len(h.runner.sshCalls()) != 1 {
		t.Fatal("agent exec did not go over ssh to the host")
	}
}

func TestExecSSHFailureIsExplained(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) { return "", 255, nil })
	if code := h.run("exec", "mini", "--", "true"); code != exitError || !strings.Contains(h.errb.String(), "check it is online with fleet ls") {
		t.Fatalf("code %d err %q", code, h.errb)
	}
}

func TestExecAllJSONReportsEveryTarget(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(c execx.Cmd) (string, int, error) {
		switch dest(c) {
		case "worker-example":
			return "linux\n", 0, nil
		case "desktop-example":
			return "", 255, nil
		}
		return "", 0, fmt.Errorf("unexpected %v", c.Args)
	})
	h.env["FLEET_SELF"] = "mac"
	h.app.Runner = &mixedRunner{fake: h.runner}
	code := h.run("exec", "--json", "all", "--", "uname -s")
	var rs []execResult
	if err := json.Unmarshal(h.out.Bytes(), &rs); err != nil {
		t.Fatalf("%v: %s", err, h.out)
	}
	if code != exitError || len(rs) != 3 {
		t.Fatalf("code %d results %+v", code, rs)
	}
	byTarget := map[string]execResult{}
	for _, r := range rs {
		byTarget[r.Target] = r
	}
	if byTarget["fw"].Stdout != "linux\n" || byTarget["mini"].Error == "" || byTarget["mac"].ExitCode != 0 || byTarget["mac"].Stdout == "" {
		t.Fatalf("results = %+v", byTarget)
	}
}

// mixedRunner runs local shells for real and sends everything else to the fake.
type mixedRunner struct{ fake *fakeRunner }

func (m *mixedRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	if c.Name == "sh" {
		c.Timeout = 10 * time.Second
		return execx.OS{}.Run(ctx, c)
	}
	return m.fake.Run(ctx, c)
}

func TestSelfDetectionFailsLoudWithoutTailnet(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = func(execx.Cmd) (string, int, error) { return "", 1, nil }
	if code := h.run("exec", "fw", "--", "true"); code != exitError || !strings.Contains(h.errb.String(), "set FLEET_SELF") {
		t.Fatalf("code %d err %q", code, h.errb)
	}
	h.env["FLEET_SELF"] = "ghost"
	if code := h.run("exec", "fw", "--", "true"); code != exitError || !strings.Contains(h.errb.String(), "FLEET_SELF: unknown node") {
		t.Fatalf("bad FLEET_SELF: %d %q", code, h.errb)
	}
}

// --- apply -----------------------------------------------------------------

func writeBundle(t *testing.T, dir, skill string) {
	t.Helper()
	files := map[string]string{
		"VERSION":                    "b1\n",
		"home/manifest.toml":         "[[tree]]\nsource = \"skills\"\ntarget = \"~/.claude/skills\"\n",
		"home/skills/demo/SKILL.md":  skill,
		"home/skills/demo/extra.md":  "extra\n",
		"home/skills/other/SKILL.md": "other\n",
	}
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestApplyLifecycle(t *testing.T) {
	h := newHarness(t)
	bundle := filepath.Join(t.TempDir(), "bundle")
	writeBundle(t, bundle, "v1\n")
	h.env["FLEET_BUNDLE"] = bundle

	if code := h.run("apply", "--dry-run", "--fail-on-pending"); code != exitDrift {
		t.Fatalf("dry run with pending: %d", code)
	}
	if !strings.Contains(h.out.String(), "create    ~/.claude/skills/demo/  (2 files)") || !strings.Contains(h.out.String(), "planned: 3 create") {
		t.Fatalf("plan output:\n%s", h.out)
	}
	if _, err := os.Stat(filepath.Join(h.app.Home, ".claude/skills/demo/SKILL.md")); err == nil {
		t.Fatal("dry run wrote files")
	}
	if code := h.run("apply"); code != exitOK || !strings.Contains(h.out.String(), "applied: 3 create") {
		t.Fatalf("apply: %d %s %s", code, h.out, h.errb)
	}
	if code := h.run("apply", "--dry-run", "--fail-on-pending"); code != exitOK {
		t.Fatalf("converged dry run: %d %s", code, h.out)
	}

	if err := os.WriteFile(filepath.Join(h.app.Home, ".claude/skills/demo/SKILL.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBundle(t, bundle, "v2\n")
	if code := h.run("apply"); code != exitConflict || !strings.Contains(h.errb.String(), "fleet adopt") {
		t.Fatalf("conflict: %d %q", code, h.errb)
	}
	if code := h.run("apply", "--json", "--dry-run"); code != exitOK {
		t.Fatal(code)
	}
	var out applyOutput
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil || out.Counts["conflict"] != 1 || out.Applied || out.Version != "b1" {
		t.Fatalf("json = %+v, %v", out, err)
	}
	if code := h.run("apply", "--force"); code != exitOK {
		t.Fatalf("force: %d", code)
	}
	if data, _ := os.ReadFile(filepath.Join(h.app.Home, ".claude/skills/demo/SKILL.md")); string(data) != "v2\n" {
		t.Fatalf("after force: %q", data)
	}
}

func TestApplyWithoutBundleExplainsPush(t *testing.T) {
	h := newHarness(t)
	h.env["FLEET_BUNDLE"] = filepath.Join(t.TempDir(), "missing")
	if code := h.run("apply"); code != exitError || !strings.Contains(h.errb.String(), "fleet push") {
		t.Fatalf("code %d %q", code, h.errb)
	}
}

func TestAdoptDestination(t *testing.T) {
	home := t.TempDir()
	for _, rel := range []string{"skills/shared/a/SKILL.md", "skills/claude/b/SKILL.md", "skills/claude/dup.md", "skills/shared/dup.md", "rules.txt"} {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	man, err := syncer.ParseManifest([]byte(`
[[file]]
source = "rules.txt"
target = "~/.codex/rules/fleet.rules"
merge = "replace"
[[file]]
source = "settings.json"
target = "~/.claude/settings.json"
merge = "json"
[[tree]]
source = "skills/shared"
target = "~/.claude/skills"
[[tree]]
source = "skills/claude"
target = "~/.claude/skills"
`))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"~/.claude/skills/a/SKILL.md": "skills/shared/a/SKILL.md",
		"~/.claude/skills/b/SKILL.md": "skills/claude/b/SKILL.md",
		"~/.codex/rules/fleet.rules":  "rules.txt",
	}
	for target, want := range cases {
		got, err := adoptDestination(man, home, target)
		if err != nil || got != filepath.Join(home, want) {
			t.Errorf("%s -> %s, %v; want %s", target, got, err, want)
		}
	}
	for target, wantErr := range map[string]string{
		"~/.claude/settings.json": "edit home/settings.json",
		"~/.claude/skills/new.md": "not a file in the shared layer",
		"~/.claude/skills/dup.md": "several repo files",
		"relative/path":           "clean path starting with ~/",
	} {
		if _, err := adoptDestination(man, home, target); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: err = %v, want %q", target, err, wantErr)
		}
	}
}

func TestDoctorScriptParsesRealOutput(t *testing.T) {
	h := newHarness(t)
	inv, err := h.app.inventory()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err = execx.OS{}.Run(context.Background(), execx.Cmd{Name: "sh", Args: []string{"-c", doctorScript(inv)}, Stdout: &out, Timeout: 10 * time.Second,
		Env: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	var row doctorRow
	parseDoctor(out.String(), &row)
	if row.Tools["sh"] != "sh-1.0" || row.Fleet != "missing" || row.Platform == "" {
		t.Fatalf("row = %+v from %q", row, out.String())
	}
}

func TestDoctorFlagsStaleAndUnreachable(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(c execx.Cmd) (string, int, error) {
		if dest(c) == "desktop-example" {
			return "", 255, nil
		}
		return "platform=Linux x86_64\nfleet=old\ntool:sh=sh-1.0\n", 0, nil
	})
	code := h.run("doctor")
	out := h.out.String()
	if code != exitError || !strings.Contains(out, "old (stale, want") || !strings.Contains(out, "unreachable") {
		t.Fatalf("code %d out:\n%s", code, out)
	}
	// Regression: --json used to exit 0 whatever it found.
	if code := h.run("doctor", "--json"); code != exitError {
		t.Fatalf("doctor --json with problems exited %d", code)
	}
}

func TestSpawnWiresProbeRunAndWait(t *testing.T) {
	h := newHarness(t)
	var scripts []string
	h.runner.handler = tailscaleHandler(func(c execx.Cmd) (string, int, error) {
		scripts = append(scripts, c.Args[len(c.Args)-1])
		switch len(scripts) {
		case 1:
			return "image=yes\nsecret=fleet-claude-token\n", 0, nil
		case 2:
			return "cid\n", 0, nil
		default:
			return "ready\n", 0, nil
		}
	})
	if code := h.run("spawn", "--tailnet=false", "fw", "builder"); code != exitOK {
		t.Fatalf("spawn: %d %s", code, h.errb)
	}
	out := h.out.String()
	for _, want := range []string{"agent builder is running on fw", "claude: subscription token", "fleet ssh fw:builder codex login --device-auth", "fleet exec fw:builder -- <command>"} {
		if !strings.Contains(out, want) {
			t.Errorf("spawn output missing %q:\n%s", want, out)
		}
	}
	if len(scripts) != 3 {
		t.Fatalf("expected probe, run, wait; got %d calls", len(scripts))
	}
	run := decodeRemote(t, scripts[1])
	for _, want := range []string{"exec podman 'run'", "'--cap-drop' 'all'", "'--security-opt' 'no-new-privileges'",
		"'--secret' 'fleet-claude-token,type=env,target=CLAUDE_CODE_OAUTH_TOKEN'", "'localhost/fleet-agent:latest'"} {
		if !strings.Contains(run, want) {
			t.Errorf("script sent to the host lacks %s:\n%s", want, run)
		}
	}

	scripts = nil
	if code := h.run("spawn", "fw", "builder"); code != exitError || !strings.Contains(h.errb.String(), "fleet-ts-authkey") {
		t.Fatalf("tailnet without key: %d %q", code, h.errb)
	}
	if len(scripts) != 1 {
		t.Fatal("container started despite missing tailnet key")
	}
	if code := h.run("spawn", "mac", "builder"); code != exitUsage {
		t.Fatalf("spawn on runtime-less node: %d", code)
	}
}

func TestContainersAndRm(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) {
		return `[{"Names":["fleet-agent-b"],"State":"running","Status":"Up","Labels":{"fleet.agent":"b"}}]`, 0, nil
	})
	if code := h.run("containers"); code != exitOK || !strings.Contains(h.out.String(), "fw:b") {
		t.Fatalf("containers: %d %s", code, h.out)
	}
	if code := h.run("rm", "fw", "Bad Name"); code != exitUsage {
		t.Fatalf("rm invalid: %d", code)
	}
	if code := h.run("rm", "--volumes", "fw", "b"); code != exitOK || !strings.Contains(h.out.String(), "removed fw:b") {
		t.Fatalf("rm: %d %s", code, h.errb)
	}
}

func TestHandshakeChecksToken(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 2048)
			n, _ := conn.Read(buf)
			status := "401 Unauthorized"
			if strings.Contains(string(buf[:n]), "Authorization: Bearer secret") {
				status = "101 Switching Protocols"
			}
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 %s\r\n\r\n", status)
			_ = conn.Close()
		}
	}()
	addr := l.Addr().String()
	if s, err := waitForUpgrade(context.Background(), addr, "secret", 5*time.Second); err != nil || s != 101 {
		t.Fatalf("authorized: %d %v", s, err)
	}
	if s, err := handshake(addr, ""); err != nil || s != 401 {
		t.Fatalf("anonymous: %d %v", s, err)
	}
	if _, err := waitForUpgrade(context.Background(), "127.0.0.1:1", "x", 700*time.Millisecond); err == nil {
		t.Fatal("closed port reported ready")
	}
}

func TestPrintPlanGroupsNewDirectories(t *testing.T) {
	var buf bytes.Buffer
	o := &output{w: &buf}
	plan := syncer.Plan{Changes: []syncer.Change{
		{Target: "~/.claude/skills/a/SKILL.md", Action: syncer.Create, Rule: "tree:skills->~/.claude/skills"},
		{Target: "~/.claude/skills/a/ref.md", Action: syncer.Create, Rule: "tree:skills->~/.claude/skills"},
		{Target: "~/.claude/skills/top.md", Action: syncer.Create, Rule: "tree:skills->~/.claude/skills"},
		{Target: "~/.claude/settings.json", Action: syncer.Merge, Detail: "+env"},
		{Target: "~/.claude/CLAUDE.md", Action: syncer.Unchanged},
	}}
	printPlan(o, plan, true)
	want := "create    ~/.claude/skills/a/  (2 files)\ncreate    ~/.claude/skills/top.md\nmerge     ~/.claude/settings.json  (+env)\nplanned: 3 create, 1 merge, 1 ok\n"
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{30 * time.Second: "just now", 5 * time.Minute: "5m ago", 7 * time.Hour: "7h ago", 72 * time.Hour: "3d ago"} {
		if got := ago(now.Add(-d), now); got != want {
			t.Errorf("%s: %s, want %s", d, got, want)
		}
	}
}

func TestApplyEnsures(t *testing.T) {
	h := newHarness(t)
	h.app.Runner = execx.OS{}
	bundle := filepath.Join(t.TempDir(), "bundle")
	writeBundle(t, bundle, "v1\n")
	mark := filepath.Join(t.TempDir(), "installed")
	manifest := fmt.Sprintf(`[[ensure]]
name = "demo installed"
check = "test -f %[1]s"
fix = "touch %[1]s"
`, mark)
	if err := os.WriteFile(filepath.Join(bundle, "home/manifest.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	h.env["FLEET_BUNDLE"] = bundle

	if code := h.run("apply", "--dry-run", "--fail-on-pending"); code != exitDrift || !strings.Contains(h.out.String(), "ensure    demo installed") {
		t.Fatalf("dry run: %d\n%s", code, h.out)
	}
	if _, err := os.Stat(mark); err == nil {
		t.Fatal("dry run ran the fix")
	}
	if code := h.run("apply"); code != exitOK || !strings.Contains(h.out.String(), "ensured   demo installed") {
		t.Fatalf("apply: %d\n%s%s", code, h.out, h.errb)
	}
	if code := h.run("apply", "--dry-run", "--fail-on-pending"); code != exitOK || !strings.Contains(h.out.String(), "ensures: 1 of 1 already met") {
		t.Fatalf("converged: %d\n%s", code, h.out)
	}

	broken := `[[ensure]]
name = "never satisfied"
check = "false"
fix = "true"
`
	if err := os.WriteFile(filepath.Join(bundle, "home/manifest.toml"), []byte(broken), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := h.run("apply"); code != exitError || !strings.Contains(h.errb.String(), "fix ran but the check still fails") {
		t.Fatalf("broken fix: %d %q", code, h.errb)
	}
}

// Regression: a failed write next to a conflict exited 3 ("conflict") and
// never printed the failure, so push reported a mere conflict.
func TestApplyFailureOutranksConflict(t *testing.T) {
	h := newHarness(t)
	bundle := filepath.Join(t.TempDir(), "bundle")
	writeBundle(t, bundle, "v1\n")
	h.env["FLEET_BUNDLE"] = bundle
	skills := filepath.Join(h.app.Home, ".claude/skills")
	if err := os.MkdirAll(filepath.Join(skills, "demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "demo/SKILL.md"), []byte("not fleet's\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(skills, "other"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(skills, "other"), 0o755) })
	code := h.run("apply")
	if code != exitError {
		t.Fatalf("exit %d, want %d (a failure outranks a conflict)\n%s%s", code, exitError, h.out, h.errb)
	}
	if !strings.Contains(h.out.String(), "failed    ~/.claude/skills/other/SKILL.md") ||
		!strings.Contains(h.errb.String(), "permission denied") || !strings.Contains(h.errb.String(), "1 conflict(s)") {
		t.Fatalf("out:\n%s\nerr:\n%s", h.out, h.errb)
	}
}

// dest is the host an ssh invocation connects to (the argument after "--").
func dest(c execx.Cmd) string { return c.Args[len(c.Args)-2] }

func TestContainersJSONFailsWhenAHostFails(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) { return "", 255, nil })
	if code := h.run("containers", "--json"); code != exitError {
		t.Fatalf("containers --json with an unreachable host exited %d", code)
	}
}

func TestAdoptRejectsTraversal(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(execx.Cmd) (string, int, error) { return "secret", 0, nil })
	if err := os.MkdirAll(filepath.Join(h.cfg, "home"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cfg, "home/manifest.toml"), []byte("[[tree]]\nsource = \"skills\"\ntarget = \"~/.claude/skills\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code := h.run("adopt", "fw", "~/.claude/skills/../../../fleet.toml")
	if code != exitUsage || !strings.Contains(h.errb.String(), "clean path") {
		t.Fatalf("code %d err %q", code, h.errb)
	}
	if len(h.runner.sshCalls()) != 0 {
		t.Fatal("adopt read a remote file before validating the path")
	}
}

// An unquoted ~ reaches fleet already expanded to this machine's home; adopt
// reads the same path under the node's home and writes it into the config.
func TestAdoptAcceptsAShellExpandedPath(t *testing.T) {
	h := newHarness(t)
	h.runner.handler = tailscaleHandler(func(c execx.Cmd) (string, int, error) {
		if c.Name == "ssh" && strings.Contains(decodeRemote(t, strings.Join(c.Args, " ")), `cat "$HOME"/'.claude/skills/demo/SKILL.md'`) {
			return "node copy\n", 0, nil
		}
		return "", 1, fmt.Errorf("unexpected %s %v", c.Name, c.Args)
	})
	if err := os.WriteFile(filepath.Join(h.cfg, "home/manifest.toml"), []byte("[[tree]]\nsource = \"skills\"\ntarget = \"~/.claude/skills\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.cfg, "home/skills/demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.cfg, "home/skills/demo/SKILL.md"), []byte("config copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := h.run("adopt", "fw", filepath.Join(h.app.Home, ".claude/skills/demo/SKILL.md")); code != 0 {
		t.Fatalf("exit %d: %s", code, h.errb)
	}
	got, err := os.ReadFile(filepath.Join(h.cfg, "home/skills/demo/SKILL.md"))
	if err != nil || string(got) != "node copy\n" {
		t.Fatalf("adopted %q, %v", got, err)
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("é", 300) // 2 bytes each
	got := truncate(s, 501)
	if !utf8.ValidString(got) || len(got) > 501+len("…") {
		t.Fatalf("truncate produced invalid or long output (%d bytes)", len(got))
	}
	if truncate("short", 500) != "short" {
		t.Fatal("short strings must pass through")
	}
}

// decodeRemote recovers the script carried by a remote.ShellCommand line, so
// tests assert on what actually runs on the node.
func decodeRemote(t *testing.T, line string) string {
	t.Helper()
	_, rest, ok := strings.Cut(line, "printf %s ")
	if !ok {
		t.Fatalf("not a fleet shell command: %s", line)
	}
	encoded, _, _ := strings.Cut(rest, " ")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestExecHelpListsItsFlags(t *testing.T) {
	h := newHarness(t)
	h.run("exec", "--help")
	for _, flag := range []string{"-timeout", "-detach", "-json"} {
		if !strings.Contains(h.errb.String(), flag) {
			t.Fatalf("exec --help does not list %s: %q", flag, h.errb)
		}
	}
}

func TestExecDetachReturnsAtOnceAndLogs(t *testing.T) {
	h := newHarness(t)
	h.env["FLEET_SELF"] = "mac"
	h.app.Runner = execx.OS{}
	t.Setenv("HOME", h.app.Home)
	start := time.Now()
	if code := h.run("exec", "--detach", "mac", "--", "sleep 2; echo finished"); code != exitOK {
		t.Fatalf("code %d err %q", code, h.errb)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("--detach waited for the job: %v", took)
	}
	log := strings.TrimSpace(h.out.String())
	if !strings.HasSuffix(log, ".log") {
		t.Fatalf("--detach should print its log path, got %q", log)
	}
	time.Sleep(3 * time.Second)
	data, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(data)) != "finished" {
		t.Fatalf("the detached job did not run to the end: %q %v", data, err)
	}
}

func TestExecDetachRefusesFanOut(t *testing.T) {
	h := newHarness(t)
	if code := h.run("exec", "--detach", "all", "--", "true"); code != exitUsage {
		t.Fatalf("--detach on several targets: %d", code)
	}
}

// The explicit local identity must bypass discovery and preserve ordinary SSH
// destinations, including DNS names that are not MagicDNS short names.
func TestOrdinarySSHWithExplicitLocalNodeDoesNotUseTailscale(t *testing.T) {
	for _, host := range []string{"worker.example.invalid", "192.0.2.10"} {
		t.Run(host, func(t *testing.T) {
			h := newHarness(t)
			h.env["FLEET_SELF"] = "mac"
			data := strings.ReplaceAll(testInventory, "worker-example", host)
			if err := os.WriteFile(filepath.Join(h.cfg, "fleet.toml"), []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			h.runner.handler = func(c execx.Cmd) (string, int, error) {
				if c.Name != "ssh" {
					return "", 1, fmt.Errorf("unexpected command %s; no discovery is available", c.Name)
				}
				return "ordinary ssh\n", 0, nil
			}
			if code := h.run("exec", "fw", "--", "uname -sm"); code != 0 || h.out.String() != "ordinary ssh\n" {
				t.Fatalf("exit %d: %s %s", code, h.out, h.errb)
			}
			calls := h.runner.sshCalls()
			if len(calls) != 1 || !strings.Contains(strings.Join(calls[0].Args, " "), " -- "+host+" ") {
				t.Fatalf("expected one SSH call to %s, got %v", host, calls)
			}
		})
	}
}
