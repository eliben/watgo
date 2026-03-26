package textformat

import (
	"errors"
	"strings"
	"testing"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
)

func asErrorList(t *testing.T, err error) diag.ErrorList {
	t.Helper()
	errs, ok := errors.AsType[diag.ErrorList](err)
	if !ok {
		t.Fatalf("expected diag.ErrorList, got %T (%v)", err, err)
	}
	return errs
}

func errorListContains(errs diag.ErrorList, needle string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), needle) {
			return true
		}
	}
	return false
}

func TestLowerModule_AddFunction(t *testing.T) {
	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
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

func TestLowerModule_UnknownLocalName(t *testing.T) {
	wat := `(module
  (func (param $a i32) (result i32)
    local.get $missing
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	_, err = LowerModule(ast)
	if err == nil {
		t.Fatal("LowerModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "invalid local.get operand") {
		t.Fatalf("got errors %q, want invalid local.get operand", errs.Error())
	}
	if !errorListContains(errs, "3:15") {
		t.Fatalf("got errors %q, want source location 3:15", errs.Error())
	}
}

func TestLowerModule_UnsupportedType(t *testing.T) {
	ast := &Module{
		Funcs: []*Function{{
			TyUse: &TypeUse{
				Params: []*ParamDecl{{Id: "$a", Ty: &BasicType{Name: "no_such_type"}}},
			},
		}},
	}

	_, err := LowerModule(ast)
	if err == nil {
		t.Fatal("LowerModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "unsupported param type") {
		t.Fatalf("got errors %q, want unsupported param type", errs.Error())
	}
}

func TestLowerModule_SIMDEndianFlipSlice(t *testing.T) {
	wat := `(module
  (import "env" "buffer" (memory 1))
  (func (param $offset i32)
    (v128.store
      (local.get $offset)
      (i8x16.swizzle
        (v128.load (local.get $offset))
        (v128.const i8x16 3 2 1 0 7 6 5 4 11 10 9 8 15 14 13 12))))
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}

	if got := m.Types[0].Params[0]; got != wasmir.ValueTypeI32 {
		t.Fatalf("param type = %v, want i32", got)
	}
	body := m.Funcs[0].Body
	if len(body) != 7 {
		t.Fatalf("got %d body instructions, want 7", len(body))
	}
	if body[2].Kind != wasmir.InstrV128Load {
		t.Fatalf("body[2]=%#v, want v128.load", body[2])
	}
	if body[3].Kind != wasmir.InstrV128Const {
		t.Fatalf("body[3]=%#v, want v128.const", body[3])
	}
	if body[4].Kind != wasmir.InstrI8x16Swizzle {
		t.Fatalf("body[4]=%#v, want i8x16.swizzle", body[4])
	}
	if body[5].Kind != wasmir.InstrV128Store {
		t.Fatalf("body[5]=%#v, want v128.store", body[5])
	}
	wantLanes := [16]byte{3, 2, 1, 0, 7, 6, 5, 4, 11, 10, 9, 8, 15, 14, 13, 12}
	if body[3].V128Const != wantLanes {
		t.Fatalf("v128.const lanes = %v, want %v", body[3].V128Const, wantLanes)
	}
}

func TestLowerModule_SIMDV128ConstI16x8(t *testing.T) {
	wat := `(module
  (func (result v128)
    (v128.const i16x8 0 1 2 3 4 5 6 7)
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}

	body := m.Funcs[0].Body
	if len(body) != 2 {
		t.Fatalf("got %d body instructions, want 2", len(body))
	}
	if body[0].Kind != wasmir.InstrV128Const {
		t.Fatalf("body[0]=%#v, want v128.const", body[0])
	}
	wantLanes := [16]byte{0, 0, 1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 6, 0, 7, 0}
	if body[0].V128Const != wantLanes {
		t.Fatalf("v128.const lanes = %v, want %v", body[0].V128Const, wantLanes)
	}
}

func TestLowerModule_CollectsMultipleDiagnostics(t *testing.T) {
	wat := `(module
  (func (param $a i32) (result i32)
    local.get $missing
    no.such.instr
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	_, err = LowerModule(ast)
	if err == nil {
		t.Fatal("LowerModule returned nil error, want diagnostics")
	}
	errs := asErrorList(t, err)
	if len(errs) < 2 {
		t.Fatalf("got %d diagnostics, want >=2 (%v)", len(errs), errs.Error())
	}
	if !errorListContains(errs, "invalid local.get operand") {
		t.Fatalf("got errors %q, missing invalid local.get operand", errs.Error())
	}
	if !errorListContains(errs, "unsupported instruction") {
		t.Fatalf("got errors %q, missing unsupported instruction", errs.Error())
	}
}

func TestLowerModule_NamedFunctionInDiagnostics(t *testing.T) {
	wat := `(module
  (func $foo (param $a i32) (result i32)
    local.get $missing
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	_, err = LowerModule(ast)
	if err == nil {
		t.Fatal("LowerModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "func[0] $foo") {
		t.Fatalf("got errors %q, want named function context", errs.Error())
	}
}

func TestLowerModule_LowersCallByName(t *testing.T) {
	wat := `(module
  (func $callee (result i32)
    (i32.const 42)
  )
  (func (export "caller") (result i32)
    call $callee
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}
	if len(m.Funcs) != 2 {
		t.Fatalf("got %d funcs, want 2", len(m.Funcs))
	}

	body := m.Funcs[1].Body
	if len(body) != 2 {
		t.Fatalf("got %d body instructions, want 2", len(body))
	}
	if body[0].Kind != wasmir.InstrCall || body[0].FuncIndex != 0 {
		t.Fatalf("body[0]=%#v, want call funcidx 0", body[0])
	}
	if body[1].Kind != wasmir.InstrEnd {
		t.Fatalf("body[1]=%#v, want end", body[1])
	}
}

func TestLowerModule_LowersPassiveDataAndMemoryInit(t *testing.T) {
	wat := `(module
  (memory i64 1)
  (data "\aa\bb")
  (func
    (memory.init 0 (i64.const 7) (i32.const 1) (i32.const 2))
    (data.drop 0))
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}

	if len(m.Data) != 1 {
		t.Fatalf("got %d data segments, want 1", len(m.Data))
	}
	if m.Data[0].Mode != wasmir.DataSegmentModePassive {
		t.Fatalf("data[0] mode=%v, want passive", m.Data[0].Mode)
	}
	if got, want := m.Data[0].Init, []byte{0xaa, 0xbb}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("data[0] init=%v, want %v", got, want)
	}

	body := m.Funcs[0].Body
	if len(body) != 6 {
		t.Fatalf("got %d body instructions, want 6", len(body))
	}
	if body[3].Kind != wasmir.InstrMemoryInit || body[3].DataIndex != 0 {
		t.Fatalf("body[3]=%#v, want memory.init 0", body[3])
	}
	if body[4].Kind != wasmir.InstrDataDrop || body[4].DataIndex != 0 {
		t.Fatalf("body[4]=%#v, want data.drop 0", body[4])
	}
}

func TestLowerModule_LowersFoldedIf(t *testing.T) {
	wat := `(module
  (func (result i64)
    (if (result i64) (i64.eqz (i64.const 0))
      (then (i64.const 1))
      (else (i64.const 2))
    )
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}

	body := m.Funcs[0].Body
	if len(body) != 8 {
		t.Fatalf("got %d body instructions, want 8", len(body))
	}
	if body[0].Kind != wasmir.InstrI64Const {
		t.Fatalf("body[0]=%#v, want i64.const", body[0])
	}
	if body[1].Kind != wasmir.InstrI64Eqz {
		t.Fatalf("body[1]=%#v, want i64.eqz", body[1])
	}
	if body[2].Kind != wasmir.InstrIf || !body[2].BlockHasResult || body[2].BlockType != wasmir.ValueTypeI64 {
		t.Fatalf("body[2]=%#v, want if with i64 result", body[2])
	}
	if body[4].Kind != wasmir.InstrElse {
		t.Fatalf("body[4]=%#v, want else", body[4])
	}
	if body[6].Kind != wasmir.InstrEnd {
		t.Fatalf("body[6]=%#v, want end (if)", body[6])
	}
	if body[7].Kind != wasmir.InstrEnd {
		t.Fatalf("body[7]=%#v, want end (func)", body[7])
	}
}

func TestLowerModule_Memory64DataOffset(t *testing.T) {
	wat := `(module
  (memory (export "memory") i64 2 250000)
  (data (i64.const 32) "abc")
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}

	if len(m.Memories) != 1 {
		t.Fatalf("got %d memories, want 1", len(m.Memories))
	}
	if got := m.Memories[0].AddressType; got != wasmir.ValueTypeI64 {
		t.Fatalf("memory address type=%v, want i64", got)
	}
	if got := m.Memories[0].Min; got != 2 {
		t.Fatalf("memory min=%d, want 2", got)
	}
	if !m.Memories[0].HasMax || m.Memories[0].Max != 250000 {
		t.Fatalf("memory max=(%t,%d), want (true,250000)", m.Memories[0].HasMax, m.Memories[0].Max)
	}

	if len(m.Data) != 1 {
		t.Fatalf("got %d data segments, want 1", len(m.Data))
	}
	if got := m.Data[0].OffsetType; got != wasmir.ValueTypeI64 {
		t.Fatalf("data offset type=%v, want i64", got)
	}
	if got := m.Data[0].OffsetI64; got != 32 {
		t.Fatalf("data offset=%d, want 32", got)
	}
	if got := string(m.Data[0].Init); got != "abc" {
		t.Fatalf("data init=%q, want %q", got, "abc")
	}
}

func TestLowerModule_Memory64MemArgOffset(t *testing.T) {
	wat := `(module
  (memory i64 1)
  (func
    i64.const 0
    i32.load offset=0xFFFF_FFFF_FFFF_FFFF
    drop
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	m, err := LowerModule(ast)
	if err != nil {
		t.Fatalf("LowerModule error: %v", err)
	}
	body := m.Funcs[0].Body
	if len(body) < 2 {
		t.Fatalf("got %d body instructions, want at least 2", len(body))
	}
	if body[1].Kind != wasmir.InstrI32Load {
		t.Fatalf("body[1]=%#v, want i32.load", body[1])
	}
	if got := body[1].MemoryOffset; got != 0xFFFF_FFFF_FFFF_FFFF {
		t.Fatalf("memory offset=0x%x, want 0xffffffffffffffff", got)
	}
}
