// Package config loads the versioned operator configuration file. YAML
// expresses intent (listen address, paths, cache policy, launch defaults,
// named profiles); SQLite remains the store for runtime state, history and
// UI-managed profiles. The service never rewrites this file.
package config

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"go.yaml.in/yaml/v4"
)

// SchemaVersion is the only configuration schema this build understands.
// It versions the file independently of the SQLite schema and the HTTP API.
const SchemaVersion = 1

// Config is the decoded v1 operator configuration. Pointer fields keep the
// difference between "omitted" and an explicit false/zero/empty value:
// merging must preserve both.
type Config struct {
	SchemaVersion *int               `yaml:"schema_version"`
	HTTP          *HTTP              `yaml:"http"`
	Paths         *Paths             `yaml:"paths"`
	Runtime       *Runtime           `yaml:"runtime"`
	Downloads     *Downloads         `yaml:"downloads"`
	PromptCache   *PromptCache       `yaml:"prompt_cache"`
	LaunchDefault *LaunchDefaults    `yaml:"launch_defaults"`
	Profiles      map[string]Profile `yaml:"profiles"`
}

type HTTP struct {
	Listen *string `yaml:"listen"`
}

type Paths struct {
	DataDir   *string `yaml:"data_dir"`
	ModelsDir *string `yaml:"models_dir"`
}

type Runtime struct {
	Executable *string           `yaml:"executable"`
	Env        map[string]string `yaml:"env"`
}

type Downloads struct {
	Parallel *int `yaml:"parallel"`
}

// PromptCache carries the host-wide aggregate cap plus the defaults that
// launches inherit. Sizes are strings with explicit binary units; they are
// parsed once at validation time into SizeBytes.
type PromptCache struct {
	MaxTotalSize *string        `yaml:"max_total_size"`
	Defaults     *CacheDefaults `yaml:"defaults"`
}

// CacheDefaults is the launch-inherited prompt-cache policy. Enabled is a
// pointer so an explicit false is kept distinct from an omitted field.
type CacheDefaults struct {
	Enabled          *bool   `yaml:"enabled"`
	MaxNamespaceSize *string `yaml:"max_namespace_size"`
	MaxEntrySize     *string `yaml:"max_entry_size"`
	TTL              *string `yaml:"ttl"`
	MinReuseTokens   *int    `yaml:"min_reuse_tokens"`
}

type LaunchDefaults struct {
	ContextSize *int              `yaml:"context_size"`
	GPULayers   *int              `yaml:"gpu_layers"`
	Args        *[]string         `yaml:"args"`
	Env         map[string]string `yaml:"env"`
}

// Profile is one declarative launch definition. The map key is its stable
// identity (and API alias); distinct profiles may share a model file.
type Profile struct {
	Model       *string           `yaml:"model"`
	Port        *int              `yaml:"port"`
	Autostart   *bool             `yaml:"autostart"`
	ContextSize *int              `yaml:"context_size"`
	GPULayers   *int              `yaml:"gpu_layers"`
	Args        *[]string         `yaml:"args"`
	Env         map[string]string `yaml:"env"`
	PromptCache *CacheDefaults    `yaml:"prompt_cache"`
}

// Parse decodes exactly one strict YAML document: unknown keys, duplicate
// keys, multiple documents, and explicit nulls are all rejected.
func Parse(data []byte) (*Config, error) {
	var root yaml.Node
	if err := yaml.Load(data, &root); err != nil {
		return nil, fmt.Errorf("parse configuration: %w", err)
	}
	if root.Kind == 0 {
		return nil, errors.New("configuration file is empty")
	}
	if err := rejectNulls(&root, ""); err != nil {
		return nil, err
	}
	cfg := &Config{}
	if err := root.Load(cfg, yaml.WithKnownFields(), yaml.WithUniqueKeys()); err != nil {
		return nil, fmt.Errorf("configuration is invalid: %w", err)
	}
	if err := rejectTypedScalars(&root, reflectShape()); err != nil {
		return nil, err
	}
	return cfg, nil
}

// rejectNulls refuses explicit nulls: v1 has no field where null is a
// meaningful value, and silently treating it as omitted hides typos.
func rejectNulls(n *yaml.Node, path string) error {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			if err := rejectNulls(c, path); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			p := key.Value
			if path != "" {
				p = path + "." + key.Value
			}
			if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
				return fmt.Errorf("%s: explicit null is not valid here; omit the field to use the default (line %d)", p, val.Line)
			}
			if err := rejectNulls(val, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// shape describes the expected YAML scalar type of each leaf so unquoted
// booleans/numbers in string fields (and vice versa) are reported with the
// field path instead of being coerced.
type shape struct {
	scalar string // "", "string", "integer", "boolean"
	// children maps mapping keys to their shapes; elems describes sequence
	// items; values describes map values when the container is a string map.
	children map[string]*shape
	elems    *shape
	values   *shape
}

func reflectShape() *shape {
	str := &shape{scalar: "string"}
	num := &shape{scalar: "integer"}
	flag := &shape{scalar: "boolean"}
	stringMap := &shape{values: str}
	cache := &shape{children: map[string]*shape{
		"enabled": flag, "max_namespace_size": str, "max_entry_size": str,
		"ttl": str, "min_reuse_tokens": num,
	}}
	launch := func() *shape {
		return &shape{children: map[string]*shape{
			"context_size": num, "gpu_layers": num,
			"args": {elems: str}, "env": stringMap,
		}}
	}
	return &shape{children: map[string]*shape{
		"schema_version":  num,
		"http":            {children: map[string]*shape{"listen": str}},
		"paths":           {children: map[string]*shape{"data_dir": str, "models_dir": str}},
		"runtime":         {children: map[string]*shape{"executable": str, "env": stringMap}},
		"downloads":       {children: map[string]*shape{"parallel": num}},
		"prompt_cache":    {children: map[string]*shape{"max_total_size": str, "defaults": cache}},
		"launch_defaults": launch(),
		"profiles":        {values: &shape{children: mergeShapes(launch().children, map[string]*shape{"model": str, "port": num, "autostart": flag, "prompt_cache": cache})}},
	}}
}

func mergeShapes(a, b map[string]*shape) map[string]*shape {
	m := make(map[string]*shape, len(a)+len(b))
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

func expectedTag(kind string) string {
	switch kind {
	case "string":
		return "!!str"
	case "integer":
		return "!!int"
	case "boolean":
		return "!!bool"
	}
	return ""
}

// rejectTypedScalars walks the document against the declared shape so type
// mismatches name the field path (without echoing secret values).
func rejectTypedScalars(n *yaml.Node, sh *shape) error {
	if sh == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil
		}
		n = n.Content[0]
	}
	switch n.Kind {
	case yaml.ScalarNode:
		if sh.scalar != "" && n.Tag != "!!null" && n.Tag != expectedTag(sh.scalar) {
			return fmt.Errorf("value must be %s like %s (line %d)",
				map[string]string{"string": "a quoted string", "integer": "a plain number", "boolean": "true or false"}[sh.scalar],
				map[string]string{"string": `"8090"`, "integer": `8192`, "boolean": `false`}[sh.scalar], n.Line)
		}
	case yaml.SequenceNode:
		for _, c := range n.Content {
			if err := rejectTypedScalars(c, sh.elems); err != nil {
				return err
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, val := n.Content[i], n.Content[i+1]
			var child *shape
			if sh.children != nil {
				child = sh.children[key.Value]
			} else if sh.values != nil {
				child = sh.values
			}
			if err := rejectTypedScalars(val, child); err != nil {
				return fmt.Errorf("%s: %w", key.Value, err)
			}
		}
	}
	return nil
}

// --- sizes and durations ---

var sizeRe = regexp.MustCompile(`^([0-9]+)(B|KiB|MiB|GiB|TiB)$`)

var sizeMultipliers = map[string]int64{
	"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
}

// ParseSize parses an explicit binary-unit size such as "32GiB". Units are
// required (bare numbers are rejected: they hide whether the author meant
// bytes or kilobytes) and multiplication is overflow-checked.
func ParseSize(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("%q is not a size: use a positive number with one of B, KiB, MiB, GiB, TiB (for example 32GiB)", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("size %q: number too large", s)
	}
	f := sizeMultipliers[m[2]]
	if n > (1<<63-1)/f {
		return 0, fmt.Errorf("size %q overflows the supported range", s)
	}
	return n * f, nil
}

// ParseDuration accepts Go duration syntax ("30m", "168h"). It deliberately
// rejects unsupported suffixes such as "7d".
func ParseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration: use Go syntax such as 30m, 24h or 168h (days like 7d are not supported)", s)
	}
	return d, nil
}
