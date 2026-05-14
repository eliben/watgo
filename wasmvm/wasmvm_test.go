package wasmvm_test

import (
	"math"
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

func callExport(t *testing.T, inst *wasmvm.ModuleInstance, name string, args ...wasmvm.Value) []wasmvm.Value {
	t.Helper()

	f, ok := inst.ExportedFunc(name)
	if !ok {
		t.Fatalf("missing %s export", name)
	}
	results, err := f.Call(args...)
	if err != nil {
		t.Fatalf("Call %s failed: %v", name, err)
	}
	return results
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

	results := callExport(t, inst, "add", wasmvm.I32(3), wasmvm.I32(4))
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("got results %#v, want i32 7", results)
	}
}

// TestNop checks that nop executes without changing the operand stack or
// control flow.
func TestNop(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "run") (result i32)
				nop
				i32.const 40
				nop
				i32.const 2
				i32.add
				nop))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "run")
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("run got results %#v, want i32 42", results)
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

	results := callExport(t, inst, "call_inc", wasmvm.I32(41))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("got results %#v, want i32 42", results)
	}
}

// TestReturnCall checks that return_call invokes a module-defined function and
// immediately returns its results from the current function.
func TestReturnCall(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func $add (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.add)
			(func (export "tail_add") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				return_call $add
				i32.const 99))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "tail_add", wasmvm.I32(20), wasmvm.I32(22))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("tail_add got results %#v, want i32 42", results)
	}
}

// TestReturnCallHostFunction checks that return_call can tail-call an imported
// host function through the same resolver path as call.
func TestReturnCallHostFunction(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(import "env" "double" (func $double (param i32) (result i32)))
			(func (export "tail_double") (param i32) (result i32)
				local.get 0
				return_call $double
				i32.const 99))
	`), wasmvm.Imports{
		"env": {
			"double": wasmvm.NewHostFunc(
				[]wasmir.ValueType{wasmir.ValueTypeI32},
				[]wasmir.ValueType{wasmir.ValueTypeI32},
				func(_ *wasmvm.Context, args []wasmvm.Value) ([]wasmvm.Value, error) {
					return []wasmvm.Value{wasmvm.I32(args[0].I32 * 2)}, nil
				},
			),
		},
	})
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "tail_double", wasmvm.I32(21))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("tail_double got results %#v, want i32 42", results)
	}
}

// TestReferenceInstructions checks the minimal reference instruction set:
// ref.null, ref.func, and ref.is_null.
func TestReferenceInstructions(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func $target)
			(elem declare func $target)
			(func (export "null_is_null") (result i32)
				ref.null func
				ref.is_null)
			(func (export "func_is_null") (result i32)
				ref.func $target
				ref.is_null)
			(func (export "return_null") (result funcref)
				ref.null func)
			(func (export "return_func") (result funcref)
				ref.func $target)
			(func (export "local_func_is_null") (result i32)
				(local funcref)
				ref.func $target
				local.set 0
				local.get 0
				ref.is_null))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "null_is_null")
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("null_is_null got results %#v, want i32 1", results)
	}
	results = callExport(t, inst, "func_is_null")
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("func_is_null got results %#v, want i32 0", results)
	}
	results = callExport(t, inst, "local_func_is_null")
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("local_func_is_null got results %#v, want i32 0", results)
	}

	results = callExport(t, inst, "return_null")
	if len(results) != 1 || !results[0].Type.IsRef() {
		t.Fatalf("return_null got results %#v, want one reference", results)
	}
	results = callExport(t, inst, "return_func")
	if len(results) != 1 || !results[0].Type.IsRef() || results[0].Ref.FuncIndex != 0 {
		t.Fatalf("return_func got results %#v, want function reference 0", results)
	}
}

// TestTableBasics checks module-defined table instantiation, active element
// initialization, table.size, table.get, and table.set.
func TestTableBasics(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table 3 funcref)
			(func $a)
			(func $b)
			(elem (i32.const 1) func $a)
			(elem declare func $b)
			(func (export "size") (result i32)
				table.size)
			(func (export "is_null") (param i32) (result i32)
				local.get 0
				table.get
				ref.is_null)
			(func (export "set_b") (param i32)
				local.get 0
				ref.func $b
				table.set))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "size")
	if len(results) != 1 || results[0] != wasmvm.I32(3) {
		t.Fatalf("size got results %#v, want i32 3", results)
	}
	results = callExport(t, inst, "is_null", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("is_null(0) got results %#v, want i32 1", results)
	}
	results = callExport(t, inst, "is_null", wasmvm.I32(1))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("is_null(1) got results %#v, want i32 0", results)
	}
	callExport(t, inst, "set_b", wasmvm.I32(2))
	results = callExport(t, inst, "is_null", wasmvm.I32(2))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("is_null(2) after set got results %#v, want i32 0", results)
	}
}

// TestTableGrowFillCopy checks table.grow failure/success behavior and the
// bulk table.fill/table.copy operations over reference values.
func TestTableGrowFillCopy(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table 2 5 funcref)
			(func $a)
			(func $b)
			(elem declare func $a $b)
			(func (export "size") (result i32)
				table.size)
			(func (export "is_null") (param i32) (result i32)
				local.get 0
				table.get
				ref.is_null)
			(func (export "grow") (param i32) (result i32)
				ref.func $a
				local.get 0
				table.grow)
			(func (export "fill_b") (param i32 i32)
				local.get 0
				ref.func $b
				local.get 1
				table.fill)
			(func (export "fill_null") (param i32 i32)
				local.get 0
				ref.null func
				local.get 1
				table.fill)
			(func (export "copy") (param i32 i32 i32)
				local.get 0
				local.get 1
				local.get 2
				table.copy))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "size")
	if len(results) != 1 || results[0] != wasmvm.I32(2) {
		t.Fatalf("size got results %#v, want i32 2", results)
	}
	results = callExport(t, inst, "grow", wasmvm.I32(2))
	if len(results) != 1 || results[0] != wasmvm.I32(2) {
		t.Fatalf("grow got results %#v, want old size i32 2", results)
	}
	results = callExport(t, inst, "size")
	if len(results) != 1 || results[0] != wasmvm.I32(4) {
		t.Fatalf("size after grow got results %#v, want i32 4", results)
	}
	results = callExport(t, inst, "grow", wasmvm.I32(2))
	if len(results) != 1 || results[0] != wasmvm.I32(-1) {
		t.Fatalf("over-max grow got results %#v, want i32 -1", results)
	}

	callExport(t, inst, "fill_null", wasmvm.I32(2), wasmvm.I32(2))
	results = callExport(t, inst, "is_null", wasmvm.I32(2))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("is_null(2) after fill_null got results %#v, want i32 1", results)
	}
	callExport(t, inst, "fill_b", wasmvm.I32(0), wasmvm.I32(2))
	callExport(t, inst, "copy", wasmvm.I32(2), wasmvm.I32(0), wasmvm.I32(2))
	results = callExport(t, inst, "is_null", wasmvm.I32(3))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("is_null(3) after copy got results %#v, want i32 0", results)
	}
}

// TestPassiveElementSegments checks that table.init can copy from a passive
// element segment and elem.drop makes that segment unavailable afterward.
func TestPassiveElementSegments(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table 2 funcref)
			(func $a)
			(elem $e funcref (ref.func $a))
			(func (export "init") (param i32 i32 i32)
				local.get 0
				local.get 1
				local.get 2
				table.init $e)
			(func (export "drop")
				elem.drop $e)
			(func (export "is_null") (param i32) (result i32)
				local.get 0
				table.get
				ref.is_null))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "is_null", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("is_null(0) before init got results %#v, want i32 1", results)
	}
	callExport(t, inst, "init", wasmvm.I32(0), wasmvm.I32(0), wasmvm.I32(1))
	results = callExport(t, inst, "is_null", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("is_null(0) after init got results %#v, want i32 0", results)
	}
	callExport(t, inst, "drop")
	initFunc, ok := inst.ExportedFunc("init")
	if !ok {
		t.Fatal("missing init export")
	}
	_, err = initFunc.Call(wasmvm.I32(1), wasmvm.I32(0), wasmvm.I32(1))
	if err == nil {
		t.Fatal("Call init after elem.drop succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 3 table.init: element segment 0 is dropped"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestIndirectCalls checks call_indirect and return_call_indirect through a
// funcref table populated by an active element segment.
func TestIndirectCalls(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $binary (func (param i32 i32) (result i32)))
			(table 3 funcref)
			(func $add (type $binary)
				local.get 0
				local.get 1
				i32.add)
			(func $sub (type $binary)
				local.get 0
				local.get 1
				i32.sub)
			(elem (i32.const 0) func $add $sub)
			(func (export "call") (param i32 i32 i32) (result i32)
				local.get 0
				local.get 1
				local.get 2
				call_indirect (type $binary))
			(func (export "tail") (param i32 i32 i32) (result i32)
				local.get 0
				local.get 1
				local.get 2
				return_call_indirect (type $binary)))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "call", wasmvm.I32(20), wasmvm.I32(22), wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("call add got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "call", wasmvm.I32(50), wasmvm.I32(8), wasmvm.I32(1))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("call sub got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "tail", wasmvm.I32(45), wasmvm.I32(3), wasmvm.I32(1))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("tail got results %#v, want i32 42", results)
	}
}

// TestIndirectCallTraps checks the runtime traps specific to indirect calls:
// null table elements and function references whose type does not match the
// call_indirect type immediate.
func TestIndirectCallTraps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $binary (func (param i32 i32) (result i32)))
			(type $unary (func (param i32) (result i32)))
			(table 3 funcref)
			(func $add (type $binary)
				local.get 0
				local.get 1
				i32.add)
			(func $inc (type $unary)
				local.get 0
				i32.const 1
				i32.add)
			(elem (i32.const 0) func $add $inc)
			(func (export "call") (param i32 i32 i32) (result i32)
				local.get 0
				local.get 1
				local.get 2
				call_indirect (type $binary)))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	call, ok := inst.ExportedFunc("call")
	if !ok {
		t.Fatal("missing call export")
	}

	_, err = call.Call(wasmvm.I32(1), wasmvm.I32(2), wasmvm.I32(1))
	if err == nil {
		t.Fatal("Call with mismatched indirect target succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 3 call_indirect: indirect call type mismatch"; got != want {
		t.Fatalf("type mismatch error = %q, want %q", got, want)
	}

	_, err = call.Call(wasmvm.I32(1), wasmvm.I32(2), wasmvm.I32(2))
	if err == nil {
		t.Fatal("Call through null table slot succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 3 call_indirect: indirect call to null reference"; got != want {
		t.Fatalf("null reference error = %q, want %q", got, want)
	}
}

// TestCallRef checks call_ref and return_call_ref through function references
// produced by ref.func.
func TestCallRef(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $binary (func (param i32 i32) (result i32)))
			(func $add (type $binary)
				local.get 0
				local.get 1
				i32.add)
			(func $sub (type $binary)
				local.get 0
				local.get 1
				i32.sub)
			(elem declare func $add $sub)
			(func (export "call") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				ref.func $add
				call_ref $binary)
			(func (export "tail") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				ref.func $sub
				return_call_ref $binary))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "call", wasmvm.I32(20), wasmvm.I32(22))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("call got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "tail", wasmvm.I32(50), wasmvm.I32(8))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("tail got results %#v, want i32 42", results)
	}
}

// TestCallRefTraps checks call_ref traps for null references and runtime
// function type mismatches.
func TestCallRefTraps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $binary (func (param i32 i32) (result i32)))
			(func (export "call_null") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				ref.null $binary
				call_ref $binary))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	callNull, ok := inst.ExportedFunc("call_null")
	if !ok {
		t.Fatal("missing call_null export")
	}
	_, err = callNull.Call(wasmvm.I32(1), wasmvm.I32(2))
	if err == nil {
		t.Fatal("Call through null reference succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 3 call_ref: call_ref to null reference"; got != want {
		t.Fatalf("null reference error = %q, want %q", got, want)
	}

	err = callInvalidCallRefRuntimeModule(t)
	if got, want := err.Error(), "pc 3 call_ref: indirect call type mismatch"; got != want {
		t.Fatalf("type mismatch error = %q, want %q", got, want)
	}
}

// TestRefAsNonNull checks that ref.as_non_null passes through a non-null
// function reference and can feed call_ref.
func TestRefAsNonNull(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $binary (func (param i32 i32) (result i32)))
			(func $add (type $binary)
				local.get 0
				local.get 1
				i32.add)
			(elem declare func $add)
			(func (export "call_checked") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				ref.func $add
				ref.as_non_null
				call_ref $binary)
			(func (export "is_null_after_check") (result i32)
				ref.func $add
				ref.as_non_null
				ref.is_null))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "call_checked", wasmvm.I32(20), wasmvm.I32(22))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("call_checked got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "is_null_after_check")
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("is_null_after_check got results %#v, want i32 0", results)
	}
}

// TestRefAsNonNullTrap checks that ref.as_non_null traps when the reference
// operand is null.
func TestRefAsNonNullTrap(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $binary (func (param i32 i32) (result i32)))
			(func (export "check_null")
				ref.null $binary
				ref.as_non_null
				drop))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	checkNull, ok := inst.ExportedFunc("check_null")
	if !ok {
		t.Fatal("missing check_null export")
	}
	_, err = checkNull.Call()
	if err == nil {
		t.Fatal("ref.as_non_null on null reference succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 1 ref.as_non_null: ref.as_non_null to null reference"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestBrOnNull checks both br_on_null paths: a null reference branches and a
// non-null reference falls through as a refined function reference.
func TestBrOnNull(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $result (func (result i32)))
			(func $forty_two (type $result)
				i32.const 42)
			(elem declare func $forty_two)
			(func (export "null_branch") (result i32)
				block $null
					ref.null $result
					br_on_null $null
					drop
					i32.const 99
					return
				end
				i32.const 42)
			(func (export "nonnull_fallthrough") (result i32)
				block $null
					ref.func $forty_two
					br_on_null $null
					call_ref $result
					return
				end
				i32.const 99))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "null_branch")
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("null_branch got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "nonnull_fallthrough")
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("nonnull_fallthrough got results %#v, want i32 42", results)
	}
}

// TestBrOnNonNull checks both br_on_non_null paths: a non-null reference
// branches as a label value and a null reference is consumed on fallthrough.
func TestBrOnNonNull(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $result (func (result i32)))
			(func $seven (type $result)
				i32.const 7)
			(func $forty_two (type $result)
				i32.const 42)
			(elem declare func $seven $forty_two)
			(func (export "nonnull_branch") (result i32)
				block $target (result (ref $result))
					ref.func $forty_two
					br_on_non_null $target
					ref.func $seven
				end
				call_ref $result)
			(func (export "null_fallthrough") (result i32)
				block $target (result (ref $result))
					ref.null $result
					br_on_non_null $target
					ref.func $seven
				end
				call_ref $result))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "nonnull_branch")
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("nonnull_branch got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "null_fallthrough")
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("null_fallthrough got results %#v, want i32 7", results)
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

	results := callExport(t, inst, "calc", wasmvm.I32(6), wasmvm.I32(5))
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

	results := callExport(t, inst, "locals", wasmvm.I32(4))
	if len(results) != 1 || results[0] != wasmvm.I32(14) {
		t.Fatalf("got results %#v, want i32 14", results)
	}
}

func TestSelect(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $result (func (result i32)))
			(func $forty_two (type $result)
				i32.const 42)
			(elem declare func $forty_two)
			(func (export "pick_i32") (param i32) (result i32)
				i32.const 10
				i32.const 20
				local.get 0
				select)
			(func (export "pick_f64") (param i32) (result f64)
				f64.const 1.5
				f64.const 2.5
				local.get 0
				select)
			(func (export "pick_typed_i64") (param i32) (result i64)
				i64.const 30
				i64.const 40
				local.get 0
				select (result i64))
			(func (export "pick_typed_ref") (param i32) (result i32)
				ref.func $forty_two
				ref.null $result
				local.get 0
				select (result (ref null $result))
				ref.as_non_null
				call_ref $result)
			(func (export "pick_null_ref_is_null") (param i32) (result i32)
				ref.func $forty_two
				ref.null $result
				local.get 0
				select (result (ref null $result))
				ref.is_null))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "pick_i32", wasmvm.I32(1))
	if len(results) != 1 || results[0] != wasmvm.I32(10) {
		t.Fatalf("pick_i32(1) got results %#v, want i32 10", results)
	}
	results = callExport(t, inst, "pick_i32", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(20) {
		t.Fatalf("pick_i32(0) got results %#v, want i32 20", results)
	}

	results = callExport(t, inst, "pick_f64", wasmvm.I32(-1))
	if len(results) != 1 || results[0] != wasmvm.F64(1.5) {
		t.Fatalf("pick_f64 got results %#v, want f64 1.5", results)
	}

	results = callExport(t, inst, "pick_typed_i64", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I64(40) {
		t.Fatalf("pick_typed_i64 got results %#v, want i64 40", results)
	}
	results = callExport(t, inst, "pick_typed_ref", wasmvm.I32(1))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("pick_typed_ref got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "pick_null_ref_is_null", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("pick_null_ref_is_null got results %#v, want i32 1", results)
	}
}

// TestRefEqAndConversions checks small reference operations that do not require
// allocating GC objects in the current wasmvm slice.
func TestRefEqAndConversions(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "null_eq") (result i32)
				ref.null eq
				ref.null eq
				ref.eq)
			(func (export "null_any_to_extern_is_null") (result i32)
				ref.null any
				extern.convert_any
				ref.is_null)
			(func (export "null_extern_to_any_is_null") (result i32)
				ref.null extern
				any.convert_extern
				ref.is_null))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "null_eq")
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("null_eq got results %#v, want i32 1", results)
	}
	results = callExport(t, inst, "null_any_to_extern_is_null")
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("null_any_to_extern_is_null got results %#v, want i32 1", results)
	}
	results = callExport(t, inst, "null_extern_to_any_is_null")
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("null_extern_to_any_is_null got results %#v, want i32 1", results)
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

	results := callExport(t, inst, "eqz", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("eqz(0) got results %#v, want i32 1", results)
	}
	results = callExport(t, inst, "eqz", wasmvm.I32(9))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("eqz(9) got results %#v, want i32 0", results)
	}

	results = callExport(t, inst, "cmp", wasmvm.I32(-2), wasmvm.I32(5))
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
		results = callExport(t, inst, tt.name, wasmvm.I32(tt.lhs), wasmvm.I32(tt.rhs))
		if len(results) != 1 || results[0] != wasmvm.I32(tt.want) {
			t.Fatalf("%s got results %#v, want i32 %d", tt.name, results, tt.want)
		}
	}
}

func TestI32ExtendedIntegerOps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "div_s") (param i32 i32) (result i32) local.get 0 local.get 1 i32.div_s)
			(func (export "div_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.div_u)
			(func (export "rem_s") (param i32 i32) (result i32) local.get 0 local.get 1 i32.rem_s)
			(func (export "rem_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.rem_u)
			(func (export "and") (param i32 i32) (result i32) local.get 0 local.get 1 i32.and)
			(func (export "or") (param i32 i32) (result i32) local.get 0 local.get 1 i32.or)
			(func (export "xor") (param i32 i32) (result i32) local.get 0 local.get 1 i32.xor)
			(func (export "shl") (param i32 i32) (result i32) local.get 0 local.get 1 i32.shl)
			(func (export "shr_s") (param i32 i32) (result i32) local.get 0 local.get 1 i32.shr_s)
			(func (export "shr_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.shr_u)
			(func (export "rotl") (param i32 i32) (result i32) local.get 0 local.get 1 i32.rotl)
			(func (export "rotr") (param i32 i32) (result i32) local.get 0 local.get 1 i32.rotr)
			(func (export "lt_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.lt_u)
			(func (export "le_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.le_u)
			(func (export "gt_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.gt_u)
			(func (export "ge_u") (param i32 i32) (result i32) local.get 0 local.get 1 i32.ge_u))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		lhs  int32
		rhs  int32
		want int32
	}{
		{name: "div_s", lhs: -7, rhs: 2, want: -3},
		{name: "div_u", lhs: -1, rhs: 2, want: 2147483647},
		{name: "rem_s", lhs: -7, rhs: 2, want: -1},
		{name: "rem_u", lhs: -1, rhs: 10, want: 5},
		{name: "and", lhs: 0x0f0f, rhs: 0x00ff, want: 0x000f},
		{name: "or", lhs: 0x0f0f, rhs: 0x00ff, want: 0x0fff},
		{name: "xor", lhs: 0x0f0f, rhs: 0x00ff, want: 0x0ff0},
		{name: "shl", lhs: 1, rhs: 33, want: 2},
		{name: "shr_s", lhs: -4, rhs: 1, want: -2},
		{name: "shr_u", lhs: -4, rhs: 1, want: 2147483646},
		{name: "rotl", lhs: 1, rhs: 33, want: 2},
		{name: "rotr", lhs: 2, rhs: 33, want: 1},
		{name: "lt_u", lhs: -1, rhs: 1, want: 0},
		{name: "le_u", lhs: -1, rhs: -1, want: 1},
		{name: "gt_u", lhs: -1, rhs: 1, want: 1},
		{name: "ge_u", lhs: 0, rhs: -1, want: 0},
	} {
		results := callExport(t, inst, tt.name, wasmvm.I32(tt.lhs), wasmvm.I32(tt.rhs))
		if len(results) != 1 || results[0] != wasmvm.I32(tt.want) {
			t.Fatalf("%s got results %#v, want i32 %d", tt.name, results, tt.want)
		}
	}
}

// TestIntegerUnaryAndSignExtension checks core integer unary operators and
// sign-extension operators for both i32 and i64.
func TestIntegerUnaryAndSignExtension(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "i32_counts") (param i32) (result i32 i32 i32)
				local.get 0
				i32.clz
				local.get 0
				i32.ctz
				local.get 0
				i32.popcnt)
			(func (export "i64_counts") (param i64) (result i64 i64 i64)
				local.get 0
				i64.clz
				local.get 0
				i64.ctz
				local.get 0
				i64.popcnt)
			(func (export "i32_ext8") (param i32) (result i32)
				local.get 0
				i32.extend8_s)
			(func (export "i32_ext16") (param i32) (result i32)
				local.get 0
				i32.extend16_s)
			(func (export "i64_ext8") (param i64) (result i64)
				local.get 0
				i64.extend8_s)
			(func (export "i64_ext16") (param i64) (result i64)
				local.get 0
				i64.extend16_s)
			(func (export "i64_ext32") (param i64) (result i64)
				local.get 0
				i64.extend32_s))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "i32_counts", wasmvm.I32(0x00f00000))
	if len(results) != 3 || results[0] != wasmvm.I32(8) || results[1] != wasmvm.I32(20) || results[2] != wasmvm.I32(4) {
		t.Fatalf("i32_counts got results %#v, want [8 20 4]", results)
	}
	results = callExport(t, inst, "i32_counts", wasmvm.I32(0))
	if len(results) != 3 || results[0] != wasmvm.I32(32) || results[1] != wasmvm.I32(32) || results[2] != wasmvm.I32(0) {
		t.Fatalf("i32_counts(0) got results %#v, want [32 32 0]", results)
	}

	results = callExport(t, inst, "i64_counts", wasmvm.I64(0x00f0000000000000))
	if len(results) != 3 || results[0] != wasmvm.I64(8) || results[1] != wasmvm.I64(52) || results[2] != wasmvm.I64(4) {
		t.Fatalf("i64_counts got results %#v, want [8 52 4]", results)
	}
	results = callExport(t, inst, "i64_counts", wasmvm.I64(0))
	if len(results) != 3 || results[0] != wasmvm.I64(64) || results[1] != wasmvm.I64(64) || results[2] != wasmvm.I64(0) {
		t.Fatalf("i64_counts(0) got results %#v, want [64 64 0]", results)
	}

	for _, tt := range []struct {
		name string
		arg  wasmvm.Value
		want wasmvm.Value
	}{
		{name: "i32_ext8", arg: wasmvm.I32(0x80), want: wasmvm.I32(-128)},
		{name: "i32_ext16", arg: wasmvm.I32(0x8001), want: wasmvm.I32(-32767)},
		{name: "i64_ext8", arg: wasmvm.I64(0xff), want: wasmvm.I64(-1)},
		{name: "i64_ext16", arg: wasmvm.I64(0x8001), want: wasmvm.I64(-32767)},
		{name: "i64_ext32", arg: wasmvm.I64(0x80000001), want: wasmvm.I64(-2147483647)},
	} {
		results := callExport(t, inst, tt.name, tt.arg)
		if len(results) != 1 || results[0] != tt.want {
			t.Fatalf("%s got results %#v, want %v", tt.name, results, tt.want)
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

	results := callExport(t, inst, "calc", wasmvm.I64(8), wasmvm.I64(7))
	if len(results) != 1 || results[0] != wasmvm.I64(47) {
		t.Fatalf("calc got results %#v, want i64 47", results)
	}

	results = callExport(t, inst, "eqz", wasmvm.I64(0))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("eqz got results %#v, want i32 1", results)
	}

	results = callExport(t, inst, "cmp", wasmvm.I64(-2), wasmvm.I64(5))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("cmp got results %#v, want i32 0", results)
	}
}

func TestI64ExtendedIntegerOps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "div_s") (param i64 i64) (result i64) local.get 0 local.get 1 i64.div_s)
			(func (export "div_u") (param i64 i64) (result i64) local.get 0 local.get 1 i64.div_u)
			(func (export "rem_s") (param i64 i64) (result i64) local.get 0 local.get 1 i64.rem_s)
			(func (export "rem_u") (param i64 i64) (result i64) local.get 0 local.get 1 i64.rem_u)
			(func (export "and") (param i64 i64) (result i64) local.get 0 local.get 1 i64.and)
			(func (export "or") (param i64 i64) (result i64) local.get 0 local.get 1 i64.or)
			(func (export "xor") (param i64 i64) (result i64) local.get 0 local.get 1 i64.xor)
			(func (export "shl") (param i64 i64) (result i64) local.get 0 local.get 1 i64.shl)
			(func (export "shr_s") (param i64 i64) (result i64) local.get 0 local.get 1 i64.shr_s)
			(func (export "shr_u") (param i64 i64) (result i64) local.get 0 local.get 1 i64.shr_u)
			(func (export "rotl") (param i64 i64) (result i64) local.get 0 local.get 1 i64.rotl)
			(func (export "rotr") (param i64 i64) (result i64) local.get 0 local.get 1 i64.rotr)
			(func (export "lt_u") (param i64 i64) (result i32) local.get 0 local.get 1 i64.lt_u)
			(func (export "le_u") (param i64 i64) (result i32) local.get 0 local.get 1 i64.le_u)
			(func (export "gt_u") (param i64 i64) (result i32) local.get 0 local.get 1 i64.gt_u)
			(func (export "ge_u") (param i64 i64) (result i32) local.get 0 local.get 1 i64.ge_u))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		lhs  int64
		rhs  int64
		want int64
	}{
		{name: "div_s", lhs: -9, rhs: 2, want: -4},
		{name: "div_u", lhs: -1, rhs: 3, want: 6148914691236517205},
		{name: "rem_s", lhs: -9, rhs: 2, want: -1},
		{name: "rem_u", lhs: -1, rhs: 10, want: 5},
		{name: "and", lhs: 0x0f0f, rhs: 0x00ff, want: 0x000f},
		{name: "or", lhs: 0x0f0f, rhs: 0x00ff, want: 0x0fff},
		{name: "xor", lhs: 0x0f0f, rhs: 0x00ff, want: 0x0ff0},
		{name: "shl", lhs: 1, rhs: 65, want: 2},
		{name: "shr_s", lhs: -8, rhs: 1, want: -4},
		{name: "shr_u", lhs: -8, rhs: 1, want: 9223372036854775804},
		{name: "rotl", lhs: 1, rhs: 65, want: 2},
		{name: "rotr", lhs: 2, rhs: 65, want: 1},
	} {
		results := callExport(t, inst, tt.name, wasmvm.I64(tt.lhs), wasmvm.I64(tt.rhs))
		if len(results) != 1 || results[0] != wasmvm.I64(tt.want) {
			t.Fatalf("%s got results %#v, want i64 %d", tt.name, results, tt.want)
		}
	}

	for _, tt := range []struct {
		name string
		lhs  int64
		rhs  int64
		want int32
	}{
		{name: "lt_u", lhs: -1, rhs: 1, want: 0},
		{name: "le_u", lhs: -1, rhs: -1, want: 1},
		{name: "gt_u", lhs: -1, rhs: 1, want: 1},
		{name: "ge_u", lhs: 0, rhs: -1, want: 0},
	} {
		results := callExport(t, inst, tt.name, wasmvm.I64(tt.lhs), wasmvm.I64(tt.rhs))
		if len(results) != 1 || results[0] != wasmvm.I32(tt.want) {
			t.Fatalf("%s got results %#v, want i32 %d", tt.name, results, tt.want)
		}
	}
}

func TestIntegerTrapErrors(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "i32_div_zero") (result i32)
				i32.const 1
				i32.const 0
				i32.div_s)
			(func (export "i32_div_overflow") (result i32)
				i32.const -2147483648
				i32.const -1
				i32.div_s)
			(func (export "i64_div_zero") (result i64)
				i64.const 1
				i64.const 0
				i64.div_s)
			(func (export "i64_div_overflow") (result i64)
				i64.const -9223372036854775808
				i64.const -1
				i64.div_s))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		want string
	}{
		{name: "i32_div_zero", want: "pc 2 i32.div_s: integer divide by zero"},
		{name: "i32_div_overflow", want: "pc 2 i32.div_s: integer overflow"},
		{name: "i64_div_zero", want: "pc 2 i64.div_s: integer divide by zero"},
		{name: "i64_div_overflow", want: "pc 2 i64.div_s: integer overflow"},
	} {
		f, ok := inst.ExportedFunc(tt.name)
		if !ok {
			t.Fatalf("missing %s export", tt.name)
		}
		_, err := f.Call()
		if err == nil {
			t.Fatalf("Call %s succeeded unexpectedly", tt.name)
		}
		if got := err.Error(); got != tt.want {
			t.Fatalf("%s error = %q, want %q", tt.name, got, tt.want)
		}
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

	results := callExport(t, inst, "calc", wasmvm.F32(4))
	if len(results) != 1 || results[0] != wasmvm.F32(11) {
		t.Fatalf("calc got results %#v, want f32 11", results)
	}

	results = callExport(t, inst, "cmp", wasmvm.F32(-1.5), wasmvm.F32(2.25))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("cmp got results %#v, want i32 1", results)
	}
}

// TestF32UnaryOps checks non-converting f32 unary instructions, including
// nearest's ties-to-even behavior.
func TestF32UnaryOps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "abs") (param f32) (result f32) local.get 0 f32.abs)
			(func (export "neg") (param f32) (result f32) local.get 0 f32.neg)
			(func (export "sqrt") (param f32) (result f32) local.get 0 f32.sqrt)
			(func (export "ceil") (param f32) (result f32) local.get 0 f32.ceil)
			(func (export "floor") (param f32) (result f32) local.get 0 f32.floor)
			(func (export "trunc") (param f32) (result f32) local.get 0 f32.trunc)
			(func (export "nearest") (param f32) (result f32) local.get 0 f32.nearest))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		arg  float32
		want float32
	}{
		{name: "abs", arg: -2.25, want: 2.25},
		{name: "neg", arg: 2.25, want: -2.25},
		{name: "sqrt", arg: 9, want: 3},
		{name: "ceil", arg: 2.25, want: 3},
		{name: "floor", arg: 2.75, want: 2},
		{name: "trunc", arg: -2.75, want: -2},
		{name: "nearest", arg: 2.5, want: 2},
		{name: "nearest", arg: 3.5, want: 4},
	} {
		results := callExport(t, inst, tt.name, wasmvm.F32(tt.arg))
		if len(results) != 1 || results[0] != wasmvm.F32(tt.want) {
			t.Fatalf("%s(%v) got results %#v, want f32 %v", tt.name, tt.arg, results, tt.want)
		}
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

	results := callExport(t, inst, "calc", wasmvm.F64(6))
	if len(results) != 1 || results[0] != wasmvm.F64(7) {
		t.Fatalf("calc got results %#v, want f64 7", results)
	}

	results = callExport(t, inst, "cmp", wasmvm.F64(3.5), wasmvm.F64(3.5))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("cmp got results %#v, want i32 1", results)
	}
}

// TestF64UnaryOps checks non-converting f64 unary instructions, including
// nearest's ties-to-even behavior.
func TestF64UnaryOps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "abs") (param f64) (result f64) local.get 0 f64.abs)
			(func (export "neg") (param f64) (result f64) local.get 0 f64.neg)
			(func (export "sqrt") (param f64) (result f64) local.get 0 f64.sqrt)
			(func (export "ceil") (param f64) (result f64) local.get 0 f64.ceil)
			(func (export "floor") (param f64) (result f64) local.get 0 f64.floor)
			(func (export "trunc") (param f64) (result f64) local.get 0 f64.trunc)
			(func (export "nearest") (param f64) (result f64) local.get 0 f64.nearest))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		arg  float64
		want float64
	}{
		{name: "abs", arg: -2.25, want: 2.25},
		{name: "neg", arg: 2.25, want: -2.25},
		{name: "sqrt", arg: 9, want: 3},
		{name: "ceil", arg: 2.25, want: 3},
		{name: "floor", arg: 2.75, want: 2},
		{name: "trunc", arg: -2.75, want: -2},
		{name: "nearest", arg: 2.5, want: 2},
		{name: "nearest", arg: 3.5, want: 4},
	} {
		results := callExport(t, inst, tt.name, wasmvm.F64(tt.arg))
		if len(results) != 1 || results[0] != wasmvm.F64(tt.want) {
			t.Fatalf("%s(%v) got results %#v, want f64 %v", tt.name, tt.arg, results, tt.want)
		}
	}
}

// TestFloatBinaryExtraOps checks min, max, and copysign for f32 and f64.
func TestFloatBinaryExtraOps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "f32_min") (param f32 f32) (result f32) local.get 0 local.get 1 f32.min)
			(func (export "f32_max") (param f32 f32) (result f32) local.get 0 local.get 1 f32.max)
			(func (export "f32_copysign") (param f32 f32) (result f32) local.get 0 local.get 1 f32.copysign)
			(func (export "f64_min") (param f64 f64) (result f64) local.get 0 local.get 1 f64.min)
			(func (export "f64_max") (param f64 f64) (result f64) local.get 0 local.get 1 f64.max)
			(func (export "f64_copysign") (param f64 f64) (result f64) local.get 0 local.get 1 f64.copysign))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		lhs  wasmvm.Value
		rhs  wasmvm.Value
		want wasmvm.Value
	}{
		{name: "f32_min", lhs: wasmvm.F32(3.5), rhs: wasmvm.F32(-1.25), want: wasmvm.F32(-1.25)},
		{name: "f32_max", lhs: wasmvm.F32(3.5), rhs: wasmvm.F32(-1.25), want: wasmvm.F32(3.5)},
		{name: "f32_copysign", lhs: wasmvm.F32(3.5), rhs: wasmvm.F32(float32(math.Copysign(0, -1))), want: wasmvm.F32(-3.5)},
		{name: "f64_min", lhs: wasmvm.F64(3.5), rhs: wasmvm.F64(-1.25), want: wasmvm.F64(-1.25)},
		{name: "f64_max", lhs: wasmvm.F64(3.5), rhs: wasmvm.F64(-1.25), want: wasmvm.F64(3.5)},
		{name: "f64_copysign", lhs: wasmvm.F64(3.5), rhs: wasmvm.F64(math.Copysign(0, -1)), want: wasmvm.F64(-3.5)},
	} {
		results := callExport(t, inst, tt.name, tt.lhs, tt.rhs)
		if len(results) != 1 || results[0] != tt.want {
			t.Fatalf("%s got results %#v, want %v", tt.name, results, tt.want)
		}
	}
}

// TestNumericConversionsAndReinterpret checks non-trapping numeric conversions
// and bit-preserving reinterpret instructions.
func TestNumericConversionsAndReinterpret(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "i32_wrap") (param i64) (result i32) local.get 0 i32.wrap_i64)
			(func (export "i64_ext_s") (param i32) (result i64) local.get 0 i64.extend_i32_s)
			(func (export "i64_ext_u") (param i32) (result i64) local.get 0 i64.extend_i32_u)
			(func (export "f32_i32_s") (param i32) (result f32) local.get 0 f32.convert_i32_s)
			(func (export "f32_i32_u") (param i32) (result f32) local.get 0 f32.convert_i32_u)
			(func (export "f32_i64_s") (param i64) (result f32) local.get 0 f32.convert_i64_s)
			(func (export "f32_i64_u") (param i64) (result f32) local.get 0 f32.convert_i64_u)
			(func (export "f32_demote") (param f64) (result f32) local.get 0 f32.demote_f64)
			(func (export "f64_i32_s") (param i32) (result f64) local.get 0 f64.convert_i32_s)
			(func (export "f64_i32_u") (param i32) (result f64) local.get 0 f64.convert_i32_u)
			(func (export "f64_i64_s") (param i64) (result f64) local.get 0 f64.convert_i64_s)
			(func (export "f64_i64_u") (param i64) (result f64) local.get 0 f64.convert_i64_u)
			(func (export "f64_promote") (param f32) (result f64) local.get 0 f64.promote_f32)
			(func (export "i32_re_f32") (param f32) (result i32) local.get 0 i32.reinterpret_f32)
			(func (export "i64_re_f64") (param f64) (result i64) local.get 0 i64.reinterpret_f64)
			(func (export "f32_re_i32") (param i32) (result f32) local.get 0 f32.reinterpret_i32)
			(func (export "f64_re_i64") (param i64) (result f64) local.get 0 f64.reinterpret_i64))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		arg  wasmvm.Value
		want wasmvm.Value
	}{
		{name: "i32_wrap", arg: wasmvm.I64(0x100000002), want: wasmvm.I32(2)},
		{name: "i64_ext_s", arg: wasmvm.I32(-1), want: wasmvm.I64(-1)},
		{name: "i64_ext_u", arg: wasmvm.I32(-1), want: wasmvm.I64(4294967295)},
		{name: "f32_i32_s", arg: wasmvm.I32(-7), want: wasmvm.F32(-7)},
		{name: "f32_i32_u", arg: wasmvm.I32(-1), want: wasmvm.F32(float32(^uint32(0)))},
		{name: "f32_i64_s", arg: wasmvm.I64(-9), want: wasmvm.F32(-9)},
		{name: "f32_i64_u", arg: wasmvm.I64(9), want: wasmvm.F32(9)},
		{name: "f32_demote", arg: wasmvm.F64(12.5), want: wasmvm.F32(12.5)},
		{name: "f64_i32_s", arg: wasmvm.I32(-11), want: wasmvm.F64(-11)},
		{name: "f64_i32_u", arg: wasmvm.I32(-1), want: wasmvm.F64(float64(^uint32(0)))},
		{name: "f64_i64_s", arg: wasmvm.I64(-13), want: wasmvm.F64(-13)},
		{name: "f64_i64_u", arg: wasmvm.I64(13), want: wasmvm.F64(13)},
		{name: "f64_promote", arg: wasmvm.F32(6.25), want: wasmvm.F64(6.25)},
		{name: "i32_re_f32", arg: wasmvm.F32(1), want: wasmvm.I32(0x3f800000)},
		{name: "i64_re_f64", arg: wasmvm.F64(1), want: wasmvm.I64(0x3ff0000000000000)},
		{name: "f32_re_i32", arg: wasmvm.I32(0x3f800000), want: wasmvm.F32(1)},
		{name: "f64_re_i64", arg: wasmvm.I64(0x3ff0000000000000), want: wasmvm.F64(1)},
	} {
		results := callExport(t, inst, tt.name, tt.arg)
		if len(results) != 1 || results[0] != tt.want {
			t.Fatalf("%s got results %#v, want %v", tt.name, results, tt.want)
		}
	}
}

// TestFloatToIntegerTruncation checks trapping and saturating float-to-integer
// conversions for representative f32 and f64 inputs.
func TestFloatToIntegerTruncation(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "i32_f32_s") (param f32) (result i32) local.get 0 i32.trunc_f32_s)
			(func (export "i32_f32_u") (param f32) (result i32) local.get 0 i32.trunc_f32_u)
			(func (export "i32_f64_s") (param f64) (result i32) local.get 0 i32.trunc_f64_s)
			(func (export "i32_f64_u") (param f64) (result i32) local.get 0 i32.trunc_f64_u)
			(func (export "i64_f32_s") (param f32) (result i64) local.get 0 i64.trunc_f32_s)
			(func (export "i64_f32_u") (param f32) (result i64) local.get 0 i64.trunc_f32_u)
			(func (export "i64_f64_s") (param f64) (result i64) local.get 0 i64.trunc_f64_s)
			(func (export "i64_f64_u") (param f64) (result i64) local.get 0 i64.trunc_f64_u)
			(func (export "i32_sat_f32_s") (param f32) (result i32) local.get 0 i32.trunc_sat_f32_s)
			(func (export "i32_sat_f32_u") (param f32) (result i32) local.get 0 i32.trunc_sat_f32_u)
			(func (export "i32_sat_f64_s") (param f64) (result i32) local.get 0 i32.trunc_sat_f64_s)
			(func (export "i32_sat_f64_u") (param f64) (result i32) local.get 0 i32.trunc_sat_f64_u)
			(func (export "i64_sat_f32_s") (param f32) (result i64) local.get 0 i64.trunc_sat_f32_s)
			(func (export "i64_sat_f32_u") (param f32) (result i64) local.get 0 i64.trunc_sat_f32_u)
			(func (export "i64_sat_f64_s") (param f64) (result i64) local.get 0 i64.trunc_sat_f64_s)
			(func (export "i64_sat_f64_u") (param f64) (result i64) local.get 0 i64.trunc_sat_f64_u))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		arg  wasmvm.Value
		want wasmvm.Value
	}{
		{name: "i32_f32_s", arg: wasmvm.F32(-2.9), want: wasmvm.I32(-2)},
		{name: "i32_f32_u", arg: wasmvm.F32(3.9), want: wasmvm.I32(3)},
		{name: "i32_f64_s", arg: wasmvm.F64(-2.9), want: wasmvm.I32(-2)},
		{name: "i32_f64_u", arg: wasmvm.F64(3.9), want: wasmvm.I32(3)},
		{name: "i64_f32_s", arg: wasmvm.F32(-2.9), want: wasmvm.I64(-2)},
		{name: "i64_f32_u", arg: wasmvm.F32(3.9), want: wasmvm.I64(3)},
		{name: "i64_f64_s", arg: wasmvm.F64(-2.9), want: wasmvm.I64(-2)},
		{name: "i64_f64_u", arg: wasmvm.F64(3.9), want: wasmvm.I64(3)},
		{name: "i32_sat_f32_s", arg: wasmvm.F32(float32(math.Inf(1))), want: wasmvm.I32(1<<31 - 1)},
		{name: "i32_sat_f32_u", arg: wasmvm.F32(-1), want: wasmvm.I32(0)},
		{name: "i32_sat_f64_s", arg: wasmvm.F64(math.Inf(-1)), want: wasmvm.I32(-1 << 31)},
		{name: "i32_sat_f64_u", arg: wasmvm.F64(math.Inf(1)), want: wasmvm.I32(-1)},
		{name: "i64_sat_f32_s", arg: wasmvm.F32(float32(math.Inf(1))), want: wasmvm.I64(1<<63 - 1)},
		{name: "i64_sat_f32_u", arg: wasmvm.F32(float32(math.Inf(1))), want: wasmvm.I64(-1)},
		{name: "i64_sat_f64_s", arg: wasmvm.F64(math.Inf(-1)), want: wasmvm.I64(-1 << 63)},
		{name: "i64_sat_f64_u", arg: wasmvm.F64(math.NaN()), want: wasmvm.I64(0)},
	} {
		results := callExport(t, inst, tt.name, tt.arg)
		if len(results) != 1 || results[0] != tt.want {
			t.Fatalf("%s got results %#v, want %v", tt.name, results, tt.want)
		}
	}

	for _, tt := range []struct {
		name string
		arg  wasmvm.Value
		want string
	}{
		{name: "i32_f64_s", arg: wasmvm.F64(2147483648), want: "pc 1 i32.trunc_f64_s: integer overflow"},
		{name: "i32_f64_u", arg: wasmvm.F64(-1), want: "pc 1 i32.trunc_f64_u: integer overflow"},
		{name: "i64_f64_s", arg: wasmvm.F64(math.Inf(1)), want: "pc 1 i64.trunc_f64_s: integer overflow"},
		{name: "i32_f64_s", arg: wasmvm.F64(math.NaN()), want: "pc 1 i32.trunc_f64_s: invalid conversion to integer"},
	} {
		f, ok := inst.ExportedFunc(tt.name)
		if !ok {
			t.Fatalf("missing %s export", tt.name)
		}
		_, err := f.Call(tt.arg)
		if err == nil {
			t.Fatalf("Call %s succeeded unexpectedly", tt.name)
		}
		if got := err.Error(); got != tt.want {
			t.Fatalf("%s error = %q, want %q", tt.name, got, tt.want)
		}
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

	results := callExport(t, inst, "early", wasmvm.I32(0))
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("early(0) got results %#v, want i32 42", results)
	}
	results = callExport(t, inst, "early", wasmvm.I32(9))
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

	results := callExport(t, inst, "abs", wasmvm.I32(-7))
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("abs(-7) got results %#v, want i32 7", results)
	}
	results = callExport(t, inst, "abs", wasmvm.I32(5))
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

	results := callExport(t, inst, "skip")
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

	results := callExport(t, inst, "clamp_zero", wasmvm.I32(12))
	if len(results) != 1 || results[0] != wasmvm.I32(12) {
		t.Fatalf("clamp_zero(12) got results %#v, want i32 12", results)
	}
	results = callExport(t, inst, "clamp_zero", wasmvm.I32(-3))
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("clamp_zero(-3) got results %#v, want i32 0", results)
	}
}

// TestBlockBranchTable checks that br_table selects table targets by an i32
// selector and falls back to the default target for out-of-range selectors.
func TestBlockBranchTable(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "choose") (param i32) (result i32)
				block $default
					block $one
						block $zero
							local.get 0
							br_table $zero $one $default
						end
						i32.const 0
						return
					end
					i32.const 1
					return
				end
				i32.const 9))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	tests := []struct {
		arg  wasmvm.Value
		want wasmvm.Value
	}{
		{wasmvm.I32(0), wasmvm.I32(0)},
		{wasmvm.I32(1), wasmvm.I32(1)},
		{wasmvm.I32(2), wasmvm.I32(9)},
		{wasmvm.I32(-1), wasmvm.I32(9)},
	}
	for _, tt := range tests {
		results := callExport(t, inst, "choose", tt.arg)
		if len(results) != 1 || results[0] != tt.want {
			t.Fatalf("choose(%v) got results %#v, want %v", tt.arg, results, tt.want)
		}
	}
}

// TestLoopBranch checks that br to a loop label jumps back to the loop body,
// while br_if to an outer block exits the loop.
func TestLoopBranch(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "sum") (param $n i32) (result i32)
				(local $acc i32)
				block $exit
					loop $again
						local.get $n
						i32.eqz
						br_if $exit
						local.get $acc
						local.get $n
						i32.add
						local.set $acc
						local.get $n
						i32.const 1
						i32.sub
						local.set $n
						br $again
					end
				end
				local.get $acc))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	tests := []struct {
		arg  wasmvm.Value
		want wasmvm.Value
	}{
		{wasmvm.I32(0), wasmvm.I32(0)},
		{wasmvm.I32(1), wasmvm.I32(1)},
		{wasmvm.I32(5), wasmvm.I32(15)},
	}
	for _, tt := range tests {
		results := callExport(t, inst, "sum", tt.arg)
		if len(results) != 1 || results[0] != tt.want {
			t.Fatalf("sum(%v) got results %#v, want %v", tt.arg, results, tt.want)
		}
	}
}

func TestModuleGlobals(t *testing.T) {
	// Module-defined globals are instantiated once and then accessed through
	// global.get/global.set while functions execute.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(global $g (mut i32) (i32.const 7))
			(global $h i64 (i64.const 11))
			(func (export "get_g") (result i32)
				global.get $g)
			(func (export "set_g") (param i32)
				local.get 0
				global.set $g)
			(func (export "get_h") (result i64)
				global.get $h))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "get_g")
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("get_g got results %#v, want i32 7", results)
	}

	callExport(t, inst, "set_g", wasmvm.I32(42))
	results = callExport(t, inst, "get_g")
	if len(results) != 1 || results[0] != wasmvm.I32(42) {
		t.Fatalf("get_g after set got results %#v, want i32 42", results)
	}

	results = callExport(t, inst, "get_h")
	if len(results) != 1 || results[0] != wasmvm.I64(11) {
		t.Fatalf("get_h got results %#v, want i64 11", results)
	}
}

func TestGlobalInitializerReadsEarlierImmutableGlobal(t *testing.T) {
	// Global initializer expressions can read earlier immutable globals and use
	// the numeric constant-expression operators currently supported by wasmvm.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(global $base i32 (i32.const 5))
			(global $sum i32
				global.get $base
				i32.const 6
				i32.add)
			(global $scale i64
				i64.const 3
				i64.const 4
				i64.mul)
			(func (export "sum") (result i32)
				global.get $sum)
			(func (export "scale") (result i64)
				global.get $scale))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "sum")
	if len(results) != 1 || results[0] != wasmvm.I32(11) {
		t.Fatalf("sum got results %#v, want i32 11", results)
	}

	results = callExport(t, inst, "scale")
	if len(results) != 1 || results[0] != wasmvm.I64(12) {
		t.Fatalf("scale got results %#v, want i64 12", results)
	}
}

func TestMemoryI32LoadStore(t *testing.T) {
	// Module-defined memories are instantiated as zeroed bytes and accessed
	// through i32.load/i32.store with the static offset immediate applied.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(func (export "roundtrip") (param i32 i32) (result i32)
				local.get 0
				local.get 1
				i32.store offset=4
				local.get 0
				i32.load offset=4)
			(func (export "zero") (result i32)
				i32.const 32
				i32.load))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "zero")
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("zero got results %#v, want i32 0", results)
	}

	results = callExport(t, inst, "roundtrip", wasmvm.I32(12), wasmvm.I32(0x12345678))
	if len(results) != 1 || results[0] != wasmvm.I32(0x12345678) {
		t.Fatalf("roundtrip got results %#v, want i32 0x12345678", results)
	}
}

func TestActiveDataSegments(t *testing.T) {
	// Active data segments are copied into memory during instantiation, and
	// offset expressions may read immutable globals.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(global $off i32 (i32.const 16))
			(data (i32.const 4) "ABCD")
			(data (global.get $off) "WXYZ")
			(func (export "load0") (result i32)
				i32.const 4
				i32.load)
			(func (export "load1") (result i32)
				i32.const 16
				i32.load))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "load0")
	if len(results) != 1 || results[0] != wasmvm.I32(0x44434241) {
		t.Fatalf("load0 got results %#v, want i32 0x44434241", results)
	}

	results = callExport(t, inst, "load1")
	if len(results) != 1 || results[0] != wasmvm.I32(0x5a595857) {
		t.Fatalf("load1 got results %#v, want i32 0x5a595857", results)
	}
}

func TestI32NarrowMemoryOps(t *testing.T) {
	// Narrow i32 loads extend to i32, and narrow stores truncate the low-order
	// bytes of the stored value.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data (i32.const 0) "\ff\80\34\12")
			(func (export "load8_s") (result i32)
				i32.const 0
				i32.load8_s)
			(func (export "load8_u") (result i32)
				i32.const 0
				i32.load8_u)
			(func (export "load16_s") (result i32)
				i32.const 0
				i32.load16_s)
			(func (export "load16_u") (result i32)
				i32.const 2
				i32.load16_u)
			(func (export "store8") (result i32)
				i32.const 8
				i32.const 0x12345678
				i32.store8
				i32.const 8
				i32.load8_u)
			(func (export "store16") (result i32)
				i32.const 10
				i32.const 0x12345678
				i32.store16
				i32.const 10
				i32.load16_u))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		want int32
	}{
		{name: "load8_s", want: -1},
		{name: "load8_u", want: 255},
		{name: "load16_s", want: -32513},
		{name: "load16_u", want: 0x1234},
		{name: "store8", want: 0x78},
		{name: "store16", want: 0x5678},
	} {
		results := callExport(t, inst, tt.name)
		if len(results) != 1 || results[0] != wasmvm.I32(tt.want) {
			t.Fatalf("%s got results %#v, want i32 %d", tt.name, results, tt.want)
		}
	}
}

func TestScalarMemoryOps(t *testing.T) {
	// The remaining scalar numeric load/store instructions share the same
	// memory resolver path with their own value encodings.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data (i32.const 0) "\01\02\03\04\05\06\07\08")
			(func (export "load_i64") (result i64)
				i32.const 0
				i64.load)
			(func (export "roundtrip_i64") (param i64) (result i64)
				i32.const 16
				local.get 0
				i64.store
				i32.const 16
				i64.load)
			(func (export "roundtrip_f32") (param f32) (result f32)
				i32.const 32
				local.get 0
				f32.store
				i32.const 32
				f32.load)
			(func (export "roundtrip_f64") (param f64) (result f64)
				i32.const 48
				local.get 0
				f64.store
				i32.const 48
				f64.load))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "load_i64")
	if len(results) != 1 || results[0] != wasmvm.I64(0x0807060504030201) {
		t.Fatalf("load_i64 got results %#v, want i64 0x0807060504030201", results)
	}

	results = callExport(t, inst, "roundtrip_i64", wasmvm.I64(-1234567890123))
	if len(results) != 1 || results[0] != wasmvm.I64(-1234567890123) {
		t.Fatalf("roundtrip_i64 got results %#v, want i64 -1234567890123", results)
	}

	results = callExport(t, inst, "roundtrip_f32", wasmvm.F32(12.5))
	if len(results) != 1 || results[0] != wasmvm.F32(12.5) {
		t.Fatalf("roundtrip_f32 got results %#v, want f32 12.5", results)
	}

	results = callExport(t, inst, "roundtrip_f64", wasmvm.F64(-9.25))
	if len(results) != 1 || results[0] != wasmvm.F64(-9.25) {
		t.Fatalf("roundtrip_f64 got results %#v, want f64 -9.25", results)
	}
}

func TestI64NarrowMemoryOps(t *testing.T) {
	// Narrow i64 loads extend to i64, and narrow i64 stores truncate the
	// low-order bytes of the stored value.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data (i32.const 0) "\ff\80\34\12\ff\ff\ff\80")
			(func (export "load8_s") (result i64)
				i32.const 0
				i64.load8_s)
			(func (export "load8_u") (result i64)
				i32.const 0
				i64.load8_u)
			(func (export "load16_s") (result i64)
				i32.const 0
				i64.load16_s)
			(func (export "load16_u") (result i64)
				i32.const 2
				i64.load16_u)
			(func (export "load32_s") (result i64)
				i32.const 4
				i64.load32_s)
			(func (export "load32_u") (result i64)
				i32.const 4
				i64.load32_u)
			(func (export "store8") (result i64)
				i32.const 16
				i64.const 0x123456789abcdef0
				i64.store8
				i32.const 16
				i64.load8_u)
			(func (export "store16") (result i64)
				i32.const 18
				i64.const 0x123456789abcdef0
				i64.store16
				i32.const 18
				i64.load16_u)
			(func (export "store32") (result i64)
				i32.const 20
				i64.const 0x123456789abcdef0
				i64.store32
				i32.const 20
				i64.load32_u))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	for _, tt := range []struct {
		name string
		want int64
	}{
		{name: "load8_s", want: -1},
		{name: "load8_u", want: 255},
		{name: "load16_s", want: -32513},
		{name: "load16_u", want: 0x1234},
		{name: "load32_s", want: -2130706433},
		{name: "load32_u", want: 0x80ffffff},
		{name: "store8", want: 0xf0},
		{name: "store16", want: 0xdef0},
		{name: "store32", want: 0x9abcdef0},
	} {
		results := callExport(t, inst, tt.name)
		if len(results) != 1 || results[0] != wasmvm.I64(tt.want) {
			t.Fatalf("%s got results %#v, want i64 %d", tt.name, results, tt.want)
		}
	}
}

func TestMemorySizeAndGrow(t *testing.T) {
	// memory.grow returns the old size on success, -1 on failure, and newly
	// allocated pages are zero-initialized.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1 3)
			(func (export "size") (result i32)
				memory.size)
			(func (export "grow") (param i32) (result i32)
				local.get 0
				memory.grow)
			(func (export "load_grown_page") (result i32)
				i32.const 70000
				i32.load)
			(func (export "store_grown_page") (result i32)
				i32.const 70000
				i32.const 99
				i32.store
				i32.const 70000
				i32.load))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "size")
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("initial size got results %#v, want i32 1", results)
	}

	results = callExport(t, inst, "grow", wasmvm.I32(1))
	if len(results) != 1 || results[0] != wasmvm.I32(1) {
		t.Fatalf("grow(1) got results %#v, want old size i32 1", results)
	}

	results = callExport(t, inst, "size")
	if len(results) != 1 || results[0] != wasmvm.I32(2) {
		t.Fatalf("size after grow got results %#v, want i32 2", results)
	}

	results = callExport(t, inst, "load_grown_page")
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("load_grown_page got results %#v, want zero-filled i32 0", results)
	}

	results = callExport(t, inst, "store_grown_page")
	if len(results) != 1 || results[0] != wasmvm.I32(99) {
		t.Fatalf("store_grown_page got results %#v, want i32 99", results)
	}

	results = callExport(t, inst, "grow", wasmvm.I32(2))
	if len(results) != 1 || results[0] != wasmvm.I32(-1) {
		t.Fatalf("grow past max got results %#v, want i32 -1", results)
	}

	results = callExport(t, inst, "size")
	if len(results) != 1 || results[0] != wasmvm.I32(2) {
		t.Fatalf("size after failed grow got results %#v, want i32 2", results)
	}
}

// TestMemoryCopyAndFill checks the bulk memory instructions that move or
// initialize byte ranges inside an instantiated memory.
func TestMemoryCopyAndFill(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data (i32.const 0) "abcdef")
			(func (export "copy_overlap") (result i32)
				i32.const 2
				i32.const 0
				i32.const 4
				memory.copy
				i32.const 0
				i32.load)
			(func (export "fill") (result i32)
				i32.const 8
				i32.const 127
				i32.const 4
				memory.fill
				i32.const 8
				i32.load))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "copy_overlap")
	if len(results) != 1 || results[0] != wasmvm.I32(0x62616261) {
		t.Fatalf("copy_overlap got results %#v, want i32 0x62616261", results)
	}

	results = callExport(t, inst, "fill")
	if len(results) != 1 || results[0] != wasmvm.I32(0x7f7f7f7f) {
		t.Fatalf("fill got results %#v, want i32 0x7f7f7f7f", results)
	}
}

// TestPassiveDataMemoryInit checks that memory.init copies from a passive data
// segment into memory and honors the source offset operand.
func TestPassiveDataMemoryInit(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data "abcdef")
			(func (export "init") (result i32)
				i32.const 8
				i32.const 1
				i32.const 4
				memory.init 0
				i32.const 8
				i32.load))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	results := callExport(t, inst, "init")
	if len(results) != 1 || results[0] != wasmvm.I32(0x65646362) {
		t.Fatalf("init got results %#v, want i32 0x65646362", results)
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

// TestExecutionErrorUnreachableContext checks that unreachable traps include
// the failing instruction location.
func TestExecutionErrorUnreachableContext(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func (export "trap")
				unreachable))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	trap, ok := inst.ExportedFunc("trap")
	if !ok {
		t.Fatal("missing trap export")
	}
	_, err = trap.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 0 unreachable: unreachable executed"; got != want {
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

// TestExecutionErrorReturnCallContext checks that return_call reports the
// instruction location when direct call resolution fails.
func TestExecutionErrorReturnCallContext(t *testing.T) {
	err := callInvalidRuntimeModule(t, []wasmir.Instruction{
		{Kind: wasmir.InstrReturnCall, FuncIndex: 3},
		{Kind: wasmir.InstrEnd},
	}, nil)

	if got, want := err.Error(), "pc 0 return_call: call function index 3 out of range"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestExecutionErrorTableOutOfBoundsContext checks that table access traps
// include the failing instruction location.
func TestExecutionErrorTableOutOfBoundsContext(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table 1 funcref)
			(func (export "get_oob")
				i32.const 1
				table.get
				drop))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("get_oob")
	if !ok {
		t.Fatal("missing get_oob export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 1 table.get: table access out of bounds"; got != want {
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

func TestExecutionErrorSelectTypeContext(t *testing.T) {
	// A select with mismatched candidate value types should report the select
	// instruction as the failing execution point.
	err := callInvalidRuntimeModule(t, []wasmir.Instruction{
		{Kind: wasmir.InstrI32Const, I32Const: 10},
		{Kind: wasmir.InstrI64Const, I64Const: 20},
		{Kind: wasmir.InstrI32Const, I32Const: 1},
		{Kind: wasmir.InstrSelect},
		{Kind: wasmir.InstrEnd},
	}, []wasmir.ValueType{wasmir.ValueTypeI32})

	if got, want := err.Error(), "pc 3 select: select got i32 and i64 operands"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestExecutionErrorGlobalSetImmutableContext(t *testing.T) {
	// Setting an immutable global should report the global.set instruction as
	// the failing execution point.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(&wasmir.Module{
		Types: []wasmir.TypeDef{{
			Kind: wasmir.TypeDefKindFunc,
		}},
		Globals: []wasmir.Global{{
			Type: wasmir.ValueTypeI32,
			Init: []wasmir.Instruction{{Kind: wasmir.InstrI32Const, I32Const: 0}},
		}},
		Funcs: []wasmir.Function{{
			TypeIdx: 0,
			Body: []wasmir.Instruction{
				{Kind: wasmir.InstrI32Const, I32Const: 1},
				{Kind: wasmir.InstrGlobalSet, GlobalIndex: 0},
				{Kind: wasmir.InstrEnd},
			},
		}},
		Exports: []wasmir.Export{{
			Name:  "run",
			Kind:  wasmir.ExternalKindFunction,
			Index: 0,
		}},
	}, nil)
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
	if got, want := err.Error(), "pc 1 global.set: global 0 is immutable"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestExecutionErrorMemoryOutOfBoundsContext(t *testing.T) {
	// An out-of-bounds memory store should report the store instruction as the
	// failing execution point.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(func (export "store_oob")
				i32.const 65533
				i32.const 1
				i32.store))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("store_oob")
	if !ok {
		t.Fatal("missing store_oob export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 2 i32.store: memory access out of bounds"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestInstantiateRejectsOutOfBoundsDataSegment(t *testing.T) {
	// Active data segments are bounds-checked while the instance memory is
	// initialized.
	rt := wasmvm.NewRuntime()
	_, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data (i32.const 65534) "ABCD"))
	`), nil)
	if err == nil {
		t.Fatal("Instantiate succeeded unexpectedly")
	}
	if got, want := err.Error(), "data[0]: memory access out of bounds"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestExecutionErrorMemoryFillOutOfBoundsContext checks that memory.fill traps
// include the failing instruction location.
func TestExecutionErrorMemoryFillOutOfBoundsContext(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(func (export "fill_oob")
				i32.const 65535
				i32.const 1
				i32.const 2
				memory.fill))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("fill_oob")
	if !ok {
		t.Fatal("missing fill_oob export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 3 memory.fill: memory access out of bounds"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestExecutionErrorMemoryInitAfterDataDropContext checks that data.drop makes
// a data segment unavailable for later memory.init operations.
func TestExecutionErrorMemoryInitAfterDataDropContext(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data "abc")
			(func (export "drop_then_init")
				data.drop 0
				i32.const 0
				i32.const 0
				i32.const 1
				memory.init 0))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("drop_then_init")
	if !ok {
		t.Fatal("missing drop_then_init export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 4 memory.init: data segment 0 is dropped"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

// TestExecutionErrorMemoryInitSourceOutOfBoundsContext checks that memory.init
// reports data segment source-range failures with instruction context.
func TestExecutionErrorMemoryInitSourceOutOfBoundsContext(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(data "abc")
			(func (export "init_oob")
				i32.const 0
				i32.const 2
				i32.const 2
				memory.init 0))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("init_oob")
	if !ok {
		t.Fatal("missing init_oob export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call succeeded unexpectedly")
	}
	if got, want := err.Error(), "pc 3 memory.init: data segment access out of bounds"; got != want {
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

// callInvalidCallRefRuntimeModule runs a deliberately unvalidated module whose
// call_ref operand is a function reference with the wrong runtime signature.
func callInvalidCallRefRuntimeModule(t *testing.T) error {
	t.Helper()

	rt := wasmvm.NewRuntime()
	i32 := wasmir.ValueTypeI32
	m := &wasmir.Module{
		Types: []wasmir.TypeDef{
			{Kind: wasmir.TypeDefKindFunc, Params: []wasmir.ValueType{i32, i32}, Results: []wasmir.ValueType{i32}},
			{Kind: wasmir.TypeDefKindFunc, Params: []wasmir.ValueType{i32}, Results: []wasmir.ValueType{i32}},
			{Kind: wasmir.TypeDefKindFunc, Results: []wasmir.ValueType{i32}},
		},
		Funcs: []wasmir.Function{
			{
				TypeIdx: 2,
				Body: []wasmir.Instruction{
					{Kind: wasmir.InstrI32Const, I32Const: 1},
					{Kind: wasmir.InstrI32Const, I32Const: 2},
					{Kind: wasmir.InstrRefFunc, FuncIndex: 1},
					{Kind: wasmir.InstrCallRef, CallTypeIndex: 0},
					{Kind: wasmir.InstrEnd},
				},
			},
			{
				TypeIdx: 1,
				Body: []wasmir.Instruction{
					{Kind: wasmir.InstrLocalGet, LocalIndex: 0},
					{Kind: wasmir.InstrI32Const, I32Const: 1},
					{Kind: wasmir.InstrI32Add},
					{Kind: wasmir.InstrEnd},
				},
			},
		},
		Exports: []wasmir.Export{{
			Name:  "run",
			Kind:  wasmir.ExternalKindFunction,
			Index: 0,
		}},
	}
	inst, err := rt.Instantiate(m, nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	run, ok := inst.ExportedFunc("run")
	if !ok {
		t.Fatal("missing run export")
	}
	_, err = run.Call()
	if err == nil {
		t.Fatal("Call with mismatched call_ref target succeeded unexpectedly")
	}
	return err
}
