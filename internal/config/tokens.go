package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/pvanek/scwpod/internal/core"
)

// TokenFile stores Cockpit query tokens as JSON, readable only by the user:
// {"<project-id>/<region>": "<secret>"}. It implements core.CockpitTokens.
type TokenFile struct {
	path   string
	mu     sync.Mutex
	tokens map[string]string
}

// DefaultTokenPath is ~/.config/scwpod/cockpit-tokens.json.
func DefaultTokenPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "scwpod", "cockpit-tokens.json"), nil
}

// LoadTokenFile reads path; a missing file yields an empty store.
func LoadTokenFile(path string) (*TokenFile, error) {
	t := &TokenFile{path: path, tokens: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &t.tokens); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	return t, nil
}

func tokenKey(pr core.ProjectRegion) string { return pr.ProjectID + "/" + pr.Region }

func (t *TokenFile) Token(pr core.ProjectRegion) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.tokens[tokenKey(pr)]
}

func (t *TokenFile) SetToken(pr core.ProjectRegion, secret string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tokens[tokenKey(pr)] = secret

	data, err := json.MarshalIndent(t.tokens, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return err
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, t.path)
}
