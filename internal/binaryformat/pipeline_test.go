package binaryformat

import (
	"bytes"
	"testing"

	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/internal/wasmir"
)

func TestPipelineEncodeAddModule(t *testing.T) {
	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	ast, err := textformat.ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, lowerErr := textformat.LowerModule(ast)
	if lowerErr != nil {
		t.Fatalf("LowerModule error: %v", lowerErr)
	}

	validateErr := wasmir.ValidateModule(m)
	if validateErr != nil {
		t.Fatalf("ValidateModule error: %v", validateErr)
	}

	got, encodeErr := EncodeModule(m)
	if encodeErr != nil {
		t.Fatalf("EncodeModule error: %v", encodeErr)
	}

	// Expected bytes were cross-checked with wasm-tools for the same WAT.
	// Note: wasm-tools parse preserves identifier metadata in a trailing "name"
	// custom section, while this encoder currently emits only core sections.
	// Comparison was done against wasm-tools output after stripping all custom
	// sections:
	//   wasm-tools parse /tmp/add.wat -o /tmp/add.wasm
	//   wasm-tools strip -a /tmp/add.wasm -o /tmp/add_stripped_all.wasm
	//   xxd -p /tmp/add_stripped_all.wasm | tr -d '\n'
	want := []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x07, 0x01, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x07, 0x01, 0x03, 0x61, 0x64, 0x64, 0x00, 0x00,
		0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b,
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}
