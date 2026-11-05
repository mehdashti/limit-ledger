package merge

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeSSH(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script as ssh")
	}
	p := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := SSH
	SSH = p
	t.Cleanup(func() { SSH = old })
}

func TestPull(t *testing.T) {
	main, laptop := open(t), open(t)
	fill(t, main, map[string]string{"2026-11-01T00:00:00Z": "org-a"}, nil)
	fill(t, laptop, map[string]string{"2026-11-01T00:00:00Z": "org-a"}, map[string]int64{"2026-11-02T10:00:00Z": 40})
	b, _, err := Export(laptop, 0, ms("2026-11-05T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "bundle.json")
	data, _ := json.Marshal(b)
	if err := os.WriteFile(bundle, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The fake ssh checks it was called without a password prompt, then prints the bundle.
	fakeSSH(t, `case "$*" in *BatchMode=yes*ConnectTimeout=5*"laptop ~/.local/bin/limit-ledger ingest"*) cat '`+bundle+`' ;; *) echo "bad args: $*" >&2; exit 3 ;; esac`)
	stats, err := Pull(main, PullOptions{Host: "laptop"}, ms("2026-11-05T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rows != 1 || stats.PerAccount[main.AccountKey("org-a")] != 1 {
		t.Errorf("pull stats %+v", stats)
	}

	fakeSSH(t, `echo "ssh: connect to host laptop port 22: No route to host" >&2; exit 255`)
	if _, err := Pull(main, PullOptions{Host: "laptop"}, 0); !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "No route") {
		t.Errorf("unreachable host: %v", err)
	}
	fakeSSH(t, `echo "limit-ledger: no ledger" >&2; exit 1`)
	if _, err := Pull(main, PullOptions{Host: "laptop"}, 0); err == nil || errors.Is(err, ErrUnreachable) {
		t.Errorf("a remote failure is an error, not unreachable: %v", err)
	}
	if _, err := Pull(main, PullOptions{Host: "laptop", RemoteBin: "x; rm -rf ~"}, 0); err == nil {
		t.Error("shell characters in the remote binary path must be refused")
	}
}
