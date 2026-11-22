package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fullExample = `
schema_version: 1

http:
  listen: "127.0.0.1:8091"

paths:
  data_dir: "/var/lib/opentambo"
  models_dir: "/var/lib/opentambo/models"

runtime:
  executable: "/opt/llama.cpp/bin/llama-server"
  env:
    CUDA_VISIBLE_DEVICES: "0"

downloads:
  parallel: 3

prompt_cache:
  max_total_size: "32GiB"
  defaults:
    enabled: false
    max_namespace_size: "4GiB"
    max_entry_size: "2GiB"
    ttl: "168h"
    min_reuse_tokens: 256

launch_defaults:
  context_size: 8192
  gpu_layers: 99
  args: []
  env: {}

profiles:
  local-chat:
    model: "local/llama-Q4_K_M.gguf"
    port: 8080
    autostart: false
    context_size: 32768
    args: ["--parallel", "1", "--jinja"]
    env: {}
    prompt_cache:
      enabled: true
      ttl: "336h"
`

func resolve(t *testing.T, src string) *Effective {
	t.Helper()
	cfg, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	eff, err := cfg.Resolve("/etc/opentambo")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return eff
}

func TestExampleResolves(t *testing.T) {
	eff := resolve(t, fullExample)
	if eff.Listen != "127.0.0.1:8091" {
		t.Errorf("Listen = %q", eff.Listen)
	}
	if eff.DataDir != "/var/lib/opentambo" || eff.ModelsDir != "/var/lib/opentambo/models" {
		t.Errorf("paths = %q %q", eff.DataDir, eff.ModelsDir)
	}
	if eff.Executable != "/opt/llama.cpp/bin/llama-server" {
		t.Errorf("Executable = %q", eff.Executable)
	}
	if eff.RuntimeEnv["CUDA_VISIBLE_DEVICES"] != "0" {
		t.Errorf("RuntimeEnv = %v", eff.RuntimeEnv)
	}
	if eff.MaxTotalBytes != 32<<30 {
		t.Errorf("MaxTotalBytes = %d", eff.MaxTotalBytes)
	}
	if eff.Cache.Enabled {
		t.Error("host cache defaults must stay off unless explicitly enabled")
	}
	if eff.Cache.MaxNamespace != 4<<30 || eff.Cache.MaxEntry != 2<<30 || eff.Cache.TTLSec != 168*3600 || eff.Cache.MinReuseTokens != 256 {
		t.Errorf("Cache = %+v", eff.Cache)
	}
	p, ok := eff.Profiles["local-chat"]
	if !ok {
		t.Fatal("profile local-chat missing")
	}
	if p.Port != 8080 || p.Model != "local/llama-Q4_K_M.gguf" || p.Autostart {
		t.Errorf("profile = %+v", p)
	}
	if p.Context != 32768 {
		t.Errorf("profile context = %d", p.Context)
	}
	if strings.Join(p.Args, ",") != "--parallel,1,--jinja" {
		t.Errorf("profile args must replace inherited array, got %v", p.Args)
	}
	if !p.Cache.Enabled || p.Cache.TTLSec != 336*3600 {
		t.Errorf("profile cache inheritance/override = %+v", p.Cache)
	}
	// Inherited namespace/entry limits stay the host defaults.
	if p.Cache.MaxNamespace != 4<<30 || p.Cache.MaxEntry != 2<<30 {
		t.Errorf("profile cache inherit = %+v", p.Cache)
	}
}

func TestMinimalConfigUsesDefaults(t *testing.T) {
	eff := resolve(t, "schema_version: 1\n")
	if eff.Listen != DefaultListen || eff.Executable != DefaultExecutable || eff.Parallel != DefaultParallel {
		t.Errorf("defaults = %+v", eff)
	}
	if eff.MaxTotalBytes != 32<<30 || eff.Cache.MaxNamespace != 4<<30 || eff.Cache.MaxEntry != 2<<30 {
		t.Errorf("cache defaults = %+v total=%d", eff.Cache, eff.MaxTotalBytes)
	}
	if eff.Launch.ContextSize != 8192 || eff.Launch.GPULayers != 99 {
		t.Errorf("launch defaults = %+v", eff.Launch)
	}
	if len(eff.Profiles) != 0 {
		t.Errorf("profiles = %v", eff.Profiles)
	}
}

func TestParseRejections(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"missing schema", "http:\n  listen: \"x\"\n", "schema_version is required"},
		{"future schema", "schema_version: 2\n", "unsupported schema_version 2"},
		{"zero schema", "schema_version: 0\n", "unsupported schema_version 0"},
		{"unknown key", "schema_version: 1\nbogus: 1\n", "bogus"},
		{"duplicate key", "schema_version: 1\nschema_version: 1\n", "already defined"},
		{"two documents", "schema_version: 1\n---\nschema_version: 1\n", "multiple"},
		{"empty file", "\n", "no documents"},
		{"explicit null", "schema_version: 1\nhttp:\n  listen:\n", "explicit null"},
		{"nested explicit null", "schema_version: 1\nprofiles:\n  p:\n    model:\n", "explicit null"},
		{"bool as string", "schema_version: 1\nruntime:\n  env:\n    K: true\n", "quoted string"},
		{"int as string", "schema_version: 1\nhttp:\n  listen: 8090\n", "quoted string"},
		{"string as int", "schema_version: 1\ndownloads:\n  parallel: \"3\"\n", "cannot construct"},
		{"string as bool", "schema_version: 1\nprompt_cache:\n  defaults:\n    enabled: \"yes\"\n", "true or false"},
		{"empty document", "---\n", "configuration file is empty"},
		{"null document", "null\n", "configuration file is empty"},
		{"sequence item type", "schema_version: 1\nlaunch_defaults:\n  args: [\"--flash\", 3]\n", "quoted string"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			if err == nil {
				// Schema-version rules are semantic and reported by Resolve.
				cfg, perr := Parse([]byte(tc.src))
				if perr != nil {
					err = perr
				} else {
					_, err = cfg.Resolve("/etc/opentambo")
				}
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestResolveRejections(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"parallel zero", "schema_version: 1\ndownloads:\n  parallel: 0\n", "between 1 and 32"},
		{"parallel big", "schema_version: 1\ndownloads:\n  parallel: 33\n", "between 1 and 32"},
		{"bad size unit", "schema_version: 1\nprompt_cache:\n  max_total_size: \"10GB\"\n", "not a size"},
		{"bad duration day", "schema_version: 1\nprompt_cache:\n  defaults:\n    ttl: \"7d\"\n", "7d"},
		{"negative ttl", "schema_version: 1\nprompt_cache:\n  defaults:\n    ttl: \"-1h\"\n", "positive"},
		{"zero namespace", "schema_version: 1\nprompt_cache:\n  defaults:\n    max_namespace_size: \"0B\"\n", "must be positive"},
		{"entry over namespace", "schema_version: 1\nprompt_cache:\n  defaults:\n    max_namespace_size: \"1GiB\"\n    max_entry_size: \"2GiB\"\n", "cannot exceed max_namespace_size"},
		{"namespace over total", "schema_version: 1\nprompt_cache:\n  max_total_size: \"2GiB\"\n  defaults:\n    max_namespace_size: \"4GiB\"\n", "max_total_size"},
		{"profile without model", "schema_version: 1\nprofiles:\n  p:\n    port: 8080\n", "model is required"},
		{"profile without port", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n", "port is required"},
		{"profile port low", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 80\n", "between 1024 and 65535"},
		{"profile port collision", "schema_version: 1\nprofiles:\n  a:\n    model: \"m.gguf\"\n    port: 8080\n  b:\n    model: \"m2.gguf\"\n    port: 8080\n", "collides"},
		{"profile bad name", "schema_version: 1\nprofiles:\n  bad name:\n    model: \"m.gguf\"\n    port: 8080\n", "name must match"},
		{"profile absolute model", "schema_version: 1\nprofiles:\n  p:\n    model: \"/etc/passwd.gguf\"\n    port: 8080\n", "relative to paths.models_dir"},
		{"profile traversal", "schema_version: 1\nprofiles:\n  p:\n    model: \"../evil.gguf\"\n    port: 8080\n", "inside paths.models_dir"},
		{"profile non-gguf", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.bin\"\n    port: 8080\n", ".gguf"},
		{"profile raises nothing", "schema_version: 1\nprompt_cache:\n  max_total_size: \"2GiB\"\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    prompt_cache:\n      max_namespace_size: \"8GiB\"\n", "max_total_size"},
		{"LLAMA_ARG rejected", "schema_version: 1\nruntime:\n  env:\n    LLAMA_ARG_CTX_SIZE: \"1\"\n", "manager-owned"},
		{"env equals sign", "schema_version: 1\nruntime:\n  env:\n    \"A=B\": \"x\"\n", "invalid environment"},
		{"context zero", "schema_version: 1\nlaunch_defaults:\n  context_size: 0\n", "context_size must be positive"},
		{"gpu negative", "schema_version: 1\nlaunch_defaults:\n  gpu_layers: -1\n", "cannot be negative"},
		{"empty arg", "schema_version: 1\nlaunch_defaults:\n  args: [\"\"]\n", "empty items"},
		{"same dirs", "schema_version: 1\npaths:\n  data_dir: \"/srv/state\"\n  models_dir: \"/srv/state\"\n", "cannot be the same"},
		{"listen no colon", "schema_version: 1\nhttp:\n  listen: \"8090\"\n", "host:port"},
		{"launch env LLAMA_ARG", "schema_version: 1\nlaunch_defaults:\n  env:\n    LLAMA_ARG_HOST: \"x\"\n", "manager-owned"},
		{"defaults ns bad size", "schema_version: 1\nprompt_cache:\n  defaults:\n    max_namespace_size: \"1G\"\n", "not a size"},
		{"defaults entry bad size", "schema_version: 1\nprompt_cache:\n  defaults:\n    max_entry_size: \"1G\"\n", "not a size"},
		{"defaults negative reuse", "schema_version: 1\nprompt_cache:\n  defaults:\n    min_reuse_tokens: -1\n", "cannot be negative"},
		{"profile context zero", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    context_size: 0\n", "context_size must be positive"},
		{"profile env LLAMA_ARG", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    env:\n      LLAMA_ARG_GPU: \"1\"\n", "manager-owned"},
		{"profile env bad name", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    env:\n      \"A=B\": \"x\"\n", "invalid environment"},
		{"profile ns bad size", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    prompt_cache:\n      max_namespace_size: \"1G\"\n", "not a size"},
		{"profile entry bad size", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    prompt_cache:\n      max_entry_size: \"1G\"\n", "not a size"},
		{"profile bad ttl", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    prompt_cache:\n      ttl: \"7d\"\n", "7d"},
		{"profile negative reuse", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    prompt_cache:\n      min_reuse_tokens: -2\n", "cannot be negative"},
		{"profile entry over ns", "schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 8080\n    prompt_cache:\n      max_namespace_size: \"1GiB\"\n      max_entry_size: \"2GiB\"\n", "cannot exceed max_namespace_size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.src))
			if err != nil {
				// A parse-stage rejection with the same message is fine too.
				if strings.Contains(err.Error(), tc.want) {
					return
				}
				t.Fatalf("Parse = %v, want Resolve error containing %q", err, tc.want)
			}
			_, err = cfg.Resolve("/etc/opentambo")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Resolve = %v, want error containing %q", err, tc.want)
			}
		})
	}
}

func TestExplicitFalseAndZeroSurvive(t *testing.T) {
	// An explicit zero min_reuse_tokens and explicit false enabled must be
	// distinguishable from omitted fields.
	eff := resolve(t, `
schema_version: 1
prompt_cache:
  defaults:
    enabled: false
    min_reuse_tokens: 0
`)
	if eff.Cache.MinReuseTokens != 0 {
		t.Fatalf("explicit zero lost: %+v", eff.Cache)
	}
	// And explicit true enables.
	eff = resolve(t, "schema_version: 1\nprompt_cache:\n  defaults:\n    enabled: true\n")
	if !eff.Cache.Enabled {
		t.Fatal("explicit true lost")
	}
}

func TestRelativePathsResolveAgainstFileDir(t *testing.T) {
	eff := resolve(t, `
schema_version: 1
paths:
  data_dir: "state"
runtime:
  executable: "bin/llama-server"
`)
	if eff.DataDir != "/etc/opentambo/state" {
		t.Errorf("DataDir = %q", eff.DataDir)
	}
	if eff.ModelsDir != "/etc/opentambo/state/models" {
		t.Errorf("ModelsDir = %q", eff.ModelsDir)
	}
	if eff.Executable != "/etc/opentambo/bin/llama-server" {
		t.Errorf("Executable = %q", eff.Executable)
	}
	// Bare basenames stay PATH-resolved.
	eff = resolve(t, "schema_version: 1\nruntime:\n  executable: \"llama-server\"\n")
	if eff.Executable != "llama-server" {
		t.Errorf("bare executable = %q", eff.Executable)
	}
}

func TestProfileInheritsAndOverridesLaunchDefaults(t *testing.T) {
	eff := resolve(t, `
schema_version: 1
launch_defaults:
  context_size: 4096
  gpu_layers: 9
  env:
    SHARED: "host"
prompt_cache:
  defaults:
    min_reuse_tokens: 64
profiles:
  big:
    model: "m.gguf"
    port: 8081
    gpu_layers: 0
    prompt_cache:
      min_reuse_tokens: 128
    autostart: true
  plain:
    model: "m.gguf"
    port: 8082
`)
	big := eff.Profiles["big"]
	if big.GPULayers != 0 || big.Context != 4096 {
		t.Errorf("profile gpu_layers override not applied: %+v", big)
	}
	if big.Cache.MinReuseTokens != 128 {
		t.Errorf("profile min_reuse_tokens = %d, want 128", big.Cache.MinReuseTokens)
	}
	if !big.Autostart {
		t.Error("autostart not carried")
	}
	plain := eff.Profiles["plain"]
	if plain.GPULayers != 9 {
		t.Errorf("plain profile did not inherit gpu_layers: %+v", plain)
	}
	if plain.Env["SHARED"] != "host" {
		t.Errorf("profile did not inherit launch_defaults.env: %v", plain.Env)
	}
	if plain.Cache.MinReuseTokens != 64 {
		t.Errorf("plain profile did not inherit host cache defaults: %+v", plain.Cache)
	}
}

func TestResolveWithoutFileDirUsesManualDataDir(t *testing.T) {
	cfg, err := Parse([]byte("schema_version: 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	eff, err := cfg.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if eff.DataDir != "./data" || eff.ModelsDir != "data/models" {
		t.Errorf("empty file dir = %q %q, want ./data paths", eff.DataDir, eff.ModelsDir)
	}
}

func TestParseSize(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
	}{
		{"1B", 1}, {"512KiB", 512 << 10}, {"2GiB", 2 << 30}, {"1TiB", 1 << 40},
	} {
		got, err := ParseSize(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseSize(%q) = %d, %v", tc.in, got, err)
		}
	}
	for _, bad := range []string{"", "10", "10GB", "10 gib", "-1GiB", "8388608TiB", "1MB", "9223372036854775808B"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) accepted", bad)
		}
	}
}

func TestDiscoverySelection(t *testing.T) {
	dir := t.TempDir()
	sys := filepath.Join(dir, "etc/opentambo/config.yaml")
	env := map[string]string{}

	// Nothing anywhere: manual mode.
	d := Discovery{Env: envGet(env), SystemPath: sys}
	if eff, err := d.Load(); eff != nil || err != nil {
		t.Fatalf("manual mode = %v, %v", eff, err)
	}
	// Implicit system file is picked up when present.
	if err := os.MkdirAll(filepath.Dir(sys), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sys, []byte("schema_version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	eff, err := d.Load()
	if err != nil || eff == nil || eff.Path != sys {
		t.Fatalf("system file = %v, %v", eff, err)
	}
	// Environment selects a different file.
	other := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(other, []byte("schema_version: 1\nhttp:\n  listen: \"127.0.0.1:9\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	env["OPENTAMBO_CONFIG"] = other
	eff, err = d.Load()
	if err != nil || eff.Listen != "127.0.0.1:9" {
		t.Fatalf("env selection = %v, %v", eff, err)
	}
	// Explicit --config wins over the environment.
	flag := filepath.Join(dir, "flag.yaml")
	if err := os.WriteFile(flag, []byte("schema_version: 1\nhttp:\n  listen: \"127.0.0.1:8\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	d.ConfigFlag = flag
	eff, err = d.Load()
	if err != nil || eff.Listen != "127.0.0.1:8" {
		t.Fatalf("flag selection = %v, %v", eff, err)
	}
	// An explicitly selected missing file is an error, not manual mode.
	d.ConfigFlag = filepath.Join(dir, "missing.yaml")
	if _, err := d.Load(); err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("missing explicit config = %v", err)
	}
}

func envGet(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadFileReportsPathAndField(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(p, []byte("schema_version: 1\nprofiles:\n  p:\n    model: \"m.gguf\"\n    port: 99\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(p)
	if err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "between 1024") {
		t.Fatalf("LoadFile error = %v", err)
	}
}
