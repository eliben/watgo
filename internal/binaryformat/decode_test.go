package binaryformat

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
)

// canonicalAddModuleBytes returns the binary encoding of:
//
//	(module
//	  (func (export "add") (param i32 i32) (result i32)
//	    local.get 0
//	    local.get 1
//	    i32.add))
func canonicalAddModuleBytes() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x07, 0x01, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x07, 0x01, 0x03, 0x61, 0x64, 0x64, 0x00, 0x00,
		0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b,
	}
}

// addModuleWithParamNameSectionBytes appends the standard "name" custom
// section for the source parameter identifiers $a and $b used by pipeline
// encoder tests. The section has only local-names subsection 2, with function
// index 0 mapping local indices 0 and 1 to "a" and "b".
func addModuleWithParamNameSectionBytes() []byte {
	b := append([]byte(nil), canonicalAddModuleBytes()...)
	return append(b,
		0x00, 0x10, 0x04, 0x6e, 0x61, 0x6d, 0x65,
		0x02, 0x09, 0x01, 0x00, 0x02, 0x00, 0x01, 0x61, 0x01, 0x01, 0x62,
	)
}

// canonicalFloatOpsModuleBytes returns the binary encoding of:
//
//	(module
//	  (func
//	    f32.const 1.0
//	    f32.ceil
//	    f32.floor
//	    f32.trunc
//	    f32.nearest
//	    f32.sqrt
//	    f32.const 2.0
//	    f32.add
//	    f32.sub
//	    f32.mul
//	    f32.div
//	    f32.min
//	    f32.max
//	    f64.const 1.0
//	    f64.ceil
//	    f64.floor
//	    f64.trunc
//	    f64.nearest
//	    f64.sqrt
//	    f64.const 2.0
//	    f64.add
//	    f64.sub
//	    f64.mul
//	    f64.div
//	    f64.min
//	    f64.max))
func canonicalFloatOpsModuleBytes() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x0a, 0x36, 0x01, 0x34, 0x00,
		0x43, 0x00, 0x00, 0x80, 0x3f,
		0x8d, 0x8e, 0x8f, 0x90, 0x91,
		0x43, 0x00, 0x00, 0x00, 0x40,
		0x92, 0x93, 0x94, 0x95, 0x96, 0x97,
		0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
		0x9b, 0x9c, 0x9d, 0x9e, 0x9f,
		0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
		0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5,
		0x0b,
	}
}

// truncatedF64ConstModuleBytes is a malformed truncation of:
//
//	(module
//	  (func
//	    f64.const 1.0))
//
// The final immediate byte is intentionally missing to test decode errors.
func truncatedF64ConstModuleBytes() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x0a, 0x0b, 0x01, 0x09, 0x00,
		0x44, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0,
	}
}

func TestDecodeModule_AddModule(t *testing.T) {
	m, err := DecodeModule(canonicalAddModuleBytes())
	if err != nil {
		t.Fatalf("DecodeModule failed: %v", err)
	}

	if len(m.Types) != 1 {
		t.Fatalf("got %d types, want 1", len(m.Types))
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
	if len(m.Exports) != 1 {
		t.Fatalf("got %d exports, want 1", len(m.Exports))
	}

	ft := m.Types[0]
	if len(ft.Params) != 2 || ft.Params[0] != wasmir.ValueTypeI32 || ft.Params[1] != wasmir.ValueTypeI32 {
		t.Fatalf("unexpected params: %#v", ft.Params)
	}
	if len(ft.Results) != 1 || ft.Results[0] != wasmir.ValueTypeI32 {
		t.Fatalf("unexpected results: %#v", ft.Results)
	}

	fn := m.Funcs[0]
	if fn.TypeIdx != 0 {
		t.Fatalf("got typeidx=%d, want 0", fn.TypeIdx)
	}
	if len(fn.Locals) != 0 {
		t.Fatalf("got %d locals, want 0", len(fn.Locals))
	}
	if len(fn.Body) != 4 {
		t.Fatalf("got %d body instructions, want 4", len(fn.Body))
	}

	if fn.Body[0].Kind != wasmir.InstrLocalGet || fn.Body[0].LocalIndex != 0 {
		t.Fatalf("body[0]=%#v, want local.get 0", fn.Body[0])
	}
	if fn.Body[1].Kind != wasmir.InstrLocalGet || fn.Body[1].LocalIndex != 1 {
		t.Fatalf("body[1]=%#v, want local.get 1", fn.Body[1])
	}
	if fn.Body[2].Kind != wasmir.InstrI32Add {
		t.Fatalf("body[2]=%#v, want i32.add", fn.Body[2])
	}
	if fn.Body[3].Kind != wasmir.InstrEnd {
		t.Fatalf("body[3]=%#v, want end", fn.Body[3])
	}

	exp := m.Exports[0]
	if exp.Name != "add" || exp.Kind != wasmir.ExternalKindFunction || exp.Index != 0 {
		t.Fatalf("unexpected export: %#v", exp)
	}
}

func TestDecodeModule_FloatOps(t *testing.T) {
	m, err := DecodeModule(canonicalFloatOpsModuleBytes())
	if err != nil {
		t.Fatalf("DecodeModule failed: %v", err)
	}

	if len(m.Types) != 1 {
		t.Fatalf("got %d types, want 1", len(m.Types))
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}

	fn := m.Funcs[0]
	if len(fn.Body) != 27 {
		t.Fatalf("got %d body instructions, want 27", len(fn.Body))
	}

	expected := []wasmir.InstrKind{
		wasmir.InstrF32Const,
		wasmir.InstrF32Ceil,
		wasmir.InstrF32Floor,
		wasmir.InstrF32Trunc,
		wasmir.InstrF32Nearest,
		wasmir.InstrF32Sqrt,
		wasmir.InstrF32Const,
		wasmir.InstrF32Add,
		wasmir.InstrF32Sub,
		wasmir.InstrF32Mul,
		wasmir.InstrF32Div,
		wasmir.InstrF32Min,
		wasmir.InstrF32Max,
		wasmir.InstrF64Const,
		wasmir.InstrF64Ceil,
		wasmir.InstrF64Floor,
		wasmir.InstrF64Trunc,
		wasmir.InstrF64Nearest,
		wasmir.InstrF64Sqrt,
		wasmir.InstrF64Const,
		wasmir.InstrF64Add,
		wasmir.InstrF64Sub,
		wasmir.InstrF64Mul,
		wasmir.InstrF64Div,
		wasmir.InstrF64Min,
		wasmir.InstrF64Max,
		wasmir.InstrEnd,
	}
	for i, kind := range expected {
		if fn.Body[i].Kind != kind {
			t.Fatalf("body[%d] kind=%v, want %v", i, fn.Body[i].Kind, kind)
		}
	}

	if fn.Body[0].F32Const != 0x3f800000 {
		t.Fatalf("body[0] f32 const bits=%#x, want 0x3f800000", fn.Body[0].F32Const)
	}
	if fn.Body[6].F32Const != 0x40000000 {
		t.Fatalf("body[6] f32 const bits=%#x, want 0x40000000", fn.Body[6].F32Const)
	}
	if fn.Body[13].F64Const != 0x3ff0000000000000 {
		t.Fatalf("body[13] f64 const bits=%#x, want 0x3ff0000000000000", fn.Body[13].F64Const)
	}
	if fn.Body[19].F64Const != 0x4000000000000000 {
		t.Fatalf("body[19] f64 const bits=%#x, want 0x4000000000000000", fn.Body[19].F64Const)
	}
}

func TestDecodeEncodeRoundTrip_AddModule(t *testing.T) {
	orig := canonicalAddModuleBytes()

	m, err := DecodeModule(orig)
	if err != nil {
		t.Fatalf("DecodeModule failed: %v", err)
	}

	got, err := EncodeModule(m)
	if err != nil {
		t.Fatalf("EncodeModule failed: %v", err)
	}

	if !bytes.Equal(got, orig) {
		t.Fatalf("roundtrip mismatch:\n got=%x\nwant=%x", got, orig)
	}
}

func TestDecodeModule_BadMagic(t *testing.T) {
	bin := canonicalAddModuleBytes()
	bin[0] = 0xff

	_, err := DecodeModule(bin)
	if err == nil {
		t.Fatalf("DecodeModule succeeded, want error")
	}

	var errs diag.ErrorList
	if !errors.As(err, &errs) {
		t.Fatalf("DecodeModule error type = %T, want diag.ErrorList", err)
	}
	if !errorListContains(errs, "bad wasm magic") {
		t.Fatalf("expected bad magic diagnostic, got: %v", err)
	}
}

func TestDecodeModule_UnsupportedOpcode(t *testing.T) {
	bin := canonicalAddModuleBytes()
	bin[len(bin)-2] = 0xff // Replace i32.add opcode with unsupported opcode.

	_, err := DecodeModule(bin)
	if err == nil {
		t.Fatalf("DecodeModule succeeded, want error")
	}

	var errs diag.ErrorList
	if !errors.As(err, &errs) {
		t.Fatalf("DecodeModule error type = %T, want diag.ErrorList", err)
	}
	if !errorListContains(errs, "unsupported opcode 0xff") {
		t.Fatalf("expected unsupported opcode diagnostic, got: %v", err)
	}
}

func TestDecodeModule_TruncatedF64Immediate(t *testing.T) {
	_, err := DecodeModule(truncatedF64ConstModuleBytes())
	if err == nil {
		t.Fatalf("DecodeModule succeeded, want error")
	}

	var errs diag.ErrorList
	if !errors.As(err, &errs) {
		t.Fatalf("DecodeModule error type = %T, want diag.ErrorList", err)
	}
	if !errorListContains(errs, "read f64 immediate") {
		t.Fatalf("expected f64 immediate diagnostic, got: %v", err)
	}
}

func errorListContains(errs diag.ErrorList, needle string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), needle) {
			return true
		}
	}
	return false
}
