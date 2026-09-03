package sp

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCacheFile has to leave either the old cache or the new one on disk,
// never a partial file, and the file it leaves must be private. The rename
// itself cannot be interrupted from a test, so what is checked is the
// observable contract: content replaced in full, mode 0600, and no temp file
// left behind in the directory.
func TestWriteCacheFileReplacesAtomically(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "sp-token.json")

	if err := writeCacheFile(path, []byte(`{"v":1}`)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := writeCacheFile(path, []byte(`{"v":2}`)); err != nil {
		t.Fatalf("second write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if string(got) != `{"v":2}` {
		t.Errorf("content = %q, want the second write", got)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 0600", perm)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("cache dir holds %v, want only sp-token.json (no temp file left behind)", names)
	}
}
