package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mehdashti/limit-ledger/internal/sample"
)

const original = `{
  "model": "opus",
  "hooks": {
    "Stop": [{"hooks": [{"type": "command", "command": "echo done"}]}]
  },
  "permissions": {"allow": ["Bash(ls:*)"]},
  "theme": "dark"
}
`

func TestParseKeepsOrder(t *testing.T) {
	o, err := Parse([]byte(original))
	if err != nil {
		t.Fatal(err)
	}
	b, err := o.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`"model"`, `"hooks"`, `"permissions"`, `"theme"`}
	last := -1
	for _, k := range want {
		i := strings.Index(string(b), k)
		if i < last {
			t.Fatalf("key order changed:\n%s", b)
		}
		last = i
	}
	var a, c any
	_ = json.Unmarshal([]byte(original), &a)
	_ = json.Unmarshal(b, &c)
	if mustJSON(a) != mustJSON(c) {
		t.Errorf("content changed:\n%s", b)
	}
	if _, err := Parse([]byte(`[1,2]`)); err == nil {
		t.Error("a non-object must be rejected")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestInstallUninstallWithoutPrevious(t *testing.T) {
	dir := t.TempDir()
	path, chain := filepath.Join(dir, "settings.json"), filepath.Join(dir, "data", "chain.json")
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	backup, err := Install(path, chain, "/opt/bin/limit-ledger", now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(backup); string(b) != original {
		t.Error("backup must hold the original file")
	}
	b, _ := os.ReadFile(path)
	o, _ := Parse(b)
	sl, ok := o.Get("statusLine")
	if !ok || !IsOurs(sl) || !strings.Contains(string(sl), `\"/opt/bin/limit-ledger\" statusline`) {
		t.Fatalf("statusLine not set: %s", b)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o640 {
		t.Errorf("file mode changed to %v", fi.Mode().Perm())
	}
	// Installing again is harmless.
	if _, err := Install(path, chain, "/opt/bin/limit-ledger", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := Uninstall(path, chain, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	var a, c any
	_ = json.Unmarshal([]byte(original), &a)
	_ = json.Unmarshal(b, &c)
	if mustJSON(a) != mustJSON(c) {
		t.Errorf("uninstall did not restore the settings:\n%s", b)
	}
	if _, err := Uninstall(path, chain, now.Add(3*time.Second)); err == nil {
		t.Error("uninstall when not installed must fail")
	}
}

func TestInstallKeepsPreviousStatusline(t *testing.T) {
	dir := t.TempDir()
	path, chain := filepath.Join(dir, "settings.json"), filepath.Join(dir, "chain.json")
	prev := `{"type":"command","command":"~/bin/my-status.sh","padding":1}`
	if err := os.WriteFile(path, []byte(`{"statusLine":`+prev+`,"theme":"dark"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := Install(path, chain, "/x/limit-ledger", now); err != nil {
		t.Fatal(err)
	}
	if got := string(sample.LoadChain(chain).Previous); mustJSONRaw(got) != mustJSONRaw(prev) {
		t.Errorf("previous statusline not saved: %s", got)
	}
	// A second install must not overwrite the saved previous statusline with ours.
	if _, err := Install(path, chain, "/x/limit-ledger", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := string(sample.LoadChain(chain).Previous); mustJSONRaw(got) != mustJSONRaw(prev) {
		t.Errorf("previous statusline lost on reinstall: %s", got)
	}
	if _, err := Uninstall(path, chain, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	o, _ := Parse(b)
	sl, _ := o.Get("statusLine")
	if mustJSONRaw(string(sl)) != mustJSONRaw(prev) {
		t.Errorf("previous statusline not restored: %s", b)
	}
}

func mustJSONRaw(s string) string {
	var v any
	_ = json.Unmarshal([]byte(s), &v)
	return mustJSON(v)
}

func TestInstallCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude", "settings.json")
	backup, err := Install(path, filepath.Join(dir, "chain.json"), "/x/limit-ledger", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if backup != "" {
		t.Error("no backup expected for a file that did not exist")
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "statusLine") {
		t.Errorf("file %s", b)
	}
}

func TestUninstallForgetsChain(t *testing.T) {
	dir := t.TempDir()
	path, chain := filepath.Join(dir, "settings.json"), filepath.Join(dir, "chain.json")
	if err := os.WriteFile(path, []byte(`{"statusLine":{"type":"command","command":"echo old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(path, chain, "/x/limit-ledger", time.Now()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"type": "command",`+"\n"+`    "command"`) {
		t.Errorf("our statusLine should list type first:\n%s", b)
	}
	if _, err := Uninstall(path, chain, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(chain); !os.IsNotExist(err) {
		t.Error("chain file should be removed after uninstall")
	}
}
