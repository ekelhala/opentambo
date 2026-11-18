package desk

// Prompt-cache identity. Snapshots are only compatible when the exact model
// content (all shards), the llama-server binary, and the state-affecting
// launch configuration all match. The namespace is a stable hash across run
// IDs so restarting a run (or the manager) finds previous snapshots. Replacing
// a model file under the same alias changes the content hash and therefore
// invalidates the namespace conservatively.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const identityHashTag = "opentambo-prompt-cache-identity-v3\n"

// hashIdentityField writes one length-prefixed field into the identity hash.
// Newline-delimited key=value text is ambiguous: a value containing a newline
// (valid in process environments and argv) can splice into the next field so
// two distinct configurations hash alike (issue #20). Framing every field as
// kind[len:value] pins each field's byte span, making the concatenation of
// any sequence of fields injective.
func hashIdentityField(h io.Writer, kind, value string) {
	fmt.Fprintf(h, "%s[%d]:", kind, len(value))
	_, _ = io.WriteString(h, value)
	_, _ = io.WriteString(h, "\n")
}

// verifiedCacheArchitectures lists GGUF architectures that passed a real
// save/restart/restore/reuse test against a supported llama-server build.
// Hybrid/recurrent and sliding-window architectures are NOT proven effective
// and stay unsupported until validated (see README).
var verifiedCacheArchitectures = map[string]bool{"llama": true}

// shardPattern matches the llama.cpp split GGUF naming convention.
var shardPattern = regexp.MustCompile(`^(.*)-\d{5}-of-(\d{5})(\.gguf)$`)

// cacheLaunch carries prepared prompt-cache state into the launch path.
type cacheLaunch struct {
	namespace string
	dir       string
	spec      PromptCache
	store     *cacheStore
}

// preparePromptCache validates configuration, checks the model architecture
// gate, and derives the namespace directory. It performs model file hashing
// outside App.mu; only the small result crosses into the locked section.
func (a *App) preparePromptCache(spec Launch) (*cacheLaunch, error) {
	if err := validatePromptCache(spec.PromptCache); err != nil {
		return nil, err
	}
	for _, arg := range spec.Args {
		name := strings.SplitN(arg, "=", 2)[0]
		switch name {
		case "--slot-save-path", "-ssp", "--cache-ram", "-cram":
			return nil, fmt.Errorf("%s is managed by the prompt cache; remove it from the launch arguments", name)
		}
	}
	model, err := a.modelPath(spec.Model)
	if err != nil {
		return nil, err
	}
	metadata, err := readGGUFMetadata(model)
	if err != nil {
		return nil, fmt.Errorf("prompt cache needs readable GGUF metadata: %w", err)
	}
	architecture, _ := metadata["general.architecture"].(string)
	if architecture == "" {
		return nil, fmt.Errorf("prompt cache needs the model architecture in GGUF metadata")
	}
	if !verifiedCacheArchitectures[architecture] {
		return nil, fmt.Errorf("prompt cache does not support architecture %q yet: it has no validated save/restore/reuse evidence (supported: %s)", architecture, strings.Join(verifiedArchitectureNames(), ", "))
	}
	namespace, err := a.promptCacheIdentity(model, architecture, spec)
	if err != nil {
		return nil, err
	}
	normalized := normalizedPromptCache(*spec.PromptCache)
	dir := filepath.Join(a.data, "prompt-cache", namespace)
	store, err := a.cacheStoreFor(dir, namespace, normalized)
	if err != nil {
		return nil, fmt.Errorf("prompt cache directory: %w", err)
	}
	return &cacheLaunch{namespace: namespace, dir: dir, spec: normalized, store: store}, nil
}

func verifiedArchitectureNames() []string {
	names := make([]string, 0, len(verifiedCacheArchitectures))
	for name := range verifiedCacheArchitectures {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// resolvedBinary maps the configured llama-server executable to the absolute
// path the OS would execute. A bare basename (the CLI default) resolves
// through PATH exactly like exec.CommandContext does at launch; both the
// launch path and cache identity hashing must agree on the same file.
func (a *App) resolvedBinary() (string, error) {
	if strings.ContainsRune(a.binary, os.PathSeparator) {
		return filepath.Abs(a.binary)
	}
	return exec.LookPath(a.binary)
}

// promptCacheIdentity hashes model content (including split shards), the
// llama-server binary, and the state-affecting launch configuration.
func (a *App) promptCacheIdentity(model, architecture string, spec Launch) (string, error) {
	files := []string{model}
	if match := shardPattern.FindStringSubmatch(filepath.Base(model)); match != nil {
		matches, err := filepath.Glob(filepath.Join(filepath.Dir(model), match[1]+"-*"+match[3]))
		if err != nil {
			return "", err
		}
		for _, candidate := range matches {
			if candidate != model {
				files = append(files, candidate)
			}
		}
	}
	sort.Strings(files)
	hash := sha256.New()
	hash.Write([]byte(identityHashTag))
	hashIdentityField(hash, "arch", architecture)
	for _, file := range files {
		digest, err := a.fileDigest(file)
		if err != nil {
			return "", err
		}
		hashIdentityField(hash, "file", filepath.Base(file))
		hashIdentityField(hash, "sha256", digest)
	}
	binaryPath, err := a.resolvedBinary()
	if err != nil {
		return "", fmt.Errorf("prompt cache cannot resolve the llama-server executable: %w", err)
	}
	digest, err := a.fileDigest(binaryPath)
	if err != nil {
		return "", err
	}
	hashIdentityField(hash, "binary", digest)
	effective, err := a.stateAffectingArgs(spec)
	if err != nil {
		return "", err
	}
	// argv keeps its original order and flag/value adjacency; the length
	// prefix means a value containing newlines cannot merge with the
	// following argument.
	for _, entry := range effective {
		hashIdentityField(hash, "arg", entry)
	}
	// Environment overrides can change engine state in ways the manager
	// cannot audit; any difference invalidates the namespace conservatively.
	// Keys and values are framed separately so delimiter-like or multiline
	// content in one environment cannot serialize into another (issue #20).
	envKeys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)
	for _, key := range envKeys {
		hashIdentityField(hash, "env-key", key)
		hashIdentityField(hash, "env-value", spec.Env[key])
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}

// stateAffectingArgs keeps configuration that can change KV state or prompt
// rendering out of alias/port churn. The effective argv keeps its original
// order and flag/value adjacency (sorting individual tokens lets two
// different settings swap into the same identity), and any argument value
// that names an existing regular file — adapters, draft models, projections,
// control vectors, grammar or template files — is bound by content digest so
// replacing the file under the same path invalidates the namespace. Unknown
// arguments are included as-is; anything ambiguous invalidates snapshots.
func (a *App) stateAffectingArgs(spec Launch) ([]string, error) {
	volatile := map[string]bool{"--port": true, "--host": true, "--alias": true, "--model": true, "-m": true, "-p": true}
	out := []string{fmt.Sprintf("ctx=%d", spec.Context), fmt.Sprintf("gpu=%d", spec.GPULayers)}
	skipValue := false
	for _, arg := range spec.Args {
		if skipValue {
			// The value of a volatile flag in separate-token form churns
			// without touching model state.
			skipValue = false
			continue
		}
		name := strings.SplitN(arg, "=", 2)[0]
		if volatile[name] {
			skipValue = !strings.Contains(arg, "=")
			continue
		}
		out = append(out, arg)
		value := arg
		if strings.Contains(arg, "=") {
			value = arg[strings.IndexByte(arg, '=')+1:]
		} else if strings.HasPrefix(arg, "-") {
			continue // bare flag; a following value token is bound on its own turn
		}
		if strings.HasPrefix(value, "-") || len(value) > 4096 {
			continue
		}
		info, err := os.Stat(value)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		digest, err := a.fileDigest(value)
		if err != nil {
			return nil, fmt.Errorf("prompt cache cannot hash referenced asset %s: %w", value, err)
		}
		out = append(out, "asset="+filepath.Base(value)+":"+digest)
	}
	return out, nil
}

// fileDigest content-addresses files with an on-disk cache keyed by path,
// size, and modification time so repeated launches do not re-hash gigabytes.
func (a *App) fileDigest(path string) (string, error) {
	fileDigests.mu.Lock()
	defer fileDigests.mu.Unlock()
	if err := fileDigests.load(a.data); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := path
	entry := fileDigests.entries[key]
	if entry != nil && entry.Size == info.Size() && entry.MTime == info.ModTime().UnixNano() {
		return entry.SHA256, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	fileDigests.entries[key] = &digestRecord{Size: info.Size(), MTime: info.ModTime().UnixNano(), SHA256: digest}
	if err := fileDigests.save(a.data); err != nil {
		return "", err
	}
	return digest, nil
}

type digestRecord struct {
	Size   int64
	MTime  int64
	SHA256 string
}

type digestCache struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]*digestRecord
}

var fileDigests = &digestCache{entries: map[string]*digestRecord{}}

func (c *digestCache) load(data string) error {
	if c.loaded {
		return nil
	}
	c.loaded = true
	contents, err := os.ReadFile(filepath.Join(data, "prompt-cache", "digests.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var entries map[string]*digestRecord
	if err := json.Unmarshal(contents, &entries); err != nil {
		return nil // a corrupt digest cache only costs re-hashing
	}
	if entries != nil {
		c.entries = entries
	}
	return nil
}

func (c *digestCache) save(data string) error {
	contents, err := json.Marshal(c.entries)
	if err != nil {
		return err
	}
	dir := filepath.Join(data, "prompt-cache")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	path := filepath.Join(dir, "digests.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, contents, 0600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
