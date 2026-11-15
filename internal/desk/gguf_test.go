package desk

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// ggufFixture builds a minimal GGUF v3 file holding only a metadata section,
// exercising metadata parsing rather than full inference-model validity.
func ggufFixture(t *testing.T, kv []byte, kvCount uint64) string {
	t.Helper()
	var out bytes.Buffer
	out.WriteString("GGUF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(3))
	_ = binary.Write(&out, binary.LittleEndian, uint64(0)) // tensor count
	_ = binary.Write(&out, binary.LittleEndian, kvCount)
	out.Write(kv)
	path := filepath.Join(t.TempDir(), "fixture.gguf")
	if err := os.WriteFile(path, out.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ggufString(s string) []byte {
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, uint64(len(s)))
	out.WriteString(s)
	return out.Bytes()
}

func ggufKV(t *testing.T, key string, kind uint32, payload []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	out.Write(ggufString(key))
	_ = binary.Write(&out, binary.LittleEndian, kind)
	out.Write(payload)
	return out.Bytes()
}

func TestReadGGUFAcceptsEveryStandardScalarType(t *testing.T) {
	cases := []struct {
		name    string
		kind    uint32
		payload any
	}{
		{"uint8", 0, uint8(255)},
		{"int8", 1, int8(-3)},
		{"uint16", 2, uint16(65535)},
		{"int16", 3, int16(-9)},
		{"uint32", 4, uint32(4294967295)},
		{"int32", 5, int32(-42)},
		{"float32", 6, float32(1.5)},
		{"bool", 7, true},
		{"uint64", 10, uint64(18446744073709551615)},
		{"int64", 11, int64(-9223372036854775808)},
		{"float64", 12, float64(math.Pi)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var payload bytes.Buffer
			_ = binary.Write(&payload, binary.LittleEndian, tc.payload)
			kv := ggufKV(t, "general.architecture", 8, ggufString("llama"))
			kv = append(kv, ggufKV(t, "custom.scalar", tc.kind, payload.Bytes())...)
			metadata, err := readGGUFMetadata(ggufFixture(t, kv, 2))
			if err != nil {
				t.Fatalf("type %d (%s) rejected: %v", tc.kind, tc.name, err)
			}
			if metadata["general.architecture"] != "llama" {
				t.Fatalf("architecture lost: %v", metadata)
			}
			switch tc.kind {
			case 6:
				if got, ok := metadata["custom.scalar"].(float32); !ok || got != float32(1.5) {
					t.Fatalf("float32 round trip: %v", metadata["custom.scalar"])
				}
			case 11:
				if got, ok := metadata["custom.scalar"].(int64); !ok || got != math.MinInt64 {
					t.Fatalf("int64 round trip: %v", metadata["custom.scalar"])
				}
			case 12:
				if got, ok := metadata["custom.scalar"].(float64); !ok || got != math.Pi {
					t.Fatalf("float64 round trip: %v", metadata["custom.scalar"])
				}
			}
		})
	}
}

func TestReadGGUFAcceptsArraysOfLargeScalars(t *testing.T) {
	for _, tc := range []struct {
		kind  uint32
		value any
	}{
		{11, int64(-42)},
		{12, float64(2.5)},
	} {
		var elements bytes.Buffer
		for i := 0; i < 3; i++ {
			_ = binary.Write(&elements, binary.LittleEndian, tc.value)
		}
		var array bytes.Buffer
		_ = binary.Write(&array, binary.LittleEndian, tc.kind)
		_ = binary.Write(&array, binary.LittleEndian, uint64(3))
		array.Write(elements.Bytes())
		kv := ggufKV(t, "general.architecture", 8, ggufString("llama"))
		kv = append(kv, ggufKV(t, "custom.array", 9, array.Bytes())...)
		metadata, err := readGGUFMetadata(ggufFixture(t, kv, 2))
		if err != nil {
			t.Fatalf("array of type %d rejected: %v", tc.kind, err)
		}
		if metadata["general.architecture"] != "llama" {
			t.Fatalf("architecture lost: %v", metadata)
		}
	}
}

func TestReadGGMalformedAndUnknownTypes(t *testing.T) {
	t.Run("truncated int64", func(t *testing.T) {
		var payload bytes.Buffer
		_ = binary.Write(&payload, binary.LittleEndian, int32(1)) // four bytes, not eight
		kv := ggufKV(t, "custom.scalar", 11, payload.Bytes())
		if _, err := readGGUFMetadata(ggufFixture(t, kv, 1)); err == nil {
			t.Fatal("accepted a truncated INT64 value")
		}
	})
	t.Run("truncated float64", func(t *testing.T) {
		kv := ggufKV(t, "custom.scalar", 12, []byte{0x00, 0x01, 0x02})
		if _, err := readGGUFMetadata(ggufFixture(t, kv, 1)); err == nil {
			t.Fatal("accepted a truncated FLOAT64 value")
		}
	})
	t.Run("unknown type", func(t *testing.T) {
		kv := ggufKV(t, "custom.scalar", 13, []byte{0})
		if _, err := readGGUFMetadata(ggufFixture(t, kv, 1)); err == nil {
			t.Fatal("accepted an unknown value type")
		}
	})
	t.Run("architecture survives unused valid scalars", func(t *testing.T) {
		var i64, f64 bytes.Buffer
		_ = binary.Write(&i64, binary.LittleEndian, int64(7))
		_ = binary.Write(&f64, binary.LittleEndian, float64(7))
		kv := ggufKV(t, "general.architecture", 8, ggufString("llama"))
		kv = append(kv, ggufKV(t, "custom.a", 11, i64.Bytes())...)
		kv = append(kv, ggufKV(t, "custom.b", 12, f64.Bytes())...)
		metadata, err := readGGUFMetadata(ggufFixture(t, kv, 3))
		if err != nil {
			t.Fatal(err)
		}
		if metadata["general.architecture"] != "llama" {
			t.Fatalf("compatible model rejected over unused fields: %v", metadata)
		}
	})
}
