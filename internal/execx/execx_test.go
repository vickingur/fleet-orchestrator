package execx

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunExitCodeIsNotAnError(t *testing.T) {
	res, err := OS{}.Run(context.Background(), Cmd{Name: "sh", Args: []string{"-c", "exit 7"}, Timeout: 5 * time.Second})
	if err != nil || res.ExitCode != 7 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestRunTimeoutKills(t *testing.T) {
	start := time.Now()
	_, err := OS{}.Run(context.Background(), Cmd{Name: "sleep", Args: []string{"5"}, Timeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not kill promptly")
	}
}

func TestRunRequiresTimeout(t *testing.T) {
	if _, err := (OS{}).Run(context.Background(), Cmd{Name: "true"}); err == nil {
		t.Fatal("missing timeout accepted")
	}
}

func TestRunMissingBinary(t *testing.T) {
	_, err := OS{}.Run(context.Background(), Cmd{Name: "definitely-not-a-binary-xyz", Timeout: time.Second})
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
}

func TestRunWiresStreamsAndEnv(t *testing.T) {
	var out bytes.Buffer
	_, err := OS{}.Run(context.Background(), Cmd{
		Name: "sh", Args: []string{"-c", `cat; printf ":%s" "$FOO"`},
		Stdin: strings.NewReader("in"), Stdout: &out, Env: []string{"FOO=bar"}, Timeout: 5 * time.Second,
	})
	if err != nil || out.String() != "in:bar" {
		t.Fatalf("out = %q, err = %v", out.String(), err)
	}
}

func TestOutput(t *testing.T) {
	out, err := Output(context.Background(), OS{}, Cmd{Name: "echo", Args: []string{"hi"}, Timeout: 5 * time.Second})
	if err != nil || string(out) != "hi\n" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	_, err = Output(context.Background(), OS{}, Cmd{Name: "sh", Args: []string{"-c", "echo boom >&2; exit 3"}, Timeout: 5 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "exited 3: boom") {
		t.Fatalf("err = %v", err)
	}
}
