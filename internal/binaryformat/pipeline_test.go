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

	m, hints, lowerErr := textformat.LowerModule(ast)
	if lowerErr != nil {
		t.Fatalf("LowerModule error: %v", lowerErr)
	}

	validateErr := validate.ValidateModule(m, hints)
	if validateErr != nil {
		t.Fatalf("ValidateModule error: %v", validateErr)
	}

	got, encodeErr := EncodeModule(m)
	if encodeErr != nil {
		t.Fatalf("EncodeModule error: %v", encodeErr)
	}

	// Expected bytes include the standard name custom section for the source
	// parameter identifiers $a and $b.
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
