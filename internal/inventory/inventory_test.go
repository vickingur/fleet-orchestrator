package inventory

import (
	"strings"
	"testing"
)

const valid = `
[[node]]
name = "laptop"
host = "laptop-example"
user = "operator"
os = "darwin"
role = "controller"

[[node]]
name = "worker"
host = "worker-example"
user = "operator"
os = "linux"
role = "member"
runtime = "podman"
port = 2222

[[tool]]
name = "claude"
version = "claude --version"
`

func mustParse(t *testing.T, s string) *Inventory {
	t.Helper()
	inv, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return inv
}

func TestParseValid(t *testing.T) {
	inv := mustParse(t, valid)
	if got := inv.Names(); strings.Join(got, ",") != "laptop,worker" {
		t.Fatalf("names = %v", got)
	}
	if c := inv.Controller(); c.Name != "laptop" {
		t.Fatalf("controller = %s", c.Name)
	}
	fw, _ := inv.Node("worker")
	if fw.SSHPort() != 2222 {
		t.Fatalf("port = %d", fw.SSHPort())
	}
	mb, _ := inv.Node("laptop")
	if mb.SSHPort() != 22 {
		t.Fatalf("default port = %d", mb.SSHPort())
	}
	if len(inv.Tools) != 1 || inv.Tools[0].Version != "claude --version" {
		t.Fatalf("tools = %+v", inv.Tools)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]struct {
		toml string
		want string
	}{
		"unknown key": {strings.Replace(valid, `port = 2222`, `prot = 2222`, 1), "unknown keys: node.prot"},
		"bad os":      {strings.Replace(valid, `os = "linux"`, `os = "windows"`, 1), "os must be"},
		"no user":     {strings.Replace(valid, `user = "operator"`, ``, 1), "user is required"},
		"two controllers": {strings.Replace(valid, `role = "member"`, `role = "controller"`, 1),
			"exactly one controller node, found 2"},
		"duplicate":        {strings.Replace(valid, `name = "worker"`, `name = "laptop"`, 1), "duplicate name"},
		"bad name":         {strings.Replace(valid, `name = "worker"`, `name = "Frame Work"`, 1), "name must match"},
		"runtime":          {strings.Replace(valid, `runtime = "podman"`, `runtime = "docker"`, 1), "runtime must be"},
		"bad role":         {strings.Replace(valid, `role = "member"`, `role = "worker"`, 1), "role must be"},
		"tool":             {valid + "\n[[tool]]\nname = \"codex\"\n", "name and version command are required"},
		"syntax":           {"[[node]\n", "parse inventory"},
		"empty":            {"", "no nodes"},
		"port":             {strings.Replace(valid, `port = 2222`, `port = 70000`, 1), "port out of range"},
		"option-like user": {strings.Replace(valid, `user = "operator"`, `user = "-oProxyCommand=touch /tmp/p"`, 1), "must match"},
		"option-like host": {strings.Replace(valid, `host = "worker-example"`, `host = "-oProxyCommand=x"`, 1), "DNS name or IPv4"},
		"shelly host":      {strings.Replace(valid, `host = "worker-example"`, `host = "a;b"`, 1), "DNS name or IPv4"},
		"tool name":        {strings.Replace(valid, `name = "claude"`, `name = "x';rm"`, 1), "tool[0]: name"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.toml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	inv := mustParse(t, valid)

	all, err := inv.Resolve("all")
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %v, %v", all, err)
	}

	one, err := inv.Resolve("worker:builder")
	if err != nil {
		t.Fatal(err)
	}
	if one[0].Node.Name != "worker" || one[0].Agent != "builder" || one[0].String() != "worker:builder" {
		t.Fatalf("agent target = %+v", one[0])
	}

	if _, err := inv.Resolve("laptop:builder"); err == nil || !strings.Contains(err.Error(), "no container runtime") {
		t.Fatalf("agent on runtime-less node: err = %v", err)
	}
	if _, err := inv.Resolve("worker:Bad_Name"); err == nil || !strings.Contains(err.Error(), "must match") {
		t.Fatalf("bad agent name: err = %v", err)
	}
	list, err := inv.Resolve("worker,laptop,worker")
	if err != nil || len(list) != 2 || list[0].Node.Name != "worker" || list[1].Node.Name != "laptop" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, bad := range []string{"worker,", "all,laptop", "worker,nope"} {
		if _, err := inv.Resolve(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := inv.Resolve("nope"); err == nil || !strings.Contains(err.Error(), "known: laptop, worker") {
		t.Fatalf("unknown node: err = %v", err)
	}
}

func TestSortedNodes(t *testing.T) {
	inv := mustParse(t, valid)
	inv.Nodes[0], inv.Nodes[1] = inv.Nodes[1], inv.Nodes[0]
	got := inv.SortedNodes()
	if got[0].Name != "laptop" || got[1].Name != "worker" {
		t.Fatalf("sorted = %v", got)
	}
	if inv.Nodes[0].Name != "worker" {
		t.Fatal("SortedNodes mutated inventory order")
	}
}
