package binaryformat

import (
	"bytes"
	"fmt"
	"slices"
	"testing"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/internal/validate"
	"github.com/eliben/watgo/wasmir"
)

func TestPipelineEncodeAddModule(t *testing.T) {
	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	// Expected bytes include the standard name custom section for the source
	// parameter identifiers $a and $b.
	got := compilePipelineWAT(t, wat)
	want := addModuleWithParamNameSectionBytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestEncodeNameSectionFromIRNames(t *testing.T) {
	m := &wasmir.Module{
		Name: "$m",
		Types: []wasmir.TypeDef{
			{
				Name: "$point",
				Kind: wasmir.TypeDefKindStruct,
				Fields: []wasmir.FieldType{{
					Name: "$x",
					Type: wasmir.ValueTypeI32,
				}},
			},
			{
				Kind:   wasmir.TypeDefKindFunc,
				Params: []wasmir.ValueType{wasmir.ValueTypeI32},
			},
		},
		Funcs: []wasmir.Function{{
			TypeIdx:    1,
			Name:       "$use",
			ParamNames: []string{"$a"},
			LocalNames: []string{"$tmp"},
			Locals:     []wasmir.ValueType{wasmir.ValueTypeI32},
			Body:       []wasmir.Instruction{{Kind: wasmir.InstrEnd}},
		}},
		Tags: []wasmir.Tag{{
			Name:    "$boom",
			TypeIdx: 1,
		}},
	}

	got := encodeNameSection(m)
	want := []byte{
		0x04, 0x6e, 0x61, 0x6d, 0x65,
		0x00, 0x02, 0x01, 0x6d,
		0x01, 0x06, 0x01, 0x00, 0x03, 0x75, 0x73, 0x65,
		0x02, 0x0b, 0x01, 0x00, 0x02, 0x00, 0x01, 0x61, 0x01, 0x03, 0x74, 0x6d, 0x70,
		0x04, 0x08, 0x01, 0x00, 0x05, 0x70, 0x6f, 0x69, 0x6e, 0x74,
		0x0a, 0x06, 0x01, 0x00, 0x01, 0x00, 0x01, 0x78,
		0x0b, 0x07, 0x01, 0x00, 0x04, 0x62, 0x6f, 0x6f, 0x6d,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("name section mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestPipelineDecodeNameSectionFromWAT(t *testing.T) {
	// This test intentionally starts from WAT source and inspects both the raw
	// custom name section and the decoded wasmir names. It verifies the
	// end-to-end behavior expected by the standard name section:
	//   - the custom section name is "name";
	//   - subsection IDs are the standard IDs in increasing order;
	//   - WAT identifiers are emitted without the textual "$" prefix;
	//   - the name section is emitted after the data section;
	//   - DecodeModule loads recognized names back into wasmir;
	//   - DecodeModule -> EncodeModule preserves the name section.
	wat := `
(module $m
  (type $point (struct (field $x i32)))
  (type $sig (func (param i32)))
  (tag $boom (type $sig))
  (memory 1)
  (data (i32.const 0) "x")
  (func $use (type $sig) (param $a i32)
    (local $tmp i32)
    local.get $a
    local.set $tmp
  )
)`

	bin := compilePipelineWAT(t, wat)
	assertSingleTrailingNameSection(t, bin)
	assertDataSectionPrecedesNameSection(t, bin)

	namePayload := requireNameSectionPayload(t, bin)
	assertNameSubsectionIDs(t, namePayload, []byte{
		nameSubsectionModuleID,
		nameSubsectionFuncID,
		nameSubsectionLocalID,
		nameSubsectionTypeID,
		nameSubsectionFieldID,
		nameSubsectionTagID,
	})

	names := decodeStandardNameSectionPayload(t, namePayload)
	if got := names.moduleName; got != "m" {
		t.Fatalf("name section module name=%q, want m", got)
	}
	if got := names.typeNames[0]; got != "point" {
		t.Fatalf("name section type[0]=%q, want point", got)
	}
	if got := names.typeNames[1]; got != "sig" {
		t.Fatalf("name section type[1]=%q, want sig", got)
	}
	if got := names.fieldNames[0][0]; got != "x" {
		t.Fatalf("name section type[0] field[0]=%q, want x", got)
	}
	if got := names.tagNames[0]; got != "boom" {
		t.Fatalf("name section tag[0]=%q, want boom", got)
	}
	if got := names.funcNames[0]; got != "use" {
		t.Fatalf("name section func[0]=%q, want use", got)
	}
	if got := names.localNames[0][0]; got != "a" {
		t.Fatalf("name section func[0] local[0]=%q, want a", got)
	}
	if got := names.localNames[0][1]; got != "tmp" {
		t.Fatalf("name section func[0] local[1]=%q, want tmp", got)
	}

	decoded, err := DecodeModule(bin)
	if err != nil {
		t.Fatalf("DecodeModule error: %v", err)
	}

	if decoded.Name != "m" {
		t.Fatalf("decoded module name=%q, want m", decoded.Name)
	}
	if got := decoded.Types[0].Name; got != "point" {
		t.Fatalf("decoded type[0] name=%q, want point", got)
	}
	if got := decoded.Types[0].Fields[0].Name; got != "x" {
		t.Fatalf("decoded type[0] field[0] name=%q, want x", got)
	}
	if got := decoded.Types[1].Name; got != "sig" {
		t.Fatalf("decoded type[1] name=%q, want sig", got)
	}
	if got := decoded.Tags[0].Name; got != "boom" {
		t.Fatalf("decoded tag[0] name=%q, want boom", got)
	}
	if got := decoded.Funcs[0].Name; got != "use" {
		t.Fatalf("decoded func[0] name=%q, want use", got)
	}
	if got, want := decoded.Funcs[0].ParamNames, []string{"a"}; !slices.Equal(got, want) {
		t.Fatalf("decoded param names=%#v, want %#v", got, want)
	}
	if got, want := decoded.Funcs[0].LocalNames, []string{"tmp"}; !slices.Equal(got, want) {
		t.Fatalf("decoded local names=%#v, want %#v", got, want)
	}

	roundTrip, err := EncodeModule(decoded)
	if err != nil {
		t.Fatalf("EncodeModule(decoded) error: %v", err)
	}
	if !bytes.Equal(roundTrip, bin) {
		t.Fatalf("name-preserving roundtrip mismatch:\n got=%x\nwant=%x", roundTrip, bin)
	}
}

func TestPipelineNameSectionOmittedWithoutSourceNames(t *testing.T) {
	// Export names are semantic module data and live in the export section, not
	// in the custom name section. With no source-level module/type/function/
	// local/tag/field names, watgo should not emit a "name" custom section.
	wat := `
(module
  (func (export "id") (param i32) (result i32)
    local.get 0
  )
)`
	bin := compilePipelineWAT(t, wat)
	if payload, ok := nameSectionPayload(bin); ok {
		t.Fatalf("unexpected name custom section payload: %x", payload)
	}
}

func TestPipelineEncodeDecodeSIMDEndianFlipSlice(t *testing.T) {
	wat := `
(module
  (import "env" "buffer" (memory 1))
  (func (export "endianflip") (param $offset i32)
    (v128.store
      (local.get $offset)
      (i8x16.swizzle
        (v128.load (local.get $offset))
        (v128.const i8x16 3 2 1 0 7 6 5 4 11 10 9 8 15 14 13 12))))
)`

	bin := compilePipelineWAT(t, wat)

	decoded, err := DecodeModule(bin)
	if err != nil {
		t.Fatalf("DecodeModule error: %v", err)
	}
	if err := validate.ValidateModule(decoded, nil); err != nil {
		t.Fatalf("ValidateModule(decoded) error: %v", err)
	}

	body := decoded.Funcs[0].Body
	if len(body) != 7 {
		t.Fatalf("got %d decoded body instructions, want 7", len(body))
	}
	if body[2].Kind != wasmir.InstrV128Load || body[4].Kind != wasmir.InstrI8x16Swizzle || body[5].Kind != wasmir.InstrV128Store {
		t.Fatalf("decoded body kinds = %#v", body)
	}
}

func TestPipelineEncodeDecodeThrow(t *testing.T) {
	wat := `(module
  (tag $e (param i32))
  (func (export "boom")
    i32.const 7
    throw $e
  )
)`

	bin := compilePipelineWAT(t, wat)

	decoded, err := DecodeModule(bin)
	if err != nil {
		t.Fatalf("DecodeModule error: %v", err)
	}
	if err := validate.ValidateModule(decoded, nil); err != nil {
		t.Fatalf("ValidateModule(decoded) error: %v", err)
	}

	if len(decoded.Tags) != 1 {
		t.Fatalf("got %d decoded tags, want 1", len(decoded.Tags))
	}
	body := decoded.Funcs[0].Body
	if len(body) != 3 {
		t.Fatalf("got %d decoded body instructions, want 3", len(body))
	}
	if body[1].Kind != wasmir.InstrThrow || body[1].TagIndex != 0 {
		t.Fatalf("decoded throw = %#v, want InstrThrow with tag index 0", body[1])
	}
}

// compilePipelineWAT runs the parse/lower/validate/encode path.
func compilePipelineWAT(t *testing.T, wat string) []byte {
	t.Helper()

	ast, err := textformat.ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}
	m, hints, err := textformat.LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}
	if err := validate.ValidateModule(m, hints); err != nil {
		t.Fatalf("ValidateModule error: %v", err)
	}
	bin, err := EncodeModule(m)
	if err != nil {
		t.Fatalf("EncodeModule error: %v", err)
	}
	return bin
}

// requireNameSectionPayload returns the payload bytes inside the standard
// "name" custom section, excluding the custom-section name itself.
func requireNameSectionPayload(t *testing.T, wasm []byte) []byte {
	t.Helper()

	payload, ok := nameSectionPayload(wasm)
	if !ok {
		t.Fatal("encoded wasm has no name custom section")
	}
	return payload
}

// decodeStandardNameSectionPayload decodes a raw name-section payload with the
// production decoder helper so tests can assert on subsection contents.
func decodeStandardNameSectionPayload(t *testing.T, payload []byte) decodedNameSection {
	t.Helper()

	var names decodedNameSection
	var diags diag.ErrorList
	decodeNameSection(bytes.NewReader(payload), &names, &diags)
	if diags.HasAny() {
		t.Fatalf("decodeNameSection diagnostics: %v", diags)
	}
	return names
}

// assertSingleTrailingNameSection verifies watgo emitted exactly one standard
// name custom section and placed it as the final section.
func assertSingleTrailingNameSection(t *testing.T, wasm []byte) {
	t.Helper()

	sections := wasmSections(t, wasm)
	nameCount := 0
	for i, section := range sections {
		if section.id != sectionCustomID {
			continue
		}
		name, _ := customSectionName(t, section.payload)
		if name != nameSectionName {
			continue
		}
		nameCount++
		if i != len(sections)-1 {
			t.Fatalf("name custom section is section index %d, want trailing section index %d", i, len(sections)-1)
		}
	}
	if nameCount != 1 {
		t.Fatalf("got %d name custom sections, want 1", nameCount)
	}
}

// assertDataSectionPrecedesNameSection verifies the standard placement rule
// relevant to modules that have data segments.
func assertDataSectionPrecedesNameSection(t *testing.T, wasm []byte) {
	t.Helper()

	sections := wasmSections(t, wasm)
	dataIndex := -1
	nameIndex := -1
	for i, section := range sections {
		switch section.id {
		case sectionDataID:
			dataIndex = i
		case sectionCustomID:
			name, _ := customSectionName(t, section.payload)
			if name == nameSectionName {
				nameIndex = i
			}
		}
	}
	if dataIndex == -1 {
		t.Fatal("test fixture did not encode a data section")
	}
	if nameIndex == -1 {
		t.Fatal("encoded wasm has no name custom section")
	}
	if nameIndex <= dataIndex {
		t.Fatalf("name custom section index=%d, want after data section index=%d", nameIndex, dataIndex)
	}
}

// assertNameSubsectionIDs checks the emitted name subsection order without
// coupling the test to every payload byte.
func assertNameSubsectionIDs(t *testing.T, payload []byte, want []byte) {
	t.Helper()

	var got []byte
	r := bytes.NewReader(payload)
	for !atEOF(r) {
		id, err := readByte(r)
		if err != nil {
			t.Fatalf("name subsection id: %v", err)
		}
		size, err := readU32(r)
		if err != nil {
			t.Fatalf("name subsection %d size: %v", id, err)
		}
		if _, err := readN(r, int(size)); err != nil {
			t.Fatalf("name subsection %d payload: %v", id, err)
		}
		got = append(got, id)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("name subsection IDs=%v, want %v", got, want)
	}
}

// nameSectionPayload finds the standard "name" custom section payload in a WASM
// binary and reports whether one was present.
func nameSectionPayload(wasm []byte) ([]byte, bool) {
	for _, section := range wasmSectionsNoFatal(wasm) {
		if section.id != sectionCustomID {
			continue
		}
		name, payload, ok := customSectionNameNoFatal(section.payload)
		if ok && name == nameSectionName {
			return payload, true
		}
	}
	return nil, false
}

type wasmSectionForTest struct {
	id      byte
	payload []byte
}

// wasmSections parses top-level WASM sections and fails the current test on
// malformed input.
func wasmSections(t *testing.T, wasm []byte) []wasmSectionForTest {
	t.Helper()

	sections, err := parseWASMSections(wasm)
	if err != nil {
		t.Fatal(err)
	}
	return sections
}

// wasmSectionsNoFatal parses top-level WASM sections for predicates that can
// report absence instead of failing the test immediately.
func wasmSectionsNoFatal(wasm []byte) []wasmSectionForTest {
	sections, err := parseWASMSections(wasm)
	if err != nil {
		return nil
	}
	return sections
}

// parseWASMSections is a small section-table parser for tests that need to
// inspect custom sections.
func parseWASMSections(wasm []byte) ([]wasmSectionForTest, error) {
	if len(wasm) < len(wasmMagic)+len(wasmVersion) {
		return nil, fmt.Errorf("short wasm")
	}
	if string(wasm[:len(wasmMagic)]) != wasmMagic {
		return nil, fmt.Errorf("bad wasm magic")
	}
	if string(wasm[len(wasmMagic):len(wasmMagic)+len(wasmVersion)]) != wasmVersion {
		return nil, fmt.Errorf("bad wasm version")
	}
	r := bytes.NewReader(wasm[len(wasmMagic)+len(wasmVersion):])
	var sections []wasmSectionForTest
	for !atEOF(r) {
		id, err := readByte(r)
		if err != nil {
			return nil, err
		}
		size, err := readU32(r)
		if err != nil {
			return nil, err
		}
		payload, err := readN(r, int(size))
		if err != nil {
			return nil, err
		}
		sections = append(sections, wasmSectionForTest{id: id, payload: payload})
	}
	return sections, nil
}

// customSectionName reads a custom-section name and returns the remaining
// custom-section payload, failing the test on malformed input.
func customSectionName(t *testing.T, payload []byte) (string, []byte) {
	t.Helper()

	name, rest, ok := customSectionNameNoFatal(payload)
	if !ok {
		t.Fatalf("invalid custom section payload: %x", payload)
	}
	return name, rest
}

// customSectionNameNoFatal reads a custom-section name and returns false for
// malformed payloads.
func customSectionNameNoFatal(payload []byte) (string, []byte, bool) {
	r := bytes.NewReader(payload)
	name, err := readName(r)
	if err != nil {
		return "", nil, false
	}
	rest, err := readN(r, r.Len())
	if err != nil {
		return "", nil, false
	}
	return name, rest, true
}
