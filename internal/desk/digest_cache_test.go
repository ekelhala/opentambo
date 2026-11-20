package desk

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDigestCacheRoundTripAndCorruption covers the on-disk digest cache:
// a corrupt file must be tolerated (it only costs re-hashing) and a saved
// cache must reload exactly.
func TestDigestCacheRoundTripAndCorruption(t *testing.T) {
	dir := t.TempDir()
	promptCache := filepath.Join(dir, "prompt-cache")
	if err := os.MkdirAll(promptCache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(promptCache, "digests.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &digestCache{entries: map[string]*digestRecord{}}
	if err := c.load(dir); err != nil {
		t.Fatalf("a corrupt digest cache must be tolerated: %v", err)
	}
	if len(c.entries) != 0 {
		t.Errorf("corrupt cache must load no entries: %v", c.entries)
	}
	// The loaded guard makes repeat loads no-ops.
	c.entries["kept"] = &digestRecord{Size: 1, MTime: 2, SHA256: "x"}
	if err := c.load(dir); err != nil || len(c.entries) != 1 {
		t.Fatalf("second load must be a no-op: %v %v", err, c.entries)
	}
	// Save then reload in a fresh cache.
	c.entries["k"] = &digestRecord{Size: 3, MTime: 7, SHA256: "abc"}
	if err := c.save(dir); err != nil {
		t.Fatal(err)
	}
	reloaded := &digestCache{entries: map[string]*digestRecord{}}
	if err := reloaded.load(dir); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.entries["k"]; got == nil || got.SHA256 != "abc" || got.Size != 3 || got.MTime != 7 {
		t.Errorf("digest round trip lost data: %+v", got)
	}
}

// TestDigestCacheMissingFileIsClean: no cache file at all is the normal
// first-run state, not an error.
func TestDigestCacheMissingFileIsClean(t *testing.T) {
	c := &digestCache{entries: map[string]*digestRecord{}}
	if err := c.load(filepath.Join(t.TempDir(), "nonexistent")); err != nil {
		t.Fatalf("missing cache file must be clean: %v", err)
	}
}
