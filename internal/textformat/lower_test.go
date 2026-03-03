package textformat

import (
	"strings"
	"testing"

	"github.com/eliben/watgo/internal/wasmir"
)

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

	m, diags := LowerModule(ast)
	if len(diags) > 0 {
		t.Fatalf("LowerModule diagnostics: %v", diags.Error())
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

	_, diags := LowerModule(ast)
	if len(diags) == 0 {
		t.Fatal("LowerModule returned no diagnostics, want failure")
	}
	if !strings.Contains(diags.Error(), "invalid local.get operand") {
		t.Fatalf("got diagnostics %q, want invalid local.get operand", diags.Error())
	}
}

func TestLowerModule_UnsupportedType(t *testing.T) {
	wat := `(module
  (func (param $a i64) (result i32)
    local.get $a
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	_, diags := LowerModule(ast)
	if len(diags) == 0 {
		t.Fatal("LowerModule returned no diagnostics, want failure")
	}
	if !strings.Contains(diags.Error(), "unsupported param type") {
		t.Fatalf("got diagnostics %q, want unsupported param type", diags.Error())
	}
}

func TestLowerModule_CollectsMultipleDiagnostics(t *testing.T) {
	wat := `(module
  (func (param $a i64) (result i32)
    i32.sub
  )
)`

	ast, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule failed: %v", err)
	}

	_, diags := LowerModule(ast)
	if len(diags) < 2 {
		t.Fatalf("got %d diagnostics, want >=2 (%v)", len(diags), diags.Error())
	}
	if !strings.Contains(diags.Error(), "unsupported param type") {
		t.Fatalf("got diagnostics %q, missing unsupported param type", diags.Error())
	}
	if !strings.Contains(diags.Error(), "unsupported instruction") {
		t.Fatalf("got diagnostics %q, missing unsupported instruction", diags.Error())
	}
}
