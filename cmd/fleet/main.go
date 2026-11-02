// Command fleet operates a personal fleet of machines and agent containers
// over Tailscale. See FLEET.md.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vickingur/fleet-orchestrator/internal/cli"
	"github.com/vickingur/fleet-orchestrator/internal/execx"
	"github.com/vickingur/fleet-orchestrator/internal/tui"
)

// version is set at build time to the bundle content hash.
var version = "dev"

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleet:", err)
		os.Exit(1)
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fleet:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	app := &cli.App{
		Version: version,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Runner:  execx.OS{},
		Getenv:  os.Getenv,
		Home:    home,
		Cwd:     cwd,
		Now:     time.Now,
	}
	args := os.Args[1:]
	if (len(args) == 0 && isTerminal(os.Stdin) && isTerminal(os.Stdout)) || (len(args) == 1 && args[0] == "tui") {
		err := tui.Run(ctx, app)
		stop()
		if err != nil {
			fmt.Fprintln(os.Stderr, "fleet:", err)
			os.Exit(1)
		}
		return
	}
	code := app.Run(ctx, args)
	stop()
	os.Exit(code)
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
