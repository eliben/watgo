package textformat

import (
	"strings"
	"testing"
)

func mustPlainInstr(t *testing.T, instr Instruction) *PlainInstr {
	t.Helper()
	pi, ok := instr.(*PlainInstr)
	if !ok {
		t.Fatalf("expected *PlainInstr, got %T", instr)
	}
	return pi
}

func TestParseSmoke(t *testing.T) {
	// Smoke test for parsing a module, checking the parsed AST without using
	// its textual/debug representation.
	wat := `
	(module $mod
		(func $add (export "add") (param $a i32) (param $b i32) (result i32)
			(local $i i64)
			(local f32)
		)
	)`
	m, err := ParseModule(wat)
	if err != nil {
		t.Fatal(err)
	}

	if m.Id != "$mod" {
		t.Errorf("got mod id %v, want $mod", m.Id)
	}
	if m.loc.String() != "2:2" {
		t.Errorf("got mod loc %s, want 2:2", m.loc)
	}

	func0 := m.Funcs[0]
	if func0.Id != "$add" {
		t.Errorf("got func id %v, want $add", func0.Id)
	}
	if func0.Export != "add" {
		t.Errorf("got func export %v, want add", func0.Export)
	}

	func0params := func0.TyUse.Params
	if len(func0params) != 2 {
		t.Errorf("got %d params, want 2", len(func0params))
	}
	param0, param1 := func0params[0], func0params[1]
	if param0.Id != "$a" || param0.Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $a i32", param0.Id, param0.Ty)
	}
	if param1.Id != "$b" || param1.Ty.String() != "i32" {
		t.Errorf("got param id=%v ty=%s, want $b i32", param1.Id, param1.Ty)
	}

	result0 := func0.TyUse.Results[0]
	if result0.Ty.String() != "i32" {
		t.Errorf("got result ty=%s, want i32", result0.Ty)
	}

	if len(func0.Locals) != 2 {
		t.Errorf("got %d locals, want 2", len(func0.Locals))
	}
	local0 := func0.Locals[0]
	if local0.Id != "$i" || local0.Ty.String() != "i64" {
		t.Errorf("got param id=%v ty=%s, want $i i64", local0.Id, local0.Ty)
	}
	local1 := func0.Locals[1]
	if local1.Id != "" || local1.Ty.String() != "f32" {
		t.Errorf("got param id=%v ty=%s, want <empty> f32", local1.Id, local1.Ty)
	}
}

func TestParseModule_LinearAddInstructions(t *testing.T) {
	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	m, err := ParseModule(wat)
	if err != nil {
		t.Fatalf("ParseModule returned error: %v", err)
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}

	f := m.Funcs[0]
	if len(f.Instrs) != 3 {
		t.Fatalf("got %d instructions, want 3", len(f.Instrs))
	}

	instr0 := mustPlainInstr(t, f.Instrs[0])
	if instr0.Name != "local.get" {
		t.Fatalf("instr0 name=%q, want local.get", instr0.Name)
	}
	if len(instr0.Operands) != 1 {
		t.Fatalf("instr0 has %d operands, want 1", len(instr0.Operands))
	}
	op0, ok := instr0.Operands[0].(*IdOperand)
	if !ok {
		t.Fatalf("instr0 operand type=%T, want *IdOperand", instr0.Operands[0])
	}
	if op0.Value != "$a" {
		t.Fatalf("instr0 operand value=%q, want $a", op0.Value)
	}

	instr1 := mustPlainInstr(t, f.Instrs[1])
	op1, ok := instr1.Operands[0].(*IdOperand)
	if !ok {
		t.Fatalf("instr1 operand type=%T, want *IdOperand", instr1.Operands[0])
	}
	if instr1.Name != "local.get" || op1.Value != "$b" {
		t.Fatalf("got instr1=(%q, %q), want (local.get, $b)", instr1.Name, op1.Value)
	}

	instr2 := mustPlainInstr(t, f.Instrs[2])
	if instr2.Name != "i32.add" {
		t.Fatalf("instr2 name=%q, want i32.add", instr2.Name)
	}
	if len(instr2.Operands) != 0 {
		t.Fatalf("instr2 operands=%d, want 0", len(instr2.Operands))
	}
}

func TestParseModule_FoldedInstructionIsRejected(t *testing.T) {
	wat := `(module
  (func (result i32)
    (i32.add (i32.const 1) (i32.const 2))
  )
)`

	_, err := ParseModule(wat)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "folded instructions are not supported yet") {
		t.Fatalf("got error %q, want folded-instruction error", err.Error())
	}
}

func TestParseModule_LocalGetWithoutOperandIsRejected(t *testing.T) {
	wat := `(module
  (func
    local.get
  )
)`

	_, err := ParseModule(wat)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "local.get expects one operand") {
		t.Fatalf("got error %q, want local.get missing-operand error", err.Error())
	}
}
