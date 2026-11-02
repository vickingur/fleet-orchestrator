package configcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vickingur/fleet-orchestrator/internal/docs"
	"github.com/vickingur/fleet-orchestrator/internal/inventory"
)

func TestExampleConfigIsValid(t *testing.T) {
	if problems := Run(filepath.Join("..", "..", "examples", "config")); len(problems) != 0 {
		t.Fatalf("examples/config: %v", problems)
	}
}

const validInventory = `
[[node]]
name = "laptop"
host = "laptop.example.invalid"
user = "operator"
os = "darwin"
role = "controller"
`

const validManifest = `
[[file]]
source = "FLEET.md"
target = "~/.config/fleet/FLEET.md"
merge = "replace"

[[file]]
source = "claude-settings.json"
target = "~/.claude/settings.json"
merge = "json"

[[file]]
source = "fleet.rules"
target = "~/.codex/rules/fleet.rules"
merge = "replace"

[[tree]]
source = "skills"
target = "~/.claude/skills"

[[ensure]]
name = "marketplace"
check = "claude plugin marketplace list | grep -q example-market"
fix = "claude plugin marketplace add example/market"

[[ensure]]
name = "plugin"
check = "false"
fix = "claude plugin install tool@example-market"
`

const validPolicy = `{
	"tagOwners": {"tag:agent": ["autogroup:admin"]},
	"grants": [
		{"src": ["autogroup:member"], "dst": ["*"], "ip": ["*"]},
		{"src": ["tag:agent"], "dst": ["tag:agent"], "ip": ["*"]},
	],
	"ssh": [
		{"action": "check", "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["root"], "checkPeriod": "12h"},
	],
	"tests": [{"src": "tag:agent", "deny": ["192.0.2.13:22"]}],
}`

// writeConfig lays out a valid config, then applies the case's change.
func writeConfig(t *testing.T, change func(dir string)) string {
	t.Helper()
	dir := t.TempDir()
	inv, err := inventory.Parse([]byte(validInventory))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"fleet.toml":                  validInventory,
		"FLEET.md":                    string(docs.Render(inv)),
		"home/manifest.toml":          validManifest,
		"home/claude-settings.json":   `{"enabledPlugins": {"tool@example-market": true}, "permissions": {"allow": ["Bash(fleet ls)", "Bash(fleet version)"]}}`,
		"home/fleet.rules":            `prefix_rule(pattern=["fleet", "ls"], decision="allow")` + "\n",
		"home/skills/demo/SKILL.md":   "demo\n",
		"policy/tailnet.hujson":       validPolicy,
		"home/skills/demo/.DS_Store":  "litter fleet never ships",
		"home/skills/demo/x/x.pyc":    "litter fleet never ships",
		"home/terminal/x.terminfo.md": "not a terminfo source",
	}
	for rel, body := range files {
		write(t, dir, rel, body)
	}
	if change != nil {
		change(dir)
	}
	return dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidConfigHasNoProblems(t *testing.T) {
	if problems := Run(writeConfig(t, nil)); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestEachCheckReportsItsProblem(t *testing.T) {
	cases := []struct {
		name, check, detail string
		change              func(dir string)
	}{
		{"unparsable inventory", "inventory", "fleet.toml", func(d string) { write(t, d, "fleet.toml", "[[node]]\nname = 1\n") }},
		{"unparsable manifest", "manifest", "home/manifest.toml", func(d string) { write(t, d, "home/manifest.toml", "[[file]]\nsource = \"x\"\n") }},
		{"missing file source", "manifest", "home/fleet.rules is not a file", func(d string) { _ = os.Remove(filepath.Join(d, "home/fleet.rules")) }},
		{"missing tree source", "manifest", "home/skills is not a directory", func(d string) { _ = os.RemoveAll(filepath.Join(d, "home/skills")) }},
		{"stale guide", "guide", "stale", func(d string) { write(t, d, "FLEET.md", "# old\n") }},
		{"nested git metadata", "layer", "home/skills/demo/.git is machine state", func(d string) { write(t, d, "home/skills/demo/.git/HEAD", "ref\n") }},
		{"archive", "layer", "home/skills/demo.zip is machine state", func(d string) { write(t, d, "home/skills/demo.zip", "PK") }},
		{"symlink", "layer", "home/skills/link is a symlink", func(d string) {
			if err := os.Symlink("demo", filepath.Join(d, "home/skills/link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"claude pre-approves doctor", "pre-approval", `"Bash(fleet doctor *)"`, func(d string) {
			write(t, d, "home/claude-settings.json", `{"permissions": {"allow": ["Bash(fleet doctor *)"]}}`)
		}},
		{"codex pre-approves push", "pre-approval", "fleet push", func(d string) {
			write(t, d, "home/fleet.rules", `prefix_rule(pattern=["fleet", "push"], decision="allow")`+"\n")
		}},
		{"plugin never installed", "plugins", "no [[ensure]] installs it", func(d string) {
			write(t, d, "home/claude-settings.json", `{"enabledPlugins": {"other@example-market": true}}`)
		}},
		{"marketplace never added", "plugins", "no [[ensure]] adds marketplace example-market", func(d string) {
			write(t, d, "home/manifest.toml", strings.Replace(validManifest, "claude plugin marketplace add", "echo", 1))
		}},
		{"plugin before marketplace", "plugins", "installed before its marketplace", func(d string) {
			i := strings.Index(validManifest, "[[ensure]]")
			ensures := strings.SplitAfter(validManifest[i:], "\n\n")
			write(t, d, "home/manifest.toml", validManifest[:i]+ensures[1]+"\n\n"+ensures[0])
		}},
		{"agent grant to everything", "policy", "grant 1 lets tag:agent reach [*]", func(d string) {
			write(t, d, "policy/tailnet.hujson", strings.Replace(validPolicy, `"dst": ["tag:agent"]`, `"dst": ["*"]`, 1))
		}},
		{"root without check", "policy", `grants root with action "accept"`, func(d string) {
			write(t, d, "policy/tailnet.hujson", strings.Replace(validPolicy, `"action": "check"`, `"action": "accept"`, 1))
		}},
		{"no agent denial test", "policy", "no policy test", func(d string) {
			write(t, d, "policy/tailnet.hujson", strings.Replace(validPolicy, `"src": "tag:agent", "deny"`, `"src": "autogroup:member", "deny"`, 1))
		}},
		{"agent tag without owner", "policy", "tag:agent has no owner", func(d string) {
			write(t, d, "policy/tailnet.hujson", strings.Replace(validPolicy, `["autogroup:admin"]`, `[]`, 1))
		}},
		{"unknown policy key", "policy", "unexpected shape", func(d string) {
			write(t, d, "policy/tailnet.hujson", strings.Replace(validPolicy, `"tests"`, `"tset"`, 1))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := Run(writeConfig(t, tc.change))
			for _, p := range problems {
				if p.Check == tc.check && strings.Contains(p.Detail, tc.detail) {
					return
				}
			}
			t.Fatalf("want a %s problem containing %q, got %v", tc.check, tc.detail, problems)
		})
	}
}

func TestPolicyIsOptional(t *testing.T) {
	dir := writeConfig(t, func(d string) { _ = os.RemoveAll(filepath.Join(d, "policy")) })
	if problems := Run(dir); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestShippedTerminfoMustCompile(t *testing.T) {
	if _, err := exec.LookPath("tic"); err != nil {
		t.Skip("tic not installed")
	}
	good := "dumb-fleet|test terminal,\n\tam,\n\tcols#80,\n"
	dir := writeConfig(t, func(d string) {
		write(t, d, "home/terminfo/good.terminfo", good)
		write(t, d, "home/terminfo/bad.terminfo", "bad-fleet|test terminal,\n\tuse=no-such-terminal,\n")
	})
	problems := Run(dir)
	if len(problems) != 1 || problems[0].Check != "terminfo" || !strings.Contains(problems[0].Detail, "home/terminfo/bad.terminfo does not compile") {
		t.Fatalf("want one terminfo problem for bad.terminfo, got %v", problems)
	}
}
