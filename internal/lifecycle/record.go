package lifecycle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// RecordVersion stamps the ownership-record schema.
const RecordVersion = 1

// Record is the root-controlled installation ownership record: the single
// place that distinguishes installer-created resources from operator files.
// Uninstall reads it and refuses to trust its paths without re-validation.
type Record struct {
	Version      int      `json:"version"`
	User         string   `json:"user"`
	Group        string   `json:"group"`
	CreatedUser  bool     `json:"created_user"`
	CreatedGroup bool     `json:"created_group"`
	BinPath      string   `json:"bin_path"`
	EtcDir       string   `json:"etc_dir"`
	StateDir     string   `json:"state_dir"`
	ModelsDir    string   `json:"models_dir"`
	UnitPath     string   `json:"unit_path"`
	LlamaServer  string   `json:"llama_server"`
	Listen       string   `json:"listen"`
	ExtraGroups  []string `json:"extra_groups,omitempty"`
	// CreatedModelsDir marks the model directory as installer-created;
	// only then may --purge-models ever remove it.
	CreatedModelsDir bool `json:"created_models_dir"`
	// StateMigrated records that an operator-requested state import from a
	// per-user installation happened (see MigrateState).
	StateMigratedFrom string `json:"state_migrated_from,omitempty"`
}

func recordFromConfig(c Config) Record {
	return Record{
		Version: RecordVersion, User: c.User, Group: c.Group,
		BinPath: c.BinPath, EtcDir: c.EtcDir, StateDir: c.StateDir,
		ModelsDir: c.ModelsDir, UnitPath: c.UnitPath,
		LlamaServer: c.LlamaServer, Listen: c.Listen, ExtraGroups: c.ExtraGroups,
	}
}

// save writes the record atomically, owner-only (it is read by root only).
func saveRecord(path string, rec Record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, contents, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// loadRecord reads the ownership record; ok is false when it does not
// exist (uninstall then falls back to defaults, best effort).
func loadRecord(path string) (Record, bool, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Record{}, false, nil
		}
		return Record{}, false, err
	}
	var rec Record
	if err = json.Unmarshal(contents, &rec); err != nil {
		return Record{}, false, fmt.Errorf("installation record %s is corrupt: %w", path, err)
	}
	if rec.Version != RecordVersion {
		return Record{}, false, fmt.Errorf("installation record %s has unsupported version %d", path, rec.Version)
	}
	return rec, true, nil
}

// safeRemovalPath re-validates a path read from the record before any
// deletion: absolute, outside the staging prefix root, not a dangerous
// system location, and matching the layout it came from.
func safeRemovalPath(candidate, expected string, prefix string) (string, error) {
	if candidate != expected {
		return "", fmt.Errorf("refusing to remove %q: recorded path does not match the configured layout %q", candidate, expected)
	}
	return resolveSafe(candidate, prefix)
}

// resolveSafe cleans a path under the staging prefix and rejects anything
// that would be catastrophic to delete.
func resolveSafe(p, prefix string) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", fmt.Errorf("refusing to remove unsafe path %q", p)
	}
	full := filepath.Join(prefix, filepath.Clean(p))
	if full == "/" || full == filepath.Clean(prefix)+"/" {
		return "", fmt.Errorf("refusing to remove %q", full)
	}
	for _, banned := range []string{"/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64", "/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/var", "/usr/local", "/var/lib", "/etc/systemd", "/etc/systemd/system"} {
		bannedFull := filepath.Join(prefix, banned)
		if full == bannedFull {
			return "", fmt.Errorf("refusing to remove system directory %q", full)
		}
	}
	return full, nil
}

// resolveWithin additionally requires the path to live inside root (the
// managed state tree), so a purge can never reach an operator's external
// model directory however the options are spelled.
func resolveWithin(p, prefix, root string) (string, error) {
	full, err := resolveSafe(p, prefix)
	if err != nil {
		return "", err
	}
	rootFull := filepath.Join(prefix, filepath.Clean(root))
	if full != rootFull && !isUnder(full+"/", rootFull+"/") {
		return "", fmt.Errorf("refusing to remove %q: outside the managed state tree %q", full, root)
	}
	return full, nil
}

func isUnder(path, root string) bool {
	return len(path) >= len(root) && path[:len(root)] == root
}
