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
	if !errorListContains(errs, "result arity mismatch") &&
		!errorListContains(errs, "block stack height mismatch") {
		t.Fatalf("got errors %q, want result arity mismatch or equivalent block stack mismatch", errs.Error())
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

func TestValidateModule_CallTypeMismatch(t *testing.T) {
	m := &Module{
		Types: []FuncType{
			{Params: []ValueType{ValueTypeI32}, Results: []ValueType{ValueTypeI32}}, // callee
			{Params: []ValueType{}, Results: []ValueType{ValueTypeI32}},             // caller
			{Params: []ValueType{ValueTypeF32}, Results: []ValueType{ValueTypeI32}}, // bad caller
		},
		Funcs: []Function{
			{
				TypeIdx:    0,
				Name:       "$callee",
				ParamNames: []string{"$x"},
				Body: []Instruction{
					{Kind: InstrLocalGet, LocalIndex: 0},
					{Kind: InstrEnd},
				},
			},
			{
				TypeIdx: 1,
				Body: []Instruction{
					{Kind: InstrI32Const, I32Const: 7},
					{Kind: InstrCall, FuncIndex: 0},
					{Kind: InstrEnd},
				},
			},
			{
				TypeIdx: 2,
				Name:    "$badcaller",
				Body: []Instruction{
					{Kind: InstrLocalGet, LocalIndex: 0},
					{Kind: InstrCall, FuncIndex: 0},
					{Kind: InstrEnd},
				},
			},
		},
	}

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "func[2] $badcaller") {
		t.Fatalf("got errors %q, want caller function name in context", errs.Error())
	}
	if !errorListContains(errs, "call to func[0] $callee expects operand 0 ($x) to be i32") {
		t.Fatalf("got errors %q, want call operand type mismatch", errs.Error())
	}
}

func TestValidateModule_IfElseWithResult(t *testing.T) {
	m := &Module{
		Types: []FuncType{
			{Params: nil, Results: []ValueType{ValueTypeI64}},
		},
		Funcs: []Function{
			{
				TypeIdx: 0,
				Body: []Instruction{
					{Kind: InstrI64Const, I64Const: 0},
					{Kind: InstrI64Eqz},
					{Kind: InstrIf, BlockHasResult: true, BlockType: ValueTypeI64},
					{Kind: InstrI64Const, I64Const: 1},
					{Kind: InstrElse},
					{Kind: InstrI64Const, I64Const: 2},
					{Kind: InstrEnd},
					{Kind: InstrEnd},
				},
			},
		},
	}

	if err := ValidateModule(m); err != nil {
		t.Fatalf("ValidateModule error: %v", err)
	}
}

func TestValidateModule_Memory64PageLimit(t *testing.T) {
	m := &Module{
		Memories: []Memory{
			{AddressType: ValueTypeI64, Min: maxMemoryPages64 + 1},
		},
	}

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "memory[0]: memory size") {
		t.Fatalf("got errors %q, want memory size diagnostic", errs.Error())
	}
}

func TestValidateModule_RejectsTooLargeMemoryAlignment(t *testing.T) {
	m := &Module{
		Types: []FuncType{{Params: nil, Results: nil}},
		Memories: []Memory{
			{AddressType: ValueTypeI64, Min: 1},
		},
		Funcs: []Function{{
			TypeIdx: 0,
			Body: []Instruction{
				{Kind: InstrI64Const, I64Const: 0},
				{Kind: InstrI32Load8S, MemoryAlign: 1},
				{Kind: InstrDrop},
				{Kind: InstrEnd},
			},
		}},
	}

	err := ValidateModule(m)
	if err == nil {
		t.Fatal("ValidateModule returned nil error, want failure")
	}
	errs := asErrorList(t, err)
	if !errorListContains(errs, "alignment must not be larger than natural") {
		t.Fatalf("got errors %q, want alignment diagnostic", errs.Error())
	}
}
