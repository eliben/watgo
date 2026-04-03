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

func TestCompileWATToWASM_PublicAPI(t *testing.T) {
	wat := []byte(`(module
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

	want := canonicalAddModuleBytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("CompileWATToWASM bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestCompileWATToWASM_MultipleErrors_PublicAPI(t *testing.T) {
	_, err := watgo.CompileWATToWASM([]byte(`(module
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

	errs, ok := errors.AsType[diag.ErrorList](err)
	if !ok {
		t.Fatalf("expected diag.ErrorList, got %T (%v)", err, err)
	}
	if len(errs) < 2 {
		t.Fatalf("got %d diagnostics, want >=2 (%v)", len(errs), errs.Error())
	}
}

func TestParseWAT_PublicAPI(t *testing.T) {
	wat := []byte(`(module
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
	m, err := watgo.ParseWAT([]byte(`(module
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
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
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
	m, err := watgo.ParseWAT([]byte(`(module
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
	m, err := watgo.ParseWAT([]byte(`(module
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
	m, err := watgo.ParseWAT([]byte(`(module
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
