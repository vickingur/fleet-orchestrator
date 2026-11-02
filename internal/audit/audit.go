// Package audit appends one JSON line per remote action, so what any agent did
// to any node can be reconstructed afterwards.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one audited action. Each action writes a "start" record before it
// runs and an "end" record after, sharing an ID, so an action cut short by a
// crash or kill still shows up.
type Record struct {
	ID       string    `json:"id"`
	Phase    string    `json:"phase"`
	Time     time.Time `json:"time"`
	Actor    string    `json:"actor"`
	Command  string    `json:"command"`
	Target   string    `json:"target"`
	Detail   string    `json:"detail"`
	ExitCode int       `json:"exit_code"`
	Error    string    `json:"error,omitempty"`
	Millis   int64     `json:"ms"`
}

// Log appends records to a JSONL file.
type Log struct {
	Path string
	mu   sync.Mutex
}

// Append writes r as one line. O_APPEND keeps concurrent fleet processes from
// interleaving partial lines.
func (l *Log) Append(r Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.Path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(l.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return errors.Join(fmt.Errorf("write audit log: %w", err), f.Close())
	}
	return f.Close()
}

// Actor identifies who is acting: FLEET_ACTOR when set (agent configs set it),
// otherwise the local user.
func Actor() string {
	if a := os.Getenv("FLEET_ACTOR"); a != "" {
		return a
	}
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}
