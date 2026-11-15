package desk

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func writeMinimalGGUF(t *testing.T, path string) {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("GGUF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(3))
	_ = binary.Write(&out, binary.LittleEndian, uint64(0))
	_ = binary.Write(&out, binary.LittleEndian, uint64(1))
	out.Write(ggufString("general.architecture"))
	_ = binary.Write(&out, binary.LittleEndian, uint32(8))
	out.Write(ggufString("llama"))
	if err := os.WriteFile(path, out.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
}

func TestPromptCacheIdentityResolvesPathExecutable(t *testing.T) {
	a := testApp(t, "llama-server")
	model := filepath.Join(a.models, "model.gguf")
	writeMinimalGGUF(t, model)

	// A PATH-resolvable basename, exactly like the documented default.
	binDir := t.TempDir()
	fake := filepath.Join(binDir, "llama-server")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	spec := Launch{Context: 8192, GPULayers: 99}
	fromPath, err := a.promptCacheIdentity(model, "llama", spec)
	if err != nil {
		t.Fatalf("PATH basename rejected: %v", err)
	}

	// The same file addressed absolutely must yield the identical namespace.
	a.binary = fake
	fromAbsolute, err := a.promptCacheIdentity(model, "llama", spec)
	if err != nil {
		t.Fatalf("absolute path rejected: %v", err)
	}
	if fromPath != fromAbsolute {
		t.Fatalf("PATH basename and absolute path produced different namespaces: %s vs %s", fromPath, fromAbsolute)
	}

	// A missing executable must fail loudly rather than hashing a
	// working-directory-relative path.
	t.Setenv("PATH", t.TempDir())
	a.binary = "llama-server-nowhere"
	if _, err = a.promptCacheIdentity(model, "llama", spec); err == nil {
		t.Fatal("accepted an unresolvable executable")
	}
}

func TestPromptCacheIdentityDistinguishesFlagValueAssociations(t *testing.T) {
	a := testApp(t, t.TempDir()+"/llama-server")
	if err := os.WriteFile(a.binary, []byte("binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(a.models, "model.gguf")
	writeMinimalGGUF(t, model)
	base := Launch{Context: 8192, GPULayers: 99}

	// Distinct RoPE settings must not collide regardless of value order.
	one := base
	one.Args = []string{"--rope-freq-base", "10000", "--rope-freq-scale", "2"}
	two := base
	two.Args = []string{"--rope-freq-base", "2", "--rope-freq-scale", "10000"}
	first, err := a.promptCacheIdentity(model, "llama", one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.promptCacheIdentity(model, "llama", two)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("swapped flag/value pairs produced an identical namespace")
	}
	// Repeating the identical configuration must stay stable (control).
	repeat, err := a.promptCacheIdentity(model, "llama", one)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != first {
		t.Fatal("identical configuration changed the namespace")
	}
	// Repeated flags stay distinct from a single occurrence and stay stable.
	dup := one
	dup.Args = []string{"--rope-freq-base", "10000", "--rope-freq-base", "10000", "--rope-freq-scale", "2"}
	dupID, err := a.promptCacheIdentity(model, "llama", dup)
	if err != nil {
		t.Fatal(err)
	}
	dupAgain, err := a.promptCacheIdentity(model, "llama", dup)
	if err != nil {
		t.Fatal(err)
	}
	if dupID != dupAgain {
		t.Fatal("repeated-flag configuration is not stable")
	}
	// Alias/port churn stays reusable.
	volatileA := base
	volatileA.Args = []string{"--port", "8081", "--alias", "one", "--jinja"}
	volatileB := base
	volatileB.Args = []string{"--port", "8099", "--alias", "two", "--jinja"}
	idA, err := a.promptCacheIdentity(model, "llama", volatileA)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := a.promptCacheIdentity(model, "llama", volatileB)
	if err != nil {
		t.Fatal(err)
	}
	if idA != idB {
		t.Fatal("alias/port churn invalidated a reusable namespace")
	}
	// Environment overrides invalidate conservatively.
	withEnv := base
	withEnv.Env = map[string]string{"GGML_CUDA_FORCE_MMQ": "1"}
	envID, err := a.promptCacheIdentity(model, "llama", withEnv)
	if err != nil {
		t.Fatal(err)
	}
	if envID == first || envID == second {
		t.Fatal("environment override did not invalidate the namespace")
	}
}

func TestPromptCacheIdentityEnvironmentSerializationIsUnambiguous(t *testing.T) {
	a := testApp(t, t.TempDir()+"/llama-server")
	if err := os.WriteFile(a.binary, []byte("binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(a.models, "model.gguf")
	writeMinimalGGUF(t, model)
	base := Launch{Context: 8192, GPULayers: 99}

	// Issue #20 reproduction: two valid, distinct process environments that
	// newline-delimited "env=key=value" lines serialize identically.
	one := base
	one.Env = map[string]string{"A": "x\nenv=B=y"}
	two := base
	two.Env = map[string]string{"A": "x", "B": "y"}
	first, err := a.promptCacheIdentity(model, "llama", one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.promptCacheIdentity(model, "llama", two)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("newline in an environment value merged into a second entry")
	}
	// Distinct keys whose names embed delimiters must not collide either.
	three := base
	three.Env = map[string]string{"A\nenv=B": "y"}
	third, err := a.promptCacheIdentity(model, "llama", three)
	if err != nil {
		t.Fatal(err)
	}
	if third == first || third == second {
		t.Fatal("delimiter-like environment key collided")
	}
	// Stability: identical environments keep the namespace stable.
	repeat, err := a.promptCacheIdentity(model, "llama", two)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != second {
		t.Fatal("identical environment configuration changed the namespace")
	}
}

func TestPromptCacheIdentityArgvSerializationIsUnambiguous(t *testing.T) {
	a := testApp(t, t.TempDir()+"/llama-server")
	if err := os.WriteFile(a.binary, []byte("binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(a.models, "model.gguf")
	writeMinimalGGUF(t, model)
	base := Launch{Context: 8192, GPULayers: 99}

	// One argument containing a newline serializes like two arguments when
	// argv is joined with unescaped "\n" line delimiters.
	one := base
	one.Args = []string{"x\narg=y"}
	two := base
	two.Args = []string{"x", "y"}
	first, err := a.promptCacheIdentity(model, "llama", one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.promptCacheIdentity(model, "llama", two)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("newline in an argv value merged two arguments into one identity")
	}
	repeat, err := a.promptCacheIdentity(model, "llama", one)
	if err != nil {
		t.Fatal(err)
	}
	if repeat != first {
		t.Fatal("identical argv changed the namespace")
	}
}

func TestPromptCacheIdentityBindsAdapterContents(t *testing.T) {
	a := testApp(t, t.TempDir()+"/llama-server")
	if err := os.WriteFile(a.binary, []byte("binary-v1"), 0755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(a.models, "model.gguf")
	writeMinimalGGUF(t, model)
	adapter := filepath.Join(t.TempDir(), "adapter.gguf")
	if err := os.WriteFile(adapter, []byte("adapter-content-v1"), 0600); err != nil {
		t.Fatal(err)
	}
	spec := Launch{Context: 8192, GPULayers: 99, Args: []string{"--lora", adapter}}
	before, err := a.promptCacheIdentity(model, "llama", spec)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(adapter, []byte("adapter-content-v2!"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := a.promptCacheIdentity(model, "llama", spec)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("replacing adapter contents kept the same namespace")
	}
	// Inline --lora=path form is bound the same way.
	inline := Launch{Context: 8192, GPULayers: 99, Args: []string{"--lora=" + adapter}}
	inlineID, err := a.promptCacheIdentity(model, "llama", inline)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(adapter, []byte("adapter-content-v3!!"), 0600); err != nil {
		t.Fatal(err)
	}
	inlineAfter, err := a.promptCacheIdentity(model, "llama", inline)
	if err != nil {
		t.Fatal(err)
	}
	if inlineID == inlineAfter {
		t.Fatal("inline --flag=value form missed the asset digest")
	}
}
