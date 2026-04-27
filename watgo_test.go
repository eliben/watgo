package watgo_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
)

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
// section for the source parameter identifiers $a and $b in
// TestCompileWATToWASM_PublicAPI. The section has only local-names subsection
// 2, with function index 0 mapping local indices 0 and 1 to "a" and "b".
func addModuleWithParamNameSectionBytes() []byte {
	b := append([]byte(nil), canonicalAddModuleBytes()...)
	return append(b,
		0x00, 0x10, 0x04, 0x6e, 0x61, 0x6d, 0x65,
		0x02, 0x09, 0x01, 0x00, 0x02, 0x00, 0x01, 0x61, 0x01, 0x01, 0x62,
	)
}

func TestCompileWATToWASM_PublicAPI(t *testing.T) {
	wat := []byte(`
(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`)

	got, err := watgo.CompileWATToWASM(wat)
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	want := addModuleWithParamNameSectionBytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("CompileWATToWASM bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestCompileWATToWASM_MultipleErrors_PublicAPI(t *testing.T) {
	_, err := watgo.CompileWATToWASM([]byte(`
(module
  (func (export "bad_local_get_1") (param $a i32) (result i32)
    local.get $missing1
  )
  (func (export "bad_local_get_2") (param $b i32) (result i32)
    local.get $missing2
  )
)`))
	if err == nil {
		t.Fatal("expected CompileWATToWASM to fail")
	}

	var errs diag.ErrorList
	if !errors.As(err, &errs) {
		t.Fatalf("expected diag.ErrorList, got %T (%v)", err, err)
	}
	if len(errs) < 2 {
		t.Fatalf("got %d diagnostics, want >=2 (%v)", len(errs), errs.Error())
	}
	if !bytes.Contains([]byte(errs[0].Error()), []byte("func[0]")) {
		t.Fatalf("first diagnostic %q, want function context", errs[0])
	}
	if !bytes.Contains([]byte(errs[1].Error()), []byte("func[1]")) {
		t.Fatalf("second diagnostic %q, want function context", errs[1])
	}
}

func TestParseWAT_PublicAPI(t *testing.T) {
	wat := []byte(`
(module
  (func (export "add") (param i32 i32) (result i32)
    local.get 0
    local.get 1
    i32.add
  )
)`)

	m, err := watgo.ParseWAT(wat)
	if err != nil {
		t.Fatalf("ParseWAT failed: %v", err)
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
	if m.Types[0].Params[0] != wasmir.ValueTypeI32 || m.Types[0].Results[0] != wasmir.ValueTypeI32 {
		t.Fatalf("unexpected signature: %#v", m.Types[0])
	}
}

func TestParseWAT_ParseError_PublicAPI(t *testing.T) {
	_, err := watgo.ParseWAT([]byte("(module"))
	if err == nil {
		t.Fatal("expected ParseWAT to fail")
	}
}

func TestParseAndValidateWAT_PublicAPI(t *testing.T) {
	m, err := watgo.ParseAndValidateWAT([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		t.Fatalf("ParseAndValidateWAT failed: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
}

func TestParseAndValidateWAT_UsesFoldedValidationHints_PublicAPI(t *testing.T) {
	_, err := watgo.ParseAndValidateWAT([]byte(`
(module
  (func
    unreachable
    (drop (i32.eqz (nop))))
)`))
	if err == nil {
		t.Fatal("ParseAndValidateWAT succeeded, want folded operand validation failure")
	}
}

func TestValidateModule_PublicAPI(t *testing.T) {
	m := &wasmir.Module{
		Types: []wasmir.TypeDef{{
			Results: []wasmir.ValueType{wasmir.ValueTypeI32},
		}},
		Funcs: []wasmir.Function{{
			TypeIdx: 0,
			Body: []wasmir.Instruction{
				{Kind: wasmir.InstrEnd},
			},
		}},
	}

	err := watgo.ValidateModule(m)
	if err == nil {
		t.Fatal("expected ValidateModule to fail")
	}
}

func TestEncodeWASM_PublicAPI(t *testing.T) {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "add") (param i32 i32) (result i32)
    local.get 0
    local.get 1
    i32.add
  )
)`))
	if err != nil {
		t.Fatalf("ParseWAT failed: %v", err)
	}
	if err := watgo.ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule failed: %v", err)
	}

	got, err := watgo.EncodeWASM(m)
	if err != nil {
		t.Fatalf("EncodeWASM failed: %v", err)
	}

	want := canonicalAddModuleBytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeWASM bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestPrintWAT_PublicAPI(t *testing.T) {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "add") (param i32 i32) (result i32)
    local.get 0
    local.get 1
    i32.add
  )
)`))
	if err != nil {
		t.Fatalf("ParseWAT failed: %v", err)
	}
	if err := watgo.ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule failed: %v", err)
	}

	printed, err := watgo.PrintWAT(m)
	if err != nil {
		t.Fatalf("PrintWAT failed: %v", err)
	}
	if !bytes.Contains(printed, []byte("(func (type 0) (param i32) (param i32) (result i32)")) {
		t.Fatalf("PrintWAT output missing function declaration:\n%s", printed)
	}
	roundTrip, err := watgo.CompileWATToWASM(printed)
	if err != nil {
		t.Fatalf("CompileWATToWASM(PrintWAT output) failed: %v\nprinted:\n%s", err, printed)
	}
	want, err := watgo.EncodeWASM(m)
	if err != nil {
		t.Fatalf("EncodeWASM failed: %v", err)
	}
	if !bytes.Equal(roundTrip, want) {
		t.Fatalf("PrintWAT roundtrip mismatch:\nprinted:\n%s", printed)
	}
}

func TestDecodeWASM_PublicAPI(t *testing.T) {
	m, err := watgo.DecodeWASM(canonicalAddModuleBytes())
	if err != nil {
		t.Fatalf("DecodeWASM failed: %v", err)
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
	if err := watgo.ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule(decoded) failed: %v", err)
	}
}

func TestDecodeWASM_DecodeError_PublicAPI(t *testing.T) {
	_, err := watgo.DecodeWASM([]byte{0x00, 0x61, 0x73})
	if err == nil {
		t.Fatal("expected DecodeWASM to fail")
	}
}

func ExampleCompileWATToWASM() {
	wasm, err := watgo.CompileWATToWASM([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		panic(err)
	}
	fmt.Println(len(wasm) > 0)
	// Output:
	// true
}

func ExampleParseWAT() {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		panic(err)
	}
	fmt.Println(len(m.Funcs), len(m.Exports))
	// Output:
	// 1 1
}

func ExampleParseWAT_moduleAnalysis() {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "add") (param i32 i32) (result i32)
    local.get 0
    local.get 1
    i32.add
  )
  (func (param f32 i32) (result i32)
    local.get 1
    i32.const 1
    i32.add
    drop
    i32.const 0
  )
)`))
	if err != nil {
		panic(err)
	}

	i32Params := 0
	localGets := 0
	i32Adds := 0

	// Module-defined functions carry a type index into m.Types. The function
	// body itself is a flat sequence of wasmir.Instruction values.
	for _, fn := range m.Funcs {
		sig := m.Types[fn.TypeIdx]
		for _, param := range sig.Params {
			if param.Kind == wasmir.ValueKindI32 {
				i32Params++
			}
		}

		for _, instr := range fn.Body {
			switch instr.Kind {
			case wasmir.InstrLocalGet:
				localGets++
			case wasmir.InstrI32Add:
				i32Adds++
			}
		}
	}

	fmt.Printf("module-defined funcs: %d\n", len(m.Funcs))
	fmt.Printf("i32 params: %d\n", i32Params)
	fmt.Printf("local.get instructions: %d\n", localGets)
	fmt.Printf("i32.add instructions: %d\n", i32Adds)
	// Output:
	// module-defined funcs: 2
	// i32 params: 3
	// local.get instructions: 3
	// i32.add instructions: 2
}

func ExampleParseAndValidateWAT() {
	m, err := watgo.ParseAndValidateWAT([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		panic(err)
	}
	fmt.Println(len(m.Funcs))
	// Output:
	// 1
}

func ExampleParseAndValidateWAT_foldedValidationHints() {
	src := []byte(`
(module
  (func
    unreachable
    (drop (i32.eqz (nop)))))
`)

	// This WAT is invalid: `(nop)` is explicitly supplied as the folded
	// operand to `i32.eqz`, but `nop` produces no value. ParseWAT lowers folded
	// WAT into a flat instruction stream, so ValidateModule only sees
	// `unreachable; nop; i32.eqz; drop`.
	//
	// The WebAssembly validation algorithm treats unreachable code as
	// stack-polymorphic: after a control frame is marked unreachable, popping a
	// missing operand can synthesize a bottom value instead of underflowing.
	// See the spec appendix:
	// https://webassembly.github.io/spec/core/appendix/algorithm.html
	//
	// That rule is correct for flat wasm instruction streams, but by itself it
	// cannot distinguish a truly missing operand from a folded source operand
	// that was explicitly present and produced no value.
	m, err := watgo.ParseWAT(src)
	if err != nil {
		panic(err)
	}
	fmt.Println(watgo.ValidateModule(m) == nil)

	// ParseAndValidateWAT keeps the lowering hints that record explicit folded
	// operands, so it rejects the original source shape.
	_, err = watgo.ParseAndValidateWAT(src)
	fmt.Println(err != nil)
	// Output:
	// true
	// true
}

func ExampleDecodeWASM() {
	m, err := watgo.DecodeWASM(canonicalAddModuleBytes())
	if err != nil {
		panic(err)
	}
	fmt.Println(len(m.Types), len(m.Funcs))
	// Output:
	// 1 1
}

func ExampleValidateModule() {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		panic(err)
	}
	fmt.Println(watgo.ValidateModule(m) == nil)
	// Output:
	// true
}

func ExampleEncodeWASM() {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		panic(err)
	}
	if err := watgo.ValidateModule(m); err != nil {
		panic(err)
	}
	wasm, err := watgo.EncodeWASM(m)
	if err != nil {
		panic(err)
	}
	fmt.Println(len(wasm) > 0)
	// Output:
	// true
}

func ExamplePrintWAT() {
	m, err := watgo.ParseWAT([]byte(`
(module
  (func (export "answer") (result i32)
    i32.const 42
  )
)`))
	if err != nil {
		panic(err)
	}
	if err := watgo.ValidateModule(m); err != nil {
		panic(err)
	}
	wat, err := watgo.PrintWAT(m)
	if err != nil {
		panic(err)
	}
	fmt.Println(bytes.Contains(wat, []byte("i32.const 42")))
	// Output:
	// true
}
