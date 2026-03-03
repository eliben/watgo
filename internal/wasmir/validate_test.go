package wasmir

import (
	"strings"
	"testing"
)

func makeValidAddModule() *Module {
	return &Module{
		Types: []FuncType{{
			Params:  []ValueType{ValueTypeI32, ValueTypeI32},
			Results: []ValueType{ValueTypeI32},
		}},
		Funcs: []Function{{
			TypeIdx: 0,
			Body: []Instruction{
				{Kind: InstrLocalGet, LocalIndex: 0},
				{Kind: InstrLocalGet, LocalIndex: 1},
				{Kind: InstrI32Add},
				{Kind: InstrEnd},
			},
		}},
		Exports: []Export{{
			Name:  "add",
			Kind:  ExternalKindFunction,
			Index: 0,
		}},
	}
}

func TestValidateModule_ValidAdd(t *testing.T) {
	m := makeValidAddModule()
	if diags := ValidateModule(m); len(diags) > 0 {
		t.Fatalf("ValidateModule diagnostics: %v", diags.Error())
	}
}

func TestValidateModule_LocalIndexOutOfRange(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body[1].LocalIndex = 99

	diags := ValidateModule(m)
	if len(diags) == 0 {
		t.Fatal("ValidateModule returned no diagnostics, want failure")
	}
	if !strings.Contains(diags.Error(), "local index 99 out of range") {
		t.Fatalf("got diagnostics %q, want local index out of range", diags.Error())
	}
}

func TestValidateModule_StackUnderflow(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body = []Instruction{{Kind: InstrI32Add}, {Kind: InstrEnd}}

	diags := ValidateModule(m)
	if len(diags) == 0 {
		t.Fatal("ValidateModule returned no diagnostics, want failure")
	}
	if !strings.Contains(diags.Error(), "i32.add needs 2 operands") {
		t.Fatalf("got diagnostics %q, want i32.add stack error", diags.Error())
	}
}

func TestValidateModule_ResultArityMismatch(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body = []Instruction{
		{Kind: InstrLocalGet, LocalIndex: 0},
		{Kind: InstrLocalGet, LocalIndex: 1},
		{Kind: InstrEnd},
	}

	diags := ValidateModule(m)
	if len(diags) == 0 {
		t.Fatal("ValidateModule returned no diagnostics, want failure")
	}
	if !strings.Contains(diags.Error(), "result arity mismatch") {
		t.Fatalf("got diagnostics %q, want result arity mismatch", diags.Error())
	}
}

func TestValidateModule_ExportIndexOutOfRange(t *testing.T) {
	m := makeValidAddModule()
	m.Exports[0].Index = 5

	diags := ValidateModule(m)
	if len(diags) == 0 {
		t.Fatal("ValidateModule returned no diagnostics, want failure")
	}
	if !strings.Contains(diags.Error(), "index 5 out of range") {
		t.Fatalf("got diagnostics %q, want export index out of range", diags.Error())
	}
}

func TestValidateModule_CollectsMultipleDiagnostics(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].TypeIdx = 99
	m.Exports = append(m.Exports, Export{
		Name:  "bad",
		Kind:  ExternalKindFunction,
		Index: 42,
	})

	diags := ValidateModule(m)
	if len(diags) < 2 {
		t.Fatalf("got %d diagnostics, want >=2 (%v)", len(diags), diags.Error())
	}
	if !strings.Contains(diags.Error(), "invalid type index") {
		t.Fatalf("got diagnostics %q, missing invalid type index", diags.Error())
	}
	if !strings.Contains(diags.Error(), "out of range") {
		t.Fatalf("got diagnostics %q, missing export out-of-range", diags.Error())
	}
}
