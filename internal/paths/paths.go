// Package paths finds limit-ledger's data directory and Claude Code's transcripts.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// DataDir is where limit-ledger keeps its database. LIMIT_LEDGER_HOME overrides it.
func DataDir() (string, error) {
	if d := os.Getenv("LIMIT_LEDGER_HOME"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "limit-ledger"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "limit-ledger"), nil
	case "windows":
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "limit-ledger"), nil
		}
	}
	return filepath.Join(home, ".local", "share", "limit-ledger"), nil
}

// DefaultDB is the default database file.
func DefaultDB() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "ledger.db"), nil
}

// Spool is the file the statusline hook appends samples to.
func Spool() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "samples.jsonl"), nil
}

// Chain is the file that remembers the statusline configured before limit-ledger's.
func Chain() (string, error) {
	d, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "statusline-chain.json"), nil
}

// ClaudeSettings is Claude Code's user settings file.
func ClaudeSettings() (string, error) {
	d, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "settings.json"), nil
}

// ClaudeDir is Claude Code's configuration directory. CLAUDE_CONFIG_DIR overrides it.
func ClaudeDir() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// DefaultProjects is the directory that holds Claude Code transcripts.
func DefaultProjects() (string, error) {
	d, err := ClaudeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "projects"), nil
}
