package lifecycle

import (
	"fmt"
	"os"
	"path/filepath"
)

// Operator configuration seeding (issue #17). The installer writes a
// starter file only when none exists: an operator-owned file is never
// overwritten, and the running service stores runtime changes in SQLite
// rather than rewriting this file.

func (c Config) seededConfig() string {
	return fmt.Sprintf(`# OpenTambo operator configuration.
# Managed by the system administrator; the service reads this file at
# startup and never rewrites it (runtime state lives in the SQLite
# database under the data directory). Validate edits with:
#   opentambo config check --config %s
schema_version: 1

http:
  listen: %q

paths:
  data_dir: %q
  models_dir: %q

runtime:
  executable: %q
  env: {}

downloads:
  parallel: 3
`, c.ConfigPath(), c.Listen, c.StateDir, c.ModelsDir, c.LlamaServer)
}

// seedConfig writes the starter file when absent and records that this
// installation created it (only then may --purge-all remove it).
func (in *Installer) seedConfig(rec *Record) error {
	c := in.Cfg
	path := c.path(c.ConfigPath())
	if _, err := os.Stat(path); err == nil {
		in.logf("retained existing operator configuration %s", c.ConfigPath())
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect %s: %w", c.ConfigPath(), err)
	}
	if err := os.WriteFile(path, []byte(c.seededConfig()), 0644); err != nil {
		return fmt.Errorf("seed configuration %s: %w", c.ConfigPath(), err)
	}
	rec.CreatedConfig = true
	in.logf("seeded operator configuration %s (edit freely; validate with `opentambo config check`)", c.ConfigPath())
	return nil
}

// seededConfigPath re-validates the recorded config location before any
// removal, mirroring the ownership-record discipline: only a file this
// installer created at the expected path may be removed.
func seededConfigPath(rec Record, c Config) (string, error) {
	if !rec.CreatedConfig {
		return "", nil
	}
	recorded := filepath.Join(rec.EtcDir, filepath.Base(c.ConfigPath()))
	return safeRemovalPath(recorded, c.ConfigPath(), c.Prefix)
}
