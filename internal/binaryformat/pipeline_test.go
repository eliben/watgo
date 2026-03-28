package binaryformat

import (
	"bytes"
	"testing"

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

	ast, err := textformat.ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, lowerErr := textformat.LowerModule(ast)
	if lowerErr != nil {
		t.Fatalf("LowerModule error: %v", lowerErr)
	}

	validateErr := validate.ValidateModule(m)
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
	want := canonicalAddModuleBytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("encoded bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestPipelineEncodeDecodeSIMDEndianFlipSlice(t *testing.T) {
	wat := `(module
  (import "env" "buffer" (memory 1))
  (func (export "endianflip") (param $offset i32)
    (v128.store
      (local.get $offset)
      (i8x16.swizzle
        (v128.load (local.get $offset))
        (v128.const i8x16 3 2 1 0 7 6 5 4 11 10 9 8 15 14 13 12))))
)`

	ast, err := textformat.ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := textformat.LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}
	if err := validate.ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule error: %v", err)
	}

	bin, err := EncodeModule(m)
	if err != nil {
		t.Fatalf("EncodeModule error: %v", err)
	}

	decoded, err := DecodeModule(bin)
	if err != nil {
		t.Fatalf("DecodeModule error: %v", err)
	}
	if err := validate.ValidateModule(decoded); err != nil {
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
