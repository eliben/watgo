package wasmir

import (
	"errors"
	"strings"
	"testing"

	"github.com/eliben/watgo/diag"
)

func errorListContains(errs diag.ErrorList, needle string) bool {
	for _, err := range errs {
		if strings.Contains(err.Error(), needle) {
			return true
		}
	}
	return false
}

func asErrorList(t *testing.T, err error) diag.ErrorList {
	t.Helper()
	errs, ok := errors.AsType[diag.ErrorList](err)
	if !ok {
		t.Fatalf("expected diag.ErrorList, got %T (%v)", err, err)
	}
	return errs
}

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
	if err := ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule error: %v", err)
	}
}

func TestValidateModule_LocalIndexOutOfRange(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body[1].LocalIndex = 99

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "local index 99 out of range") {
		t.Fatalf("got errors %q, want local index out of range", errs.Error())
	}
}

func TestValidateModule_IncludesInstructionSourceLocation(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body[1].LocalIndex = 99
	m.Funcs[0].Body[1].SourceLoc = "12:34"

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "12:34") {
		t.Fatalf("got errors %q, want source location 12:34", errs.Error())
	}
}

func TestValidateModule_StackUnderflow(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body = []Instruction{{Kind: InstrI32Add}, {Kind: InstrEnd}}

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "i32.add needs 2 operands") {
		t.Fatalf("got errors %q, want i32.add stack error", errs.Error())
	}
}

func TestValidateModule_ResultArityMismatch(t *testing.T) {
	m := makeValidAddModule()
	m.Funcs[0].Body = []Instruction{
		{Kind: InstrLocalGet, LocalIndex: 0},
		{Kind: InstrLocalGet, LocalIndex: 1},
		{Kind: InstrEnd},
	}

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "result arity mismatch") {
		t.Fatalf("got errors %q, want result arity mismatch", errs.Error())
	}
}

func TestValidateModule_ExportIndexOutOfRange(t *testing.T) {
	m := makeValidAddModule()
	m.Exports[0].Index = 5

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "index 5 out of range") {
		t.Fatalf("got errors %q, want export index out of range", errs.Error())
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

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want diagnostics")
	}
	errs := asErrorList(t, err)
	if len(errs) < 2 {
		t.Fatalf("got %d diagnostics, want >=2 (%v)", len(errs), errs.Error())
	}
	if !errorListContains(errs, "invalid type index") {
		t.Fatalf("got errors %q, missing invalid type index", errs.Error())
	}
	if !errorListContains(errs, "out of range") {
		t.Fatalf("got errors %q, missing export out-of-range", errs.Error())
	}
}
