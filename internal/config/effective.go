package config

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
)

// Built-in defaults for every supported setting. The system installer
// writes a file that matches these so a minimal configuration (just
// schema_version) behaves identically.
const (
	DefaultListen        = "127.0.0.1:8090"
	DefaultExecutable    = "llama-server"
	DefaultParallel      = 3
	DefaultMaxTotalSize  = "32GiB"
	DefaultNamespaceSize = "4GiB"
	DefaultEntrySize     = "2GiB"
	DefaultTTL           = "168h"
	DefaultMinReuse      = 256
	DefaultContextSize   = 8192
	DefaultGPULayers     = 99
)

// Effective is the fully resolved configuration: every field carries its
// final value, sizes are byte counts and durations are parsed. Nothing
// merges at use time — this is the single snapshot the manager applies.
type Effective struct {
	// Path is the file these settings came from ("" for pure defaults).
	Path       string
	Listen     string
	DataDir    string
	ModelsDir  string
	Executable string
	Parallel   int
	RuntimeEnv map[string]string

	MaxTotalBytes int64
	Cache         CachePolicy
	Launch        LaunchPolicy

	Profiles map[string]EffectiveProfile
}

// CachePolicy is the host-wide aggregate cap plus the per-namespace policy
// launches inherit when they do not override it.
type CachePolicy struct {
	Enabled        bool
	MaxNamespace   int64
	MaxEntry       int64
	TTLSec         int64
	MinReuseTokens int64
}

type LaunchPolicy struct {
	ContextSize int
	GPULayers   int
	Args        []string
	Env         map[string]string
}

// EffectiveProfile is one resolved declarative profile: cache and launch
// fields are inherited from the configured defaults, args replace (never
// append) the inherited array, and env merges over it.
type EffectiveProfile struct {
	Name      string
	Model     string
	Port      int
	Autostart bool
	Context   int
	GPULayers int
	Args      []string
	Env       map[string]string
	Cache     CachePolicy
}

// profileNameRe matches the manager's alias rules: a stable name usable as
// an OpenAI model alias and as a UI profile identity.
var profileNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Resolve validates the decoded file and produces the effective snapshot.
// dir is the directory the file was loaded from: relative manager paths
// resolve against it. No shell or tilde expansion is performed.
func (c *Config) Resolve(dir string) (*Effective, error) {
	if c.SchemaVersion == nil {
		return nil, errors.New("schema_version is required (this build supports schema_version: 1)")
	}
	if *c.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %d: this build supports exactly %d", *c.SchemaVersion, SchemaVersion)
	}
	e := &Effective{
		Listen:     DefaultListen,
		Executable: DefaultExecutable,
		Parallel:   DefaultParallel,
		RuntimeEnv: map[string]string{},
		Launch: LaunchPolicy{
			ContextSize: DefaultContextSize,
			GPULayers:   DefaultGPULayers,
			Args:        []string{},
			Env:         map[string]string{},
		},
	}
	var err error
	if e.MaxTotalBytes, err = sizeOr(c.PromptCacheField(func(p *PromptCache) *string { return p.MaxTotalSize }), DefaultMaxTotalSize, "prompt_cache.max_total_size"); err != nil {
		return nil, err
	}
	if err = e.resolveSections(c, dir); err != nil {
		return nil, err
	}
	if err = e.resolveCache(c); err != nil {
		return nil, err
	}
	if c.LaunchDefault != nil {
		ld := c.LaunchDefault
		if err = validateLaunchScalars(ld.ContextSize, ld.GPULayers, ld.Args, "launch_defaults"); err != nil {
			return nil, err
		}
		if ld.ContextSize != nil {
			e.Launch.ContextSize = *ld.ContextSize
		}
		if ld.GPULayers != nil {
			e.Launch.GPULayers = *ld.GPULayers
		}
		if ld.Args != nil {
			e.Launch.Args = append([]string{}, *ld.Args...)
		}
		if err = mergeEnv(e.Launch.Env, ld.Env, "launch_defaults.env"); err != nil {
			return nil, err
		}
	}
	if err = e.resolveProfiles(c); err != nil {
		return nil, err
	}
	return e, nil
}

// PromptCacheField reads an optional prompt_cache string through a getter,
// tolerating an absent section.
func (c *Config) PromptCacheField(get func(*PromptCache) *string) *string {
	if c.PromptCache == nil {
		return nil
	}
	return get(c.PromptCache)
}

func (e *Effective) resolveSections(c *Config, dir string) error {
	if c.HTTP != nil && c.HTTP.Listen != nil {
		e.Listen = *c.HTTP.Listen
	}
	if err := validateListen(e.Listen); err != nil {
		return err
	}
	e.DataDir = defaultDataDir(dir)
	e.ModelsDir = path.Join(e.DataDir, "models")
	if c.Paths != nil {
		if c.Paths.DataDir != nil {
			e.DataDir = resolvePath(*c.Paths.DataDir, dir)
			e.ModelsDir = path.Join(e.DataDir, "models")
		}
		if c.Paths.ModelsDir != nil {
			e.ModelsDir = resolvePath(*c.Paths.ModelsDir, dir)
		}
	}
	if e.DataDir == e.ModelsDir {
		return errors.New("paths.models_dir cannot be the same directory as paths.data_dir")
	}
	if c.Runtime != nil {
		if c.Runtime.Executable != nil {
			e.Executable = resolveExecutable(*c.Runtime.Executable, dir)
		}
		if err := mergeEnv(e.RuntimeEnv, c.Runtime.Env, "runtime.env"); err != nil {
			return err
		}
	}
	if c.Downloads != nil && c.Downloads.Parallel != nil {
		if *c.Downloads.Parallel < 1 || *c.Downloads.Parallel > 32 {
			return fmt.Errorf("downloads.parallel must be between 1 and 32 (got %d)", *c.Downloads.Parallel)
		}
		e.Parallel = *c.Downloads.Parallel
	}
	return nil
}

func (e *Effective) resolveCache(c *Config) error {
	var d *CacheDefaults
	if c.PromptCache != nil {
		d = c.PromptCache.Defaults
	}
	e.Cache = CachePolicy{Enabled: false, MinReuseTokens: DefaultMinReuse}
	var err error
	if e.Cache.MaxNamespace, err = sizeOr(pick(d, func(x *CacheDefaults) *string { return x.MaxNamespaceSize }), DefaultNamespaceSize, "prompt_cache.defaults.max_namespace_size"); err != nil {
		return err
	}
	if e.Cache.MaxEntry, err = sizeOr(pick(d, func(x *CacheDefaults) *string { return x.MaxEntrySize }), DefaultEntrySize, "prompt_cache.defaults.max_entry_size"); err != nil {
		return err
	}
	ttl, err := durationOr(pick(d, func(x *CacheDefaults) *string { return x.TTL }), DefaultTTL, "prompt_cache.defaults.ttl")
	if err != nil {
		return err
	}
	e.Cache.TTLSec = int64(ttl / 1e9)
	if v := pick(d, func(x *CacheDefaults) *int { return x.MinReuseTokens }); v != nil {
		if *v < 0 {
			return fmt.Errorf("prompt_cache.defaults.min_reuse_tokens cannot be negative (got %d)", *v)
		}
		e.Cache.MinReuseTokens = int64(*v)
	}
	if v := pick(d, func(x *CacheDefaults) *bool { return x.Enabled }); v != nil {
		e.Cache.Enabled = *v
	}
	return validateCacheOrder(e.Cache.MaxEntry, e.Cache.MaxNamespace, e.MaxTotalBytes, "prompt_cache.defaults")
}

func (e *Effective) resolveProfiles(c *Config) error {
	e.Profiles = map[string]EffectiveProfile{}
	ports := map[int]string{}
	for name, p := range c.Profiles {
		if err := validateProfileIdentity(name, p, ports); err != nil {
			return err
		}
		ep, err := e.profilePolicy(name, p)
		if err != nil {
			return err
		}
		if p.Autostart != nil {
			ep.Autostart = *p.Autostart
		}
		e.Profiles[name] = ep
	}
	return nil
}

// validateProfileIdentity checks the name, model reference and port of a
// declared profile, registering the port so two profiles can never claim it.
func validateProfileIdentity(name string, p Profile, ports map[int]string) error {
	if !profileNameRe.MatchString(name) {
		return fmt.Errorf("profiles.%s: name must match %s (letters, digits, dot, dash, underscore)", name, profileNameRe.String())
	}
	if p.Model == nil || *p.Model == "" {
		return fmt.Errorf("profiles.%s.model is required", name)
	}
	if err := validateModelRef(name, *p.Model); err != nil {
		return err
	}
	if p.Port == nil {
		return fmt.Errorf("profiles.%s.port is required", name)
	}
	if *p.Port < 1024 || *p.Port > 65535 {
		return fmt.Errorf("profiles.%s.port must be between 1024 and 65535 (got %d)", name, *p.Port)
	}
	if other, dup := ports[*p.Port]; dup {
		return fmt.Errorf("profiles.%s.port %d collides with profile %s", name, *p.Port, other)
	}
	ports[*p.Port] = name
	return nil
}

// profilePolicy resolves one profile against the host defaults: scalars and
// args replace, env merges, cache fields inherit per-field.
func (e *Effective) profilePolicy(name string, p Profile) (EffectiveProfile, error) {
	ep := EffectiveProfile{
		Name: name, Model: *p.Model, Port: *p.Port,
		Context: e.Launch.ContextSize, GPULayers: e.Launch.GPULayers,
		Args:  append([]string{}, e.Launch.Args...),
		Env:   copyEnv(e.Launch.Env),
		Cache: e.Cache,
	}
	if err := validateLaunchScalars(p.ContextSize, p.GPULayers, p.Args, "profiles."+name); err != nil {
		return ep, err
	}
	if p.ContextSize != nil {
		ep.Context = *p.ContextSize
	}
	if p.GPULayers != nil {
		ep.GPULayers = *p.GPULayers
	}
	if p.Args != nil {
		ep.Args = append([]string{}, *p.Args...)
	}
	if err := mergeEnv(ep.Env, p.Env, "profiles."+name+".env"); err != nil {
		return ep, err
	}
	if p.PromptCache != nil {
		if err := e.applyProfileCache(&ep, name, p.PromptCache); err != nil {
			return ep, err
		}
	}
	return ep, nil
}

// rebuildLaunch is intentionally absent: profile scalars are applied inline
// above so args replace (never append) the inherited array.

func (e *Effective) applyProfileCache(ep *EffectiveProfile, name string, pc *CacheDefaults) error {
	c := ep.Cache
	var err error
	if v := pick(pc, func(x *CacheDefaults) *bool { return x.Enabled }); v != nil {
		c.Enabled = *v
	}
	if c.MaxNamespace, err = sizeOr(pick(pc, func(x *CacheDefaults) *string { return x.MaxNamespaceSize }), itoaSize(c.MaxNamespace), "profiles."+name+".prompt_cache.max_namespace_size"); err != nil {
		return err
	}
	if c.MaxEntry, err = sizeOr(pick(pc, func(x *CacheDefaults) *string { return x.MaxEntrySize }), itoaSize(c.MaxEntry), "profiles."+name+".prompt_cache.max_entry_size"); err != nil {
		return err
	}
	ttl, err := durationOr(pick(pc, func(x *CacheDefaults) *string { return x.TTL }), fmt.Sprintf("%ds", c.TTLSec), "profiles."+name+".prompt_cache.ttl")
	if err != nil {
		return err
	}
	c.TTLSec = int64(ttl / 1e9)
	if v := pick(pc, func(x *CacheDefaults) *int { return x.MinReuseTokens }); v != nil {
		if *v < 0 {
			return fmt.Errorf("profiles.%s.prompt_cache.min_reuse_tokens cannot be negative", name)
		}
		c.MinReuseTokens = int64(*v)
	}
	if err := validateCacheOrder(c.MaxEntry, c.MaxNamespace, e.MaxTotalBytes, "profiles."+name+".prompt_cache"); err != nil {
		return err
	}
	ep.Cache = c
	return nil
}

// --- helpers ---

func pick[T any](d *CacheDefaults, get func(*CacheDefaults) *T) *T {
	if d == nil {
		return nil
	}
	return get(d)
}

func sizeOr(v *string, def, field string) (int64, error) {
	if v == nil {
		v = &def
	}
	n, err := ParseSize(*v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%s must be positive; zero does not mean unlimited in schema_version 1", field)
	}
	return n, nil
}

func durationOr(v *string, def, field string) (time.Duration, error) {
	if v == nil {
		v = &def
	}
	d, err := ParseDuration(*v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive; zero does not mean unlimited in schema_version 1", field)
	}
	return d, nil
}

func validateCacheOrder(entry, namespace, total int64, field string) error {
	if entry > namespace {
		return fmt.Errorf("%s.max_entry_size (%d bytes) cannot exceed max_namespace_size (%d bytes)", field, entry, namespace)
	}
	if namespace > total {
		return fmt.Errorf("%s.max_namespace_size (%d bytes) cannot exceed the host-wide prompt_cache.max_total_size (%d bytes)", field, namespace, total)
	}
	return nil
}

func validateLaunchScalars(ctxSize, gpuLayers *int, args *[]string, field string) error {
	if ctxSize != nil && *ctxSize < 1 {
		return fmt.Errorf("%s.context_size must be positive (got %d)", field, *ctxSize)
	}
	if gpuLayers != nil && *gpuLayers < 0 {
		return fmt.Errorf("%s.gpu_layers cannot be negative (got %d)", field, *gpuLayers)
	}
	if args != nil {
		for _, a := range *args {
			if a == "" {
				return fmt.Errorf("%s.args cannot contain empty items", field)
			}
		}
	}
	return nil
}

func validateListen(addr string) error {
	if addr == "" || !strings.Contains(addr, ":") {
		return fmt.Errorf("http.listen must be a host:port address (got %q)", addr)
	}
	return nil
}

// validateModelRef enforces that a profile model is a relative path under
// the models directory; absolute paths and traversal are refused here,
// symlink containment is enforced at launch by the manager.
func validateModelRef(name, model string) error {
	if strings.HasPrefix(model, "/") || strings.Contains(model, "\\") {
		return fmt.Errorf("profiles.%s.model must be a path relative to paths.models_dir (got %q)", name, model)
	}
	clean := path.Clean(model)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != model {
		return fmt.Errorf("profiles.%s.model must stay inside paths.models_dir without traversal or dot segments (got %q)", name, model)
	}
	if !strings.HasSuffix(strings.ToLower(model), ".gguf") {
		return fmt.Errorf("profiles.%s.model must reference a .gguf file (got %q)", name, model)
	}
	return nil
}

func mergeEnv(dst map[string]string, src map[string]string, field string) error {
	for k, v := range src {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("%s: invalid environment variable name or value", field)
		}
		if strings.HasPrefix(k, "LLAMA_ARG_") {
			return fmt.Errorf("%s: %s is a manager-owned override; use launch arguments instead", field, k)
		}
		dst[k] = v
	}
	return nil
}

func copyEnv(src map[string]string) map[string]string {
	m := make(map[string]string, len(src))
	for k, v := range src {
		m[k] = v
	}
	return m
}

func resolvePath(p, dir string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(dir, p)
}

// defaultDataDir: relative paths resolve against the configuration file's
// directory, so a file at /etc/opentambo defaults its state beside it only
// when the operator asks; without a file the manual-mode "./data" stands.
func defaultDataDir(dir string) string {
	if dir == "" {
		return "./data"
	}
	return resolvePath("./data", dir)
}

// resolveExecutable keeps bare basenames PATH-resolved; a relative path
// with a separator resolves against the configuration file's directory.
func resolveExecutable(p, dir string) string {
	if path.Base(p) == p {
		return p
	}
	return resolvePath(p, dir)
}

func itoaSize(n int64) string { return fmt.Sprintf("%dB", n) }
