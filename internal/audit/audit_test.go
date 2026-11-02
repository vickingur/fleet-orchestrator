package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendConcurrentLinesStayWhole(t *testing.T) {
	l := &Log{Path: filepath.Join(t.TempDir(), "nested", "audit.jsonl")}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := l.Append(Record{Time: time.Unix(int64(i), 0), Actor: "t", Command: "exec", Target: "fw", Detail: "uptime", ExitCode: i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	info, err := os.Stat(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("audit log mode %o, want 600", info.Mode().Perm())
	}
	data, err := os.ReadFile(l.Path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("torn line %q: %v", line, err)
		}
		seen[r.ExitCode] = true
	}
	if len(seen) != 50 {
		t.Fatalf("got %d distinct records, want 50", len(seen))
	}
}

func TestActor(t *testing.T) {
	t.Setenv("FLEET_ACTOR", "claude-code")
	if Actor() != "claude-code" {
		t.Fatal("FLEET_ACTOR ignored")
	}
	t.Setenv("FLEET_ACTOR", "")
	t.Setenv("USER", "operator")
	if Actor() != "operator" {
		t.Fatal("USER fallback broken")
	}
}
