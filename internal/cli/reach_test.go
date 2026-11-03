package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

// mini reaches only mac. From mini, fleet refuses fw before any SSH, still
// reaches mac, and runs its own commands locally.
func TestANodeConnectsOnlyToTheNodesItReaches(t *testing.T) {
	h := newHarness(t)
	inv := strings.Replace(testInventory, "name = \"mini\"\nhost = \"desktop-example\"\nuser = \"v\"\nos = \"darwin\"\nrole = \"member\"\n",
		"name = \"mini\"\nhost = \"desktop-example\"\nuser = \"v\"\nos = \"darwin\"\nrole = \"member\"\nreaches = [\"mac\"]\n", 1)
	if inv == testInventory {
		t.Fatal("fixture did not change")
	}
	if err := os.WriteFile(filepath.Join(h.cfg, "fleet.toml"), []byte(inv), 0o644); err != nil {
		t.Fatal(err)
	}
	h.env["FLEET_SELF"] = "mini"
	h.runner.handler = func(execx.Cmd) (string, int, error) { return "ran\n", 0, nil }

	for _, args := range [][]string{
		{"exec", "fw", "--", "true"},
		{"exec", "fw:builder", "--", "true"},
		{"ssh", "fw"},
		{"codex", "--check", "fw"},
	} {
		if code := h.run(args...); code == 0 || !strings.Contains(h.errb.String(), "mini does not reach fw: its reaches list in fleet.toml is [mac]") {
			t.Errorf("%v: exit %d, stderr %q", args, code, h.errb)
		}
	}
	if calls := h.runner.sshCalls(); len(calls) != 0 {
		t.Fatalf("fleet connected to an unreachable node: %v", calls)
	}

	if code := h.run("exec", "mac", "--", "true"); code != 0 || len(h.runner.sshCalls()) != 1 {
		t.Fatalf("a listed node was refused: exit %d %s", code, h.errb)
	}
	if code := h.run("exec", "mini", "--", "true"); code != 0 || len(h.runner.sshCalls()) != 1 {
		t.Fatalf("the node itself must run locally: exit %d %s", code, h.errb)
	}
	if code := h.run("exec", "all", "--", "true"); code == 0 || !strings.Contains(h.out.String()+h.errb.String(), "does not reach fw") {
		t.Fatalf("all must report the unreachable node: exit %d %s %s", code, h.out, h.errb)
	}
}
