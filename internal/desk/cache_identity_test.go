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
