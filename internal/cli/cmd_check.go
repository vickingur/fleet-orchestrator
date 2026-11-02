package cli

import (
	"context"
	"fmt"

	"github.com/vickingur/fleet-orchestrator/internal/configcheck"
)

// --- check -----------------------------------------------------------------

func (a *App) cmdCheck(_ context.Context, args []string) error {
	fs := newFlags("check")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := parse(fs, args); err != nil {
		return err
	}
	var dir string
	switch fs.NArg() {
	case 0:
		cfg, err := a.configDir()
		if err != nil {
			return err
		}
		dir = cfg
	case 1:
		dir = fs.Arg(0)
	default:
		return usagef("expects at most one config directory")
	}
	problems := configcheck.Run(dir)
	if *asJSON {
		if err := writeJSON(a.out, struct {
			Config   string                `json:"config"`
			Problems []configcheck.Problem `json:"problems"`
		}{dir, append([]configcheck.Problem{}, problems...)}); err != nil {
			return err
		}
	} else {
		for _, p := range problems {
			a.printf("%-12s %s\n", p.Check, p.Detail)
		}
		if len(problems) == 0 {
			a.printf("%s: config is valid\n", dir)
		}
	}
	if len(problems) > 0 {
		return codeError{code: exitError, err: fmt.Errorf("%s: %d problem(s) found", dir, len(problems))}
	}
	return nil
}
