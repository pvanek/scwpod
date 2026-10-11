package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pvanek/scwpod/internal/core"
)

func TestTokenFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "tokens.json")
	tf, err := LoadTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pr := core.ProjectRegion{ProjectID: "p1", Region: "fr-par"}
	if tf.Token(pr) != "" {
		t.Error("new store not empty")
	}
	if err := tf.SetToken(pr, "s3cret"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %o, want 600", perm)
	}
	again, err := LoadTokenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Token(pr); got != "s3cret" {
		t.Errorf("reloaded token = %q", got)
	}
}
