package syncer

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ApplyOptions locate the files apply writes besides the targets.
type ApplyOptions struct {
	Home      string
	StateFile string
	// BackupDir receives a copy of every existing file before it is changed,
	// at the same home-relative path.
	BackupDir string
}

// ApplyResult separates the two ways a change can be left undone.
type ApplyResult struct {
	// Conflicts were skipped on purpose: the file was edited locally.
	Conflicts []string
	// Failed changes hit an error; see the returned error for each cause.
	Failed []string
}

// Apply performs the plan. Conflicts are skipped and listed; real failures
// (unwritable files, a state file that cannot be saved) come back as the
// error, never folded into the conflict list. State is saved after every
// change, so an interrupted apply never mistakes its own writes for local edits.
func (p Plan) Apply(opt ApplyOptions) (ApplyResult, error) {
	next := State{Version: stateVersion, Files: map[string]FileState{}}
	for k, v := range p.prior.Files {
		next.Files[k] = v
	}
	var res ApplyResult
	var errs []error
	for _, c := range p.Changes {
		if c.Action == Conflict {
			res.Conflicts = append(res.Conflicts, c.Target)
			continue
		}
		if err := c.apply(opt); err != nil {
			res.Failed = append(res.Failed, c.Target)
			errs = append(errs, fmt.Errorf("%s: %w", c.Target, err))
			continue
		}
		if c.entry == nil {
			delete(next.Files, c.Target)
		} else {
			next.Files[c.Target] = *c.entry
		}
		if c.Action == Unchanged {
			continue // recorded below with the final save
		}
		if err := SaveState(opt.StateFile, next); err != nil {
			return res, errors.Join(append(errs, fmt.Errorf("save state after %s: %w", c.Target, err))...)
		}
	}
	if err := SaveState(opt.StateFile, next); err != nil {
		errs = append(errs, fmt.Errorf("save state: %w", err))
	}
	return res, errors.Join(errs...)
}

func (c Change) apply(opt ApplyOptions) error {
	switch c.Action {
	case Unchanged, Release:
		return nil
	case Create:
		return writeAtomic(c.abs, c.data, c.mode)
	case Update, Merge:
		if _, err := BackupFile(c.abs, c.Target, opt.BackupDir); err != nil {
			return err
		}
		return writeAtomic(c.abs, c.data, c.mode)
	case Delete:
		if _, err := BackupFile(c.abs, c.Target, opt.BackupDir); err != nil {
			return err
		}
		return os.Remove(c.abs)
	}
	return fmt.Errorf("unknown action %q", c.Action)
}

// BackupFile copies the file at abs into the backup set dir, at the same
// home-relative path as its "~/..." key and with its permission bits (set
// explicitly, so the umask cannot narrow them), so a restore can recreate a
// deleted file as it was. The copy is written atomically: a file in the set
// is always a complete snapshot, never a partial one left by a failed write.
// The first copy in a set wins: a key already saved keeps the content from
// before the set began, however many changes touch it. A file that does not
// exist is not an error: there is nothing to keep, and saved reports false.
func BackupFile(abs, key, dir string) (saved bool, err error) {
	info, err := os.Stat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	dst := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(key, "~/")))
	if _, err := os.Lstat(dst); err == nil {
		return true, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false, err
	}
	return true, writeAtomic(dst, data, info.Mode().Perm())
}
