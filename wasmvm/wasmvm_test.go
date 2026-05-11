package wasmvm_test

import (
	"testing"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/wasmir"
	"github.com/eliben/watgo/wasmvm"
)

func parseWAT(t *testing.T, src string) *wasmir.Module {
	t.Helper()

	m, err := watgo.ParseAndValidateWAT([]byte(src))
	if err != nil {
		t.Fatalf("ParseAndValidateWAT failed: %v", err)
	}
	return m
}

func TestExportedAdd(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "add") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.add))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	add, ok := inst.ExportedFunc("add")
	if !ok {
		t.Fatal("missing add export")
	}
	results, err := add.Call(wasmvm.I32(3), wasmvm.I32(4))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("got results %#v, want i32 7", results)
	}
}

func TestHostFunctionImport(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(import "env" "inc" (func $inc (param i32) (result i32)))
			(func (export "call_inc") (param i32) (result i32)
				local.get 0
				call $inc))
	`), wasmvm.Imports{
		"env": {
			"inc": wasmvm.NewHostFunc(
				[]wasmir.ValueType{wasmir.ValueTypeI32},
				[]wasmir.ValueType{wasmir.ValueTypeI32},
				func(_ *wasmvm.Context, args []wasmvm.Value) ([]wasmvm.Value, error) {
					return []wasmvm.Value{wasmvm.I32(args[0].I32 + 1)}, nil
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	callInc, ok := inst.ExportedFunc("call_inc")
	if !ok {
		t.Fatal("missing call_inc export")
	}
	results, err := callInc.Call(wasmvm.I32(41))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("got results %#v, want i32 42", results)
	}
}

func TestI32Arithmetic(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "calc") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.mul
				i32.const 7
				i32.sub))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	calc, ok := inst.ExportedFunc("calc")
	if !ok {
		t.Fatal("missing calc export")
	}
	results, err := calc.Call(wasmvm.I32(6), wasmvm.I32(5))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(23) {
		t.Fatalf("got results %#v, want i32 23", results)
	}
}

func TestLocalSetAndTee(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "locals") (param i32) (result i32)
				(local i32)
				local.get 0
				local.set 1
				local.get 1
				i32.const 3
				i32.add
				local.tee 1
				local.get 1
				i32.add))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	locals, ok := inst.ExportedFunc("locals")
	if !ok {
		t.Fatal("missing locals export")
	}
	results, err := locals.Call(wasmvm.I32(4))
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(14) {
		t.Fatalf("got results %#v, want i32 14", results)
	}
}

func TestI32Predicates(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "eqz") (param i32) (result i32)
				local.get 0
				i32.eqz)
			(func (export "cmp") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.lt_s
				local.get 0
				local.get 1
				i32.ne
				i32.add)
			(func (export "eq") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.eq)
			(func (export "le") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.le_s)
			(func (export "gt") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.gt_s)
			(func (export "ge") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.ge_s))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	eqz, ok := inst.ExportedFunc("eqz")
	if !ok {
		t.Fatal("missing eqz export")
	}
	results, err := eqz.Call(wasmvm.I32(0))
	if err != nil {
		t.Fatalf("Call eqz(0) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("eqz(0) got results %#v, want i32 1", results)
	}
	results, err = eqz.Call(wasmvm.I32(9))
	if err != nil {
		t.Fatalf("Call eqz(9) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("eqz(9) got results %#v, want i32 0", results)
	}

	cmp, ok := inst.ExportedFunc("cmp")
	if !ok {
		t.Fatal("missing cmp export")
	}
	results, err = cmp.Call(wasmvm.I32(-2), wasmvm.I32(5))
	if err != nil {
		t.Fatalf("Call cmp failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(2) {
		t.Fatalf("cmp got results %#v, want i32 2", results)
	}

	for _, tt := range []struct {
		name string
		lhs  int32
		rhs  int32
		want int32
	}{
		{name: "eq", lhs: 8, rhs: 8, want: 1},
		{name: "le", lhs: -3, rhs: -2, want: 1},
		{name: "gt", lhs: 10, rhs: 4, want: 1},
		{name: "ge", lhs: 5, rhs: 5, want: 1},
	} {
		f, ok := inst.ExportedFunc(tt.name)
		if !ok {
			t.Fatalf("missing %s export", tt.name)
		}
		results, err = f.Call(wasmvm.I32(tt.lhs), wasmvm.I32(tt.rhs))
		if err != nil {
			t.Fatalf("Call %s failed: %v", tt.name, err)
		}
		if len(results) != 1 || results[0] != wasmvm.I32(tt.want) {
			t.Fatalf("%s got results %#v, want i32 %d", tt.name, results, tt.want)
		}
	}
}

func TestI64ArithmeticAndPredicates(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "calc") (param i64 i64) (result i64)
				local.get 0
				local.get 1
				i64.mul
				i64.const 9
				i64.sub)
			(func (export "eqz") (param i64) (result i32)
				local.get 0
				i64.eqz)
			(func (export "cmp") (param i64 i64) (result i32)
				local.get 0
				local.get 1
				i64.ge_s))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	calc, ok := inst.ExportedFunc("calc")
	if !ok {
		t.Fatal("missing calc export")
	}
	results, err := calc.Call(wasmvm.I64(8), wasmvm.I64(7))
	if err != nil {
		t.Fatalf("Call calc failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I64(47) {
		t.Fatalf("calc got results %#v, want i64 47", results)
	}

	eqz, ok := inst.ExportedFunc("eqz")
	if !ok {
		t.Fatal("missing eqz export")
	}
	results, err = eqz.Call(wasmvm.I64(0))
	if err != nil {
		t.Fatalf("Call eqz failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("eqz got results %#v, want i32 1", results)
	}

	cmp, ok := inst.ExportedFunc("cmp")
	if !ok {
		t.Fatal("missing cmp export")
	}
	results, err = cmp.Call(wasmvm.I64(-2), wasmvm.I64(5))
	if err != nil {
		t.Fatalf("Call cmp failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("cmp got results %#v, want i32 0", results)
	}
}

func TestF32ArithmeticAndPredicates(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "calc") (param f32) (result f32)
				local.get 0
				f32.const 2.5
				f32.mul
				f32.const 1.0
				f32.add)
			(func (export "cmp") (param f32 f32) (result i32)
				local.get 0
				local.get 1
				f32.lt))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	calc, ok := inst.ExportedFunc("calc")
	if !ok {
		t.Fatal("missing calc export")
	}
	results, err := calc.Call(wasmvm.F32(4))
	if err != nil {
		t.Fatalf("Call calc failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.F32(11) {
		t.Fatalf("calc got results %#v, want f32 11", results)
	}

	cmp, ok := inst.ExportedFunc("cmp")
	if !ok {
		t.Fatal("missing cmp export")
	}
	results, err = cmp.Call(wasmvm.F32(-1.5), wasmvm.F32(2.25))
	if err != nil {
		t.Fatalf("Call cmp failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("cmp got results %#v, want i32 1", results)
	}
}

func TestF64ArithmeticAndPredicates(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "calc") (param f64) (result f64)
				local.get 0
				f64.const 8.0
				f64.add
				f64.const 2.0
				f64.div)
			(func (export "cmp") (param f64 f64) (result i32)
				local.get 0
				local.get 1
				f64.ge))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	calc, ok := inst.ExportedFunc("calc")
	if !ok {
		t.Fatal("missing calc export")
	}
	results, err := calc.Call(wasmvm.F64(6))
	if err != nil {
		t.Fatalf("Call calc failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.F64(7) {
		t.Fatalf("calc got results %#v, want f64 7", results)
	}

	cmp, ok := inst.ExportedFunc("cmp")
	if !ok {
		t.Fatal("missing cmp export")
	}
	results, err = cmp.Call(wasmvm.F64(3.5), wasmvm.F64(3.5))
	if err != nil {
		t.Fatalf("Call cmp failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("cmp got results %#v, want i32 1", results)
	}
}

func TestDropAndReturn(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "early") (param i32) (result i32)
				local.get 0
				i32.eqz
				if
					i32.const 42
					return
				end
				i32.const 100
				drop
				local.get 0))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	early, ok := inst.ExportedFunc("early")
	if !ok {
		t.Fatal("missing early export")
	}
	results, err := early.Call(wasmvm.I32(0))
	if err != nil {
		t.Fatalf("Call early(0) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("early(0) got results %#v, want i32 42", results)
	}
	results, err = early.Call(wasmvm.I32(9))
	if err != nil {
		t.Fatalf("Call early(9) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(9) {
		t.Fatalf("early(9) got results %#v, want i32 9", results)
	}
}

func TestIfElse(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "abs") (param i32) (result i32)
				local.get 0
				i32.const 0
				i32.lt_s
				if (result i32)
					i32.const 0
					local.get 0
					i32.sub
				else
					local.get 0
				end))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	abs, ok := inst.ExportedFunc("abs")
	if !ok {
		t.Fatal("missing abs export")
	}
	results, err := abs.Call(wasmvm.I32(-7))
	if err != nil {
		t.Fatalf("Call abs(-7) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("abs(-7) got results %#v, want i32 7", results)
	}
	results, err = abs.Call(wasmvm.I32(5))
	if err != nil {
		t.Fatalf("Call abs(5) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(5) {
		t.Fatalf("abs(5) got results %#v, want i32 5", results)
	}
}

func TestBlockBranch(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "skip") (result i32)
				block (result i32)
					i32.const 99
					br 0
					i32.const 10
				end))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	skip, ok := inst.ExportedFunc("skip")
	if !ok {
		t.Fatal("missing skip export")
	}
	results, err := skip.Call()
	if err != nil {
		t.Fatalf("Call skip failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(99) {
		t.Fatalf("skip got results %#v, want i32 99", results)
	}
}

func TestBlockBranchIf(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "clamp_zero") (param i32) (result i32)
				block (result i32)
					local.get 0
					local.get 0
					i32.const 0
					i32.ge_s
					br_if 0
					drop
					i32.const 0
				end))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	clampZero, ok := inst.ExportedFunc("clamp_zero")
	if !ok {
		t.Fatal("missing clamp_zero export")
	}
	results, err := clampZero.Call(wasmvm.I32(12))
	if err != nil {
		t.Fatalf("Call clamp_zero(12) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(12) {
		t.Fatalf("clamp_zero(12) got results %#v, want i32 12", results)
	}
	results, err = clampZero.Call(wasmvm.I32(-3))
	if err != nil {
		t.Fatalf("Call clamp_zero(-3) failed: %v", err)
	}
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("clamp_zero(-3) got results %#v, want i32 0", results)
	}
}

// The execution-error tests below use hand-built wasmir modules instead of WAT.
// WAT parsing validates stack shape and function indices before the runtime
// sees the code, but these tests specifically check the diagnostics produced
// when the VM encounters invalid runtime state.

func TestExecutionErrorInstructionContext(t *testing.T) {
	// A binary instruction with no operands should report the instruction's pc
	// and opcode in addition to the low-level stack underflow.
	err := callInvalidRuntimeModule(t, []wasmir.Instruction{
		{Kind: wasmir.InstrI32Add},
		{Kind: wasmir.InstrEnd},
	}, []wasmir.ValueType{wasmir.ValueTypeI32})

	if got, want := err.Error(), "pc 0 i32.add: operand stack underflow"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestExecutionErrorCallContext(t *testing.T) {
	// A call to an invalid function index should report the call instruction's
	// pc and opcode along with the resolver error.
	err := callInvalidRuntimeModule(t, []wasmir.Instruction{
		{Kind: wasmir.InstrCall, FuncIndex: 3},
		{Kind: wasmir.InstrEnd},
	}, nil)

	if got, want := err.Error(), "pc 0 call: call function index 3 out of range"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestExecutionErrorResultContext(t *testing.T) {
	// A function that declares a result but leaves the stack empty should report
	// the final end instruction as the failing execution point.
	err := callInvalidRuntimeModule(t, []wasmir.Instruction{
		{Kind: wasmir.InstrEnd},
	}, []wasmir.ValueType{wasmir.ValueTypeI32})

	if got, want := err.Error(), "pc 0 end: operand stack underflow"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func invalidRuntimeModule(body []wasmir.Instruction, results []wasmir.ValueType) *wasmir.Module {
	return &wasmir.Module{
		Types: []wasmir.TypeDef{{
			Kind:    wasmir.TypeDefKindFunc,
			Results: results,
		}},
		Funcs: []wasmir.Function{{
			TypeIdx: 0,
			Body:    body,
		}},
		Exports: []wasmir.Export{{
			Name:  "run",
			Kind:  wasmir.ExternalKindFunction,
			Index: 0,
		}},
	}
}

func callInvalidRuntimeModule(t *testing.T, body []wasmir.Instruction, results []wasmir.ValueType) error {
	t.Helper()

	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(invalidRuntimeModule(body, results), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("run")
	if !ok {
		t.Fatal("missing run export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	return err
}
