package container

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestParseProbe(t *testing.T) {
	p, err := ParseProbe("image=yes\nsecret=fleet-claude-token\nsecret=other\n")
	if err != nil || !p.Image || !p.Secrets["fleet-claude-token"] || p.Secrets["fleet-ts-authkey"] {
		t.Fatalf("probe = %+v, %v", p, err)
	}
	if _, err := ParseProbe("secret=x\n"); err == nil {
		t.Fatal("missing image line accepted")
	}
	if _, err := ParseProbe("garbage\n"); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestRunArgsHardeningAndWiring(t *testing.T) {
	rep, err := RunArgs(Spec{Agent: "builder", Tailnet: true},
		Probe{Image: true, Secrets: map[string]bool{"fleet-ts-authkey": true, "fleet-claude-token": true}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rep.Args, " ")
	for _, want := range []string{
		"--name fleet-agent-builder", "--label fleet.agent=builder", "--init",
		"--cap-drop all", "--security-opt no-new-privileges",
		"--secret fleet-ts-authkey,type=mount,target=ts-authkey,uid=1000,gid=1000,mode=0400",
		"--volume fleet-agent-builder-state:/home/agent/.local/state/fleet",
		"--pids-limit 4096",
		"--secret fleet-claude-token,type=env,target=CLAUDE_CODE_OAUTH_TOKEN",
		"--env FLEET_ACTOR=agent-builder",
		"--volume fleet-agent-builder-work:/home/agent/work",
		"--volume fleet-agent-builder-claude:/home/agent/.claude",
		"--volume fleet-agent-builder-codex:/home/agent/.codex",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if strings.Contains(joined, "type=env,target=TS_AUTHKEY") {
		t.Error("tailnet key passed as env: every podman exec session would inherit it")
	}
	for _, banned := range []string{"--privileged", "--network host", "--pid host", "/var/run", "--user root", "-v /:"} {
		if strings.Contains(joined, banned) {
			t.Errorf("dangerous flag %q present", banned)
		}
	}
	if strings.Contains(joined, "API_KEY") {
		t.Error("an API key is wired in; agents must run on subscriptions")
	}
	if rep.Args[len(rep.Args)-1] != Image {
		t.Errorf("image not last: %v", rep.Args)
	}
	if !slices.ContainsFunc(rep.Notes, func(n string) bool { return strings.Contains(n, "codex login --device-auth") }) {
		t.Errorf("codex subscription login not explained: %v", rep.Notes)
	}
	unsigned, _ := RunArgs(Spec{Agent: "b"}, Probe{Image: true, Secrets: map[string]bool{}})
	if strings.Contains(strings.Join(unsigned.Args, " "), "CLAUDE_CODE_OAUTH_TOKEN") ||
		!slices.ContainsFunc(unsigned.Notes, func(n string) bool { return strings.Contains(n, "claude setup-token") }) {
		t.Errorf("missing claude secret: args %v notes %v", unsigned.Args, unsigned.Notes)
	}
}

func TestRunArgsTailnetWithoutKeyFailsLoud(t *testing.T) {
	_, err := RunArgs(Spec{Agent: "b", Tailnet: true}, Probe{Image: true, Secrets: map[string]bool{}})
	if err == nil || !strings.Contains(err.Error(), "podman secret create fleet-ts-authkey") {
		t.Fatalf("err = %v", err)
	}
	rep, err := RunArgs(Spec{Agent: "b"}, Probe{Image: true, Secrets: map[string]bool{"fleet-ts-authkey": true}})
	if err != nil || strings.Contains(strings.Join(rep.Args, " "), "ts-authkey") {
		t.Fatalf("tailnet off still wired key: %v %v", rep.Args, err)
	}
}

func TestRunArgsRequiresImageAndValidName(t *testing.T) {
	if _, err := RunArgs(Spec{Agent: "b"}, Probe{}); err == nil || !strings.Contains(err.Error(), "fleet image") {
		t.Fatalf("missing image: %v", err)
	}
	if _, err := RunArgs(Spec{Agent: "Bad;rm"}, Probe{Image: true}); err == nil {
		t.Fatal("invalid name accepted")
	}
}

func TestRunScriptQuotesForSh(t *testing.T) {
	script := RunScript([]string{"run", "--env", "X=it's $HOME"})
	out, err := exec.Command("sh", "-c", strings.Replace(script, "exec podman", "printf '%s\\n'", 1)).Output()
	if err != nil || string(out) != "run\n--env\nX=it's $HOME\n" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}

func TestRemoveScript(t *testing.T) {
	if s := RemoveScript("b", false); strings.Contains(s, "volume") || !strings.Contains(s, "podman rm -f -t 10 fleet-agent-b") {
		t.Fatalf("script = %q", s)
	}
	if s := RemoveScript("b", true); !strings.Contains(s, "podman volume rm -f fleet-agent-b-work fleet-agent-b-claude fleet-agent-b-codex fleet-agent-b-state") {
		t.Fatalf("script = %q", s)
	}
}

func TestParseList(t *testing.T) {
	out := `[{"Names":["fleet-agent-zed"],"State":"running","Status":"Up 2 minutes","Image":"localhost/fleet-agent:latest","Labels":{"fleet.agent":"zed"}},
	         {"Names":["fleet-agent-amy"],"State":"exited","Status":"Exited (0)","Image":"x","Labels":{"fleet.agent":"amy"}}]`
	cs, err := ParseList([]byte(out))
	if err != nil || len(cs) != 2 || cs[0].Agent != "amy" || cs[1].State != "running" || cs[1].Name != "fleet-agent-zed" {
		t.Fatalf("cs = %+v, %v", cs, err)
	}
	if _, err := ParseList([]byte("nope")); err == nil {
		t.Fatal("garbage accepted")
	}
}
