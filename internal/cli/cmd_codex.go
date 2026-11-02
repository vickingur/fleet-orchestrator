package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/execx"
)

// codexServeScript starts Codex's app-server on the node's loopback with a
// capability token read from stdin, and stops it when stdin closes (that is,
// when the local fleet process ends the tunnel).
const codexServeScript = `set -eu
dir="$HOME/.local/state/fleet"
mkdir -p "$dir"
umask 077
tok="$dir/codex-ws-token-$$"
IFS= read -r t
printf %%s "$t" > "$tok"
codex app-server --listen ws://127.0.0.1:%d --ws-auth capability-token --ws-token-file "$tok" > "$dir/codex-app-server.log" 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null; rm -f "$tok"' EXIT INT TERM HUP
cat > /dev/null
`

func (a *App) cmdCodex(ctx context.Context, args []string) (retErr error) {
	fs := newFlags("codex")
	check := fs.Bool("check", false, "verify the tunnel and token auth, then exit without opening the TUI")
	if err := parse(fs, args); err != nil {
		return err
	}
	name, err := oneArg(fs.Args(), "node")
	if err != nil {
		return err
	}
	inv, err := a.inventory()
	if err != nil {
		return err
	}
	node, err := inv.Node(name)
	if err != nil {
		return usagef("%v", err)
	}
	ex, err := a.executor(ctx, inv)
	if err != nil {
		return err
	}
	if node.Name == ex.self {
		return usagef("%s is this machine; run codex directly", name)
	}
	token, err := randomHex(32)
	if err != nil {
		return err
	}
	localPort, err := freePort()
	if err != nil {
		return err
	}
	rp, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return err
	}
	remotePort := 20000 + int(rp.Int64())

	// The session is audited from before the tunnel opens until it closes,
	// including sessions that fail to start.
	end, err := ex.audited("codex", name, fmt.Sprintf("app-server on 127.0.0.1:%d, tunnel from 127.0.0.1:%d", remotePort, localPort))
	if err != nil {
		return err
	}
	defer func() {
		if logErr := end(execx.Result{}, retErr); logErr != nil {
			retErr = errors.Join(retErr, logErr)
		}
	}()

	tunnel := exec.CommandContext(ctx, "ssh", a.sshOptions().TunnelArgs(node, localPort, remotePort, fmt.Sprintf(codexServeScript, remotePort))...)
	stdin, err := tunnel.StdinPipe()
	if err != nil {
		return err
	}
	tunnelErr := &lockedBuffer{}
	tunnel.Stderr = tunnelErr
	if err := tunnel.Start(); err != nil {
		return fmt.Errorf("start ssh tunnel: %w", err)
	}
	defer func() {
		// Closing stdin is the stop signal for the remote app-server; a close
		// error at teardown has no further consequence.
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = tunnel.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = tunnel.Process.Kill()
			<-done
		}
	}()
	if _, err := io.WriteString(stdin, token+"\n"); err != nil {
		return fmt.Errorf("send token: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	status, err := waitForUpgrade(ctx, addr, token, 30*time.Second)
	if err != nil {
		return fmt.Errorf("codex app-server on %s did not come up: %w %s", name, err, strings.TrimSpace(tunnelErr.String()))
	}
	if status != 101 {
		return fmt.Errorf("codex app-server on %s answered %d to an authorized handshake", name, status)
	}
	if *check {
		anon, err := handshake(addr, "")
		if err != nil {
			return err
		}
		if anon == 101 {
			return errors.New("app-server accepted a handshake without the token")
		}
		a.printf("codex app-server on %s is reachable through the tunnel (authorized handshake 101, anonymous %d)\n", name, anon)
		return nil
	}
	tui := execx.Cmd{
		Name:    "codex",
		Args:    []string{"--remote", "ws://" + addr, "--remote-auth-token-env", "FLEET_CODEX_TOKEN"},
		Env:     append(os.Environ(), "FLEET_CODEX_TOKEN="+token),
		Stdin:   a.Stdin,
		Stdout:  a.out,
		Stderr:  a.Stderr,
		Timeout: 24 * time.Hour,
	}
	res, err := a.Runner.Run(ctx, tui)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return codeError{code: res.ExitCode, err: errors.New("")}
	}
	return nil
}

// lockedBuffer is a concurrency-safe stderr sink: exec copies into it from its
// own goroutine while this one may read it for an error message.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// handshake sends a WebSocket upgrade request and returns the HTTP status.
func handshake(addr, token string) (int, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }() // read-only probe; nothing to flush
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return 0, err
	}
	req := "GET / HTTP/1.1\r\nHost: " + addr + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: ZmxlZXQtaGFuZHNoYWtlLTE=\r\nSec-WebSocket-Version: 13\r\n"
	if token != "" {
		req += "Authorization: Bearer " + token + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		return 0, err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return 0, err
	}
	var proto string
	var status int
	if _, err := fmt.Sscanf(line, "%s %d", &proto, &status); err != nil {
		return 0, fmt.Errorf("bad status line %q", strings.TrimSpace(line))
	}
	return status, nil
}

func waitForUpgrade(ctx context.Context, addr, token string, limit time.Duration) (int, error) {
	deadline := time.Now().Add(limit)
	var last error
	for time.Now().Before(deadline) {
		status, err := handshake(addr, token)
		if err == nil {
			return status, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	return 0, fmt.Errorf("timed out after %s: %w", limit, last)
}
