// Package settings installs and removes limit-ledger's statusline in Claude Code's
// user settings file. It keeps every other setting and its order, and leaves a
// backup of the file before writing.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mehdashti/limit-ledger/internal/sample"
)

// Object is a JSON object that keeps its key order.
type Object struct {
	keys []string
	vals map[string]json.RawMessage
}

// Parse reads a JSON object. Empty input is an empty object.
func Parse(b []byte) (*Object, error) {
	o := &Object{vals: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("settings file is not a JSON object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := kt.(string)
		if !ok {
			return nil, errors.New("settings file has a non-string key")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

// Get returns a value.
func (o *Object) Get(k string) (json.RawMessage, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// Set sets a value, keeping the key's position or appending it.
func (o *Object) Set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Delete removes a key.
func (o *Object) Delete(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// Bytes renders the object with two-space indentation.
func (o *Object) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(o.vals[k])
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// HookCommand is the statusline command for the binary at exe.
func HookCommand(exe string) string { return fmt.Sprintf("%q statusline", exe) }

// IsOurs reports whether a statusLine setting is limit-ledger's.
func IsOurs(raw json.RawMessage) bool {
	var sl struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &sl) != nil {
		return false
	}
	c := strings.TrimSpace(sl.Command)
	return strings.Contains(c, "limit-ledger") && strings.HasSuffix(c, " statusline")
}

// Install makes limit-ledger the statusline. A statusline that was already set is
// saved in the chain file and keeps being shown. It returns the backup file path.
func Install(settingsPath, chainPath, exe string, now time.Time) (backup string, err error) {
	orig, mode, err := read(settingsPath)
	if err != nil {
		return "", err
	}
	o, err := Parse(orig)
	if err != nil {
		return "", fmt.Errorf("%s: %w", settingsPath, err)
	}
	if cur, ok := o.Get("statusLine"); ok && !IsOurs(cur) {
		if err := sample.SaveChain(chainPath, sample.Chain{Previous: cur}); err != nil {
			return "", err
		}
	} else if !ok {
		if err := sample.SaveChain(chainPath, sample.Chain{}); err != nil {
			return "", err
		}
	}
	ours, err := json.Marshal(struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Padding int    `json:"padding"`
	}{"command", HookCommand(exe), 0})
	if err != nil {
		return "", err
	}
	o.Set("statusLine", ours)
	return write(settingsPath, orig, mode, o, now)
}

// Uninstall puts back the statusline that was there before, or removes the setting.
func Uninstall(settingsPath, chainPath string, now time.Time) (backup string, err error) {
	orig, mode, err := read(settingsPath)
	if err != nil {
		return "", err
	}
	o, err := Parse(orig)
	if err != nil {
		return "", fmt.Errorf("%s: %w", settingsPath, err)
	}
	cur, ok := o.Get("statusLine")
	if !ok || !IsOurs(cur) {
		return "", errors.New("limit-ledger is not the configured statusline; nothing to undo")
	}
	if prev := sample.LoadChain(chainPath).Previous; len(prev) > 0 {
		o.Set("statusLine", prev)
	} else {
		o.Delete("statusLine")
	}
	backup, err = write(settingsPath, orig, mode, o, now)
	if err != nil {
		return "", err
	}
	// The previous statusline is back in the settings; forget it here.
	if err := os.Remove(chainPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return backup, err
	}
	return backup, nil
}

func read(path string) ([]byte, fs.FileMode, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0o600, nil
	}
	if err != nil {
		return nil, 0, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return b, fi.Mode().Perm(), nil
}

func write(path string, orig []byte, mode fs.FileMode, o *Object, now time.Time) (string, error) {
	b, err := o.Bytes()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	backup := ""
	if orig != nil {
		backup = fmt.Sprintf("%s.limit-ledger-backup-%s", path, now.UTC().Format("20060102-150405"))
		if err := os.WriteFile(backup, orig, mode); err != nil {
			return "", err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return backup, os.Rename(tmp.Name(), path)
}
