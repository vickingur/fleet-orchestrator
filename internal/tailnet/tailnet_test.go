package tailnet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

const sample = `{
  "BackendState": "Running",
  "Self": {"HostName": "Example Laptop Display Name", "DNSName": "laptop-example.example.invalid.", "OS": "macOS",
           "TailscaleIPs": ["192.0.2.13", "fd7a::1"], "Online": false, "LastSeen": "0001-01-01T00:00:00Z"},
  "Peer": {
    "k1": {"HostName": "worker-example", "DNSName": "worker-example.example.invalid.", "OS": "linux",
           "TailscaleIPs": ["192.0.2.14"], "Online": true, "LastSeen": "0001-01-01T00:00:00Z"},
    "k2": {"HostName": "localhost", "DNSName": "phone-example.example.invalid.", "OS": "iOS",
           "TailscaleIPs": ["192.0.2.15"], "Online": false, "LastSeen": "2026-10-02T18:56:57.1Z"},
    "k3": {"HostName": "agent-builder", "DNSName": "agent-builder.example.invalid.", "OS": "linux",
           "TailscaleIPs": ["192.0.2.16"], "Online": true, "Tags": ["tag:agent"]}
  }
}`

func TestParse(t *testing.T) {
	st, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	self, ok := st.Self()
	if !ok || self.Name != "laptop-example" || !self.Online {
		t.Fatalf("self = %+v (display HostName must not be used; self is always online)", self)
	}
	fw, ok := st.Find("Worker-Example")
	if !ok || !fw.Online || fw.IPs[0] != "192.0.2.14" || !fw.LastSeen.IsZero() {
		t.Fatalf("worker = %+v", fw)
	}
	phone, _ := st.Find("phone-example")
	if phone.Online || phone.LastSeen.Format(time.RFC3339) != "2026-10-02T18:56:57Z" {
		t.Fatalf("phone = %+v", phone)
	}
	if _, ok := st.Find("localhost"); ok {
		t.Fatal("matched on display HostName instead of MagicDNS name")
	}
	agents := st.Tagged("tag:agent")
	if len(agents) != 1 || agents[0].Name != "agent-builder" {
		t.Fatalf("agents = %+v", agents)
	}
}

func TestParseRejectsStoppedBackend(t *testing.T) {
	_, err := Parse([]byte(`{"BackendState": "Stopped"}`))
	if err == nil || !strings.Contains(err.Error(), "tailscale up") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Parse([]byte(`{"BackendState": "Running"}`)); err == nil {
		t.Fatal("missing Self accepted")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Fatal("garbage accepted")
	}
}

type fakeRunner struct {
	args []string
	out  string
}

func (f *fakeRunner) Run(_ context.Context, c execx.Cmd) (execx.Result, error) {
	f.args = c.Args
	_, _ = c.Stdout.Write([]byte(f.out))
	return execx.Result{}, nil
}

func TestReaderPassesSocket(t *testing.T) {
	fr := &fakeRunner{out: sample}
	if _, err := (Reader{Runner: fr, Socket: "/tmp/ts.sock"}).Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(fr.args, " ") != "--socket /tmp/ts.sock status --json" {
		t.Fatalf("args = %v", fr.args)
	}
}
