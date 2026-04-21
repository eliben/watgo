package toolchaintests

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/eliben/watgo"
)

const (
	wasmMagic   = "\x00asm"
	wasmVersion = "\x01\x00\x00\x00"

	sectionCustomID byte = 0
	nameSectionName      = "name"
)

func TestGoJSEmptyWASM_PreservesUnknownCustomSections(t *testing.T) {
	// A committed Go js/wasm fixture should preserve opaque non-"name" custom
	// sections byte-for-byte and in order through DecodeWASM -> EncodeWASM.
	original := readFixtureWASM(t, filepath.Join("go-js-empty", "main.wasm"))

	decoded, err := watgo.DecodeWASM(original)
	if err != nil {
		t.Fatalf("DecodeWASM error: %v", err)
	}

	gotNames := make([]string, 0, len(decoded.CustomSections))
	for _, sec := range decoded.CustomSections {
		gotNames = append(gotNames, sec.Name)
	}
	if !slices.Contains(gotNames, "go:buildid") {
		t.Fatalf("decoded unknown custom section names=%v, want go:buildid", gotNames)
	}
	if !slices.Contains(gotNames, "producers") {
		t.Fatalf("decoded unknown custom section names=%v, want producers", gotNames)
	}

	roundTrip, err := watgo.EncodeWASM(decoded)
	if err != nil {
		t.Fatalf("EncodeWASM(decoded) error: %v", err)
	}

	origCustoms := nonNameCustomSectionsFromWASM(t, original)
	roundTripCustoms := nonNameCustomSectionsFromWASM(t, roundTrip)
	if len(origCustoms) == 0 {
		t.Fatal("fixture had no unknown custom sections")
	}
	if len(roundTripCustoms) == 0 {
		t.Fatal("round-tripped wasm had no unknown custom sections")
	}
	if len(origCustoms) != len(roundTripCustoms) {
		t.Fatalf("got %d round-tripped unknown custom sections, want %d", len(roundTripCustoms), len(origCustoms))
	}
	for i := range origCustoms {
		if origCustoms[i].name != roundTripCustoms[i].name {
			t.Fatalf("custom section[%d] name=%q, want %q", i, roundTripCustoms[i].name, origCustoms[i].name)
		}
		if !bytes.Equal(origCustoms[i].payload, roundTripCustoms[i].payload) {
			t.Fatalf("custom section[%d] payload mismatch for %q", i, origCustoms[i].name)
		}
	}
}

type wasmCustomSection struct {
	name    string
	payload []byte
}

type wasmSection struct {
	id      byte
	payload []byte
}

func readFixtureWASM(t *testing.T, relPath string) []byte {
	t.Helper()

	b, err := os.ReadFile(relPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", relPath, err)
	}
	return b
}

func nonNameCustomSectionsFromWASM(t *testing.T, wasm []byte) []wasmCustomSection {
	t.Helper()

	var out []wasmCustomSection
	for _, section := range wasmSections(t, wasm) {
		if section.id != sectionCustomID {
			continue
		}
		name, payload := customSectionName(t, section.payload)
		if name == nameSectionName {
			continue
		}
		out = append(out, wasmCustomSection{name: name, payload: payload})
	}
	return out
}

func wasmSections(t *testing.T, wasm []byte) []wasmSection {
	t.Helper()

	if len(wasm) < len(wasmMagic)+len(wasmVersion) {
		t.Fatal("short wasm")
	}
	if string(wasm[:len(wasmMagic)]) != wasmMagic {
		t.Fatal("bad wasm magic")
	}
	if string(wasm[len(wasmMagic):len(wasmMagic)+len(wasmVersion)]) != wasmVersion {
		t.Fatal("bad wasm version")
	}

	r := bytes.NewReader(wasm[len(wasmMagic)+len(wasmVersion):])
	var sections []wasmSection
	for r.Len() > 0 {
		id, err := r.ReadByte()
		if err != nil {
			t.Fatalf("ReadByte(section id): %v", err)
		}
		size, err := readU32(r)
		if err != nil {
			t.Fatalf("readU32(section size): %v", err)
		}
		payload := make([]byte, size)
		if _, err := r.Read(payload); err != nil {
			t.Fatalf("Read(section payload): %v", err)
		}
		sections = append(sections, wasmSection{id: id, payload: payload})
	}
	return sections
}

func customSectionName(t *testing.T, payload []byte) (string, []byte) {
	t.Helper()

	r := bytes.NewReader(payload)
	name, err := readName(r)
	if err != nil {
		t.Fatalf("readName(custom section): %v", err)
	}
	rest := make([]byte, r.Len())
	if _, err := r.Read(rest); err != nil {
		t.Fatalf("Read(custom section payload): %v", err)
	}
	return name, rest
}

func readName(r *bytes.Reader) (string, error) {
	n, err := readU32(r)
	if err != nil {
		return "", err
	}
	b := make([]byte, n)
	if _, err := r.Read(b); err != nil {
		return "", err
	}
	return string(b), nil
}

func readU32(r *bytes.Reader) (uint32, error) {
	var result uint32
	var shift uint
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint32(b&0x7f) << shift
		if b < 0x80 {
			return result, nil
		}
		shift += 7
	}
}
