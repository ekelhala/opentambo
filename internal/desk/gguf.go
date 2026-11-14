package desk

// Minimal GGUF metadata reader. The prompt-cache feature only supports model
// architectures validated by a real save/restart/restore/reuse test, so the
// launch path reads the architecture (and shard identity hints) from the
// GGUF metadata header without loading the model.

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

const ggufMetadataLimit = 32 << 20

type ggufReader struct {
	file  *os.File
	ended bool
}

func readGGUFMetadata(path string) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	r := &ggufReader{file: file}
	magic := make([]byte, 4)
	if _, err = io.ReadFull(r, magic); err != nil || string(magic) != "GGUF" {
		return nil, fmt.Errorf("%s is not a GGUF file", path)
	}
	var version uint32
	if err = binary.Read(r, binary.LittleEndian, &version); err != nil {
		return nil, err
	}
	if version < 2 || version > 3 {
		return nil, fmt.Errorf("unsupported GGUF version %d", version)
	}
	var tensorCount, kvCount uint64
	if err = binary.Read(r, binary.LittleEndian, &tensorCount); err != nil {
		return nil, err
	}
	if err = binary.Read(r, binary.LittleEndian, &kvCount); err != nil {
		return nil, err
	}
	if kvCount > 1_000_000 {
		return nil, fmt.Errorf("implausible GGUF metadata count %d", kvCount)
	}
	metadata := map[string]any{}
	for i := uint64(0); i < kvCount; i++ {
		key, err := r.readString()
		if err != nil {
			return nil, err
		}
		value, err := r.readValue()
		if err != nil {
			return nil, err
		}
		metadata[key] = value
	}
	return metadata, nil
}

func (r *ggufReader) Read(p []byte) (int, error) {
	if r.ended {
		return 0, io.ErrUnexpectedEOF
	}
	// Metadata lives before the tensor info/data sections; guard against
	// malformed files by refusing reads past the metadata allowance.
	if int64(len(p)) > ggufMetadataLimit {
		return 0, io.EOF
	}
	return r.file.Read(p)
}

func (r *ggufReader) readString() (string, error) {
	var length uint64
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return "", err
	}
	if length > ggufMetadataLimit {
		return "", fmt.Errorf("GGUF string of %d bytes is too large", length)
	}
	buffer := make([]byte, length)
	if _, err := io.ReadFull(r, buffer); err != nil {
		return "", err
	}
	return string(buffer), nil
}

func (r *ggufReader) readValue() (any, error) {
	var kind uint32
	if err := binary.Read(r, binary.LittleEndian, &kind); err != nil {
		return nil, err
	}
	return r.valueOf(kind)
}

func (r *ggufReader) valueOf(kind uint32) (any, error) {
	switch kind {
	case 0:
		var v uint8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 1:
		var v int8
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 2:
		var v uint16
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 3:
		var v int16
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 4:
		var v uint32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 5:
		var v int32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 6:
		var v float32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 7:
		var v bool
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	case 8:
		return r.readString()
	case 9:
		var arrayKind uint32
		var count uint64
		if err := binary.Read(r, binary.LittleEndian, &arrayKind); err != nil {
			return nil, err
		}
		if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
			return nil, err
		}
		if count > 1_000_000 {
			return nil, fmt.Errorf("GGUF array of %d items is too large", count)
		}
		// Elements carry no per-item type tag; the array type applies to all.
		var last any
		for i := uint64(0); i < count; i++ {
			value, err := r.valueOf(arrayKind)
			if err != nil {
				return nil, err
			}
			last = value
		}
		return last, nil
	case 10:
		var v uint64
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	default:
		return nil, fmt.Errorf("unknown GGUF value type %d", kind)
	}
}
