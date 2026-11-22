package desk

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeArchitectureGGUF(t *testing.T, path, architecture string) {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("GGUF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(3))
	_ = binary.Write(&out, binary.LittleEndian, uint64(0))
	_ = binary.Write(&out, binary.LittleEndian, uint64(1))
	out.Write(ggufString("general.architecture"))
	_ = binary.Write(&out, binary.LittleEndian, uint32(8))
	out.Write(ggufString(architecture))
	if err := os.WriteFile(path, out.Bytes(), 0640); err != nil {
		t.Fatal(err)
	}
}

// TestPreparePromptCacheAcceptsAnyArchitecture pins the measured-capability
// design: no architecture is rejected at launch. Hybrid/recurrent models
// reach the child and the runtime probe decides. Only the identity input
// (the architecture string itself) is required.
func TestPreparePromptCacheAcceptsAnyArchitecture(t *testing.T) {
	a := testApp(t, filepath.Join(t.TempDir(), "llama-server"))
	if err := os.WriteFile(a.binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.models, "org"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, architecture := range []string{"llama", "qwen2", "qwen3next", "lfm2", "kimi_linear"} {
		name := "model-" + architecture + ".gguf"
		writeArchitectureGGUF(t, filepath.Join(a.models, "org", name), architecture)
		prepared, err := a.preparePromptCache(Launch{
			Model: "org/" + name, Context: 8192, GPULayers: 99,
			PromptCache: &PromptCache{Enabled: true},
		})
		if err != nil {
			t.Errorf("%s: architecture must not be gated at launch: %v", architecture, err)
			continue
		}
		if prepared.namespace == "" {
			t.Errorf("%s: missing namespace", architecture)
		}
	}
	// An empty architecture string is still rejected: it is a mandatory
	// identity input, and its absence means the metadata is unusable.
	writeArchitectureGGUF(t, filepath.Join(a.models, "org", "blank.gguf"), "")
	if _, err := a.preparePromptCache(Launch{
		Model: "org/blank.gguf", Context: 8192, GPULayers: 99,
		PromptCache: &PromptCache{Enabled: true},
	}); err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("blank architecture must report the missing identity input, got %v", err)
	}
}

func TestPreparePromptCacheValidatesBeforeTouchingFiles(t *testing.T) {
	a := testApp(t, filepath.Join(t.TempDir(), "llama-server"))
	// Negative limits are rejected before any model file needs to exist.
	if _, err := a.preparePromptCache(Launch{
		Model: "missing.gguf", Context: 8192, GPULayers: 99,
		PromptCache: &PromptCache{Enabled: true, MaxBytes: -5},
	}); err == nil {
		t.Fatal("negative cache limit accepted")
	}
	// Manager-owned cache arguments cannot sneak in through launch args.
	for _, arg := range []string{"--slot-save-path", "-ssp", "--cache-ram=0", "-cram"} {
		_, err := a.preparePromptCache(Launch{
			Model: "missing.gguf", Context: 8192, GPULayers: 99,
			Args:        []string{arg},
			PromptCache: &PromptCache{Enabled: true},
		})
		if err == nil || !strings.Contains(err.Error(), "managed by the prompt cache") {
			t.Errorf("%s: expected managed-argument rejection, got %v", arg, err)
		}
	}
	// A model outside the shelf fails with the normal path error.
	if _, err := a.preparePromptCache(Launch{
		Model: "../escape.gguf", Context: 8192, GPULayers: 99,
		PromptCache: &PromptCache{Enabled: true},
	}); err == nil {
		t.Fatal("path escape accepted")
	}
	// A model without GGUF metadata is rejected with the dedicated message.
	if err := os.MkdirAll(filepath.Join(a.models, "org"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.models, "org", "junk.gguf"), []byte("not a gguf"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := a.preparePromptCache(Launch{
		Model: "org/junk.gguf", Context: 8192, GPULayers: 99,
		PromptCache: &PromptCache{Enabled: true},
	}); err == nil || !strings.Contains(err.Error(), "GGUF metadata") {
		t.Fatalf("unreadable metadata must report clearly, got %v", err)
	}
}

func TestPreparePromptCacheHappyPathDerivesStore(t *testing.T) {
	a := testApp(t, filepath.Join(t.TempDir(), "llama-server"))
	if err := os.WriteFile(a.binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.models, "org"), 0755); err != nil {
		t.Fatal(err)
	}
	writeMinimalGGUF(t, filepath.Join(a.models, "org", "model.gguf"))
	prepared, err := a.preparePromptCache(Launch{
		Model: "org/model.gguf", Context: 8192, GPULayers: 99,
		PromptCache: &PromptCache{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.namespace == "" || prepared.store == nil {
		t.Fatalf("incomplete preparation: %+v", prepared)
	}
	if prepared.dir != filepath.Join(a.data, "prompt-cache", prepared.namespace) {
		t.Errorf("namespace dir %s is not under the data directory", prepared.dir)
	}
	if prepared.spec.MaxBytes == 0 {
		t.Error("limits were not normalized")
	}
	// Same configuration yields the same namespace across preparations.
	again, err := a.preparePromptCache(Launch{
		Model: "org/model.gguf", Context: 8192, GPULayers: 99,
		PromptCache: &PromptCache{Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.namespace != prepared.namespace {
		t.Errorf("namespace unstable across preparations: %s vs %s", prepared.namespace, again.namespace)
	}
}
