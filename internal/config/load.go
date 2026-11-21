package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SystemPath is the standard operator configuration file used by the
// system-wide service installation (#16).
const SystemPath = "/etc/opentambo/config.yaml"

// Discovery selects the configuration file, in order: an explicit
// --config path, the OPENTAMBO_CONFIG environment variable, then the
// standard system path when that file exists. A missing implicit default
// keeps manual flag-based operation working; an explicitly selected
// missing file is an error.
type Discovery struct {
	ConfigFlag string // from --config
	Env        func(string) string
	// SystemPath is overridable so tests can stage a standard layout.
	SystemPath string
}

// Selected reports the chosen file path and whether selection was explicit
// (flag or environment). ok is false when no file applies (manual mode).
func (d Discovery) Selected() (string, bool, error) {
	getenv := d.Env
	if getenv == nil {
		getenv = os.Getenv
	}
	sys := d.SystemPath
	if sys == "" {
		sys = SystemPath
	}
	if d.ConfigFlag != "" {
		return d.ConfigFlag, true, nil
	}
	if env := getenv("OPENTAMBO_CONFIG"); env != "" {
		return env, true, nil
	}
	if _, err := os.Stat(sys); err == nil {
		return sys, false, nil
	}
	return "", false, nil
}

// Load selects, reads, parses and resolves the configuration. When no file
// applies it returns (nil, nil): the caller proceeds with flags and
// built-in defaults. Explicit files that are missing or unreadable are
// reported as errors.
func (d Discovery) Load() (*Effective, error) {
	path, _, err := d.Selected()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return LoadFile(path)
}

// LoadFile reads, parses and resolves one configuration file.
func LoadFile(path string) (*Effective, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("configuration file %s: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	eff, err := cfg.Resolve(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	eff.Path = filepath.Clean(path)
	return eff, nil
}

// ErrNoFile reports that no configuration file applies.
var ErrNoFile = errors.New("no configuration file selected")
