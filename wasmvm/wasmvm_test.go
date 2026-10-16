package wasmvm_test

import (
	"fmt"
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

// expectValueResult calls an exported function and checks for one result value.
func expectValueResult(t *testing.T, inst *wasmvm.ModuleInstance, name string, want wasmvm.Value, args ...wasmvm.Value) {
	t.Helper()

	results := callExport(t, inst, name, args...)
	if len(results) != 1 || results[0] != want {
		t.Fatalf("%s got results %#v, want %#v", name, results, want)
	}
}

// expectI32Result calls an exported function and checks for one i32 result.
func expectI32Result(t *testing.T, inst *wasmvm.ModuleInstance, name string, want int32, args ...wasmvm.Value) {
	t.Helper()

	expectValueResult(t, inst, name, wasmvm.I32(want), args...)
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

	expectI32Result(t, inst, "add", 7, wasmvm.I32(3), wasmvm.I32(4))
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

	expectI32Result(t, inst, "run", 42)
}

func TestEmptyImportNames(t *testing.T) {
	rt := wasmvm.NewRuntime()
	provider, err := rt.Instantiate(parseWAT(t, `
(module
  (func $f (export "f") (result i32) i32.const 42)
  (table (export "t") 1 funcref)
  (elem (i32.const 0) $f)
  (memory (export "m") 1)
  (data (i32.const 0) "\2a")
  (global (export "g") i32 (i32.const 42))
  (tag $e (export "e") (param i32))
  (func (export "throw") (throw $e (i32.const 42)))
)`), nil)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := provider.ExportedFunc("f")
	table, _ := provider.ExportedTable("t")
	memory, _ := provider.ExportedMemory("m")
	global, _ := provider.ExportedGlobal("g")
	tag, _ := provider.ExportedTag("e")
	throw, _ := provider.ExportedFunc("throw")
	for _, names := range []struct{ module, field string }{
		{"", ""}, {"", "field"}, {"module", ""},
	} {
		for _, tc := range []struct {
			kind, typ, body string
			extern          wasmvm.Extern
		}{
			{"func", "(result i32)", "call $imp", f},
			{"table", "1 funcref", "i32.const 0 call_indirect (type $sig)", table},
			{"memory", "1", "i32.const 0 i32.load8_u", memory},
			{"global", "i32", "global.get $imp", global},
			{"tag", "(param i32)", `(block (result i32)
				(try_table (catch $imp 0) (call $throw))
				(i32.const 0))`, tag},
		} {
			t.Run(fmt.Sprintf("%s/%q/%q", tc.kind, names.module, names.field), func(t *testing.T) {
				src := fmt.Sprintf(`(module
					(type $sig (func (result i32)))
					(%s $imp (import %q %q) %s)
					(import "provider" "throw" (func $throw))
					(func (export "run") (result i32) %s))`,
					tc.kind, names.module, names.field, tc.typ, tc.body)
				encoded, err := watgo.CompileWATToWASM([]byte(src))
				if err != nil {
					t.Fatal(err)
				}
				m, err := watgo.DecodeWASM(encoded)
				if err != nil {
					t.Fatal(err)
				}
				imports := wasmvm.Imports{"provider": {"throw": throw}}
				if _, err := rt.Instantiate(m, imports); err == nil {
					t.Fatal("missing empty-name import was treated as a definition")
				}
				imports[names.module] = map[string]wasmvm.Extern{names.field: tc.extern}
				inst, err := rt.Instantiate(m, imports)
				if err != nil {
					t.Fatal(err)
				}
				expectI32Result(t, inst, "run", 42)
			})
		}
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

	expectI32Result(t, inst, "call_inc", 42, wasmvm.I32(41))
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

	expectI32Result(t, inst, "tail_add", 42, wasmvm.I32(20), wasmvm.I32(22))
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

	expectI32Result(t, inst, "tail_double", 42, wasmvm.I32(21))
}

// TestReturnCallDoesNotGrowCallDepth checks that recursive return_call reuses
// the current VM frame instead of consuming one call-depth slot per iteration.
func TestReturnCallDoesNotGrowCallDepth(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(func $count (param i64) (result i64)
				local.get 0
				i64.eqz
				if (result i64)
					local.get 0
				else
					local.get 0
					i64.const 1
					i64.sub
					return_call $count
				end)
			(func (export "count") (param i64) (result i64)
				local.get 0
				return_call $count))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	expectValueResult(t, inst, "count", wasmvm.I64(0), wasmvm.I64(20000))
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

	expectI32Result(t, inst, "null_is_null", 1)
	expectI32Result(t, inst, "func_is_null", 0)
	expectI32Result(t, inst, "local_func_is_null", 0)

	results := callExport(t, inst, "return_null")
	if len(results) != 1 || !results[0].Type.IsRef() {
		t.Fatalf("return_null got results %#v, want one reference", results)
	}
	results = callExport(t, inst, "return_func")
	if len(results) != 1 || !results[0].Type.IsRef() || results[0].Ref.FuncIndex != 0 {
		t.Fatalf("return_func got results %#v, want function reference 0", results)
	}
}

// TestReferenceTableAndGlobalOps checks reference values moving through
// globals and tables.
func TestReferenceTableAndGlobalOps(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table $t_func 1 funcref)
			(table $t_extern 1 externref)
			(elem funcref (ref.func 2) (ref.null func))
			(elem externref (ref.null extern))
			(global $g (mut funcref) (ref.null func))

			(func (export "ref_null_func") (result funcref)
				ref.null func)
			(func (export "ref_null_extern") (result externref)
				ref.null extern)
			(func $ref_is_null_func (export "ref_is_null_func") (result i32)
				global.get $g
				ref.is_null)
			(func $ref_func (export "ref_func") (result funcref)
				ref.func $ref_is_null_func)
			(func $table_set (export "table_set")
				i32.const 0
				call $ref_func
				table.set $t_func)
			(func (export "table_get") (result i32)
				call $table_set
				i32.const 0
				table.get $t_func
				ref.is_null)
			(func $global_set (export "global_set") (result funcref)
				ref.func $ref_is_null_func
				global.set $g
				global.get $g))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	expectI32Result(t, inst, "ref_is_null_func", 1)
	results := callExport(t, inst, "ref_func")
	if len(results) != 1 || !results[0].Type.IsRef() || results[0].Ref.FuncIndex != 2 {
		t.Fatalf("ref_func got results %#v, want function reference 2", results)
	}
	callExport(t, inst, "table_set")
	expectI32Result(t, inst, "table_get", 0)
	results = callExport(t, inst, "global_set")
	if len(results) != 1 || !results[0].Type.IsRef() || results[0].Ref.FuncIndex != 2 {
		t.Fatalf("global_set got results %#v, want function reference 2", results)
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

	expectI32Result(t, inst, "size", 3)
	expectI32Result(t, inst, "is_null", 1, wasmvm.I32(0))
	expectI32Result(t, inst, "is_null", 0, wasmvm.I32(1))
	callExport(t, inst, "set_b", wasmvm.I32(2))
	expectI32Result(t, inst, "is_null", 0, wasmvm.I32(2))
}

// TestSharedTableImportExport checks the public table import/export API and
// cross-module function references in a shared table.
func TestSharedTableImportExport(t *testing.T) {
	rt := wasmvm.NewRuntime()

	owner, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $out-i32 (func (result i32)))
			(table (export "shared-table") 3 funcref)
			(func (export "call-1") (result i32)
				i32.const 1
				call_indirect (type $out-i32)))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate owner failed: %v", err)
	}
	sharedTable, ok := owner.ExportedTable("shared-table")
	if !ok {
		t.Fatal("missing shared-table export")
	}
	if got, want := sharedTable.Size(), uint64(3); got != want {
		t.Fatalf("shared table size got %d, want %d", got, want)
	}

	writer, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $out-i32 (func (result i32)))
			(import "owner" "shared-table" (table 3 funcref))
			(elem (i32.const 1) $answer)
			(func $answer (type $out-i32)
				i32.const 42))
	`), wasmvm.Imports{
		"owner": {
			"shared-table": sharedTable,
		},
	})
	if err != nil {
		t.Fatalf("Instantiate writer failed: %v", err)
	}
	_ = writer

	expectI32Result(t, owner, "call-1", 42)
}

// TestActiveElementSegmentsApplyBeforeDataSegments checks that active element
// segments mutate an imported table before a later active data segment traps
// during instantiation.
func TestActiveElementSegmentsApplyBeforeDataSegments(t *testing.T) {
	rt := wasmvm.NewRuntime()

	owner, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $out-i32 (func (result i32)))
			(table (export "tab") 10 funcref)
			(func $g (result i32)
				i32.const 4)
			(elem (i32.const 2) func $g $g $g $g)
			(func (export "call") (param i32) (result i32)
				local.get 0
				call_indirect (type $out-i32)))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate owner failed: %v", err)
	}
	table, ok := owner.ExportedTable("tab")
	if !ok {
		t.Fatal("missing tab export")
	}

	_, err = rt.Instantiate(parseWAT(t, `
		(module
			(import "owner" "tab" (table 10 funcref))
			(memory 1)
			(func $f (result i32)
				i32.const 0)
			(elem (i32.const 7) func $f)
			(data (i32.const 0x10000) "x"))
	`), wasmvm.Imports{
		"owner": {
			"tab": table,
		},
	})
	if err == nil {
		t.Fatal("Instantiate side-effecting module succeeded unexpectedly")
	}

	expectI32Result(t, owner, "call", 0, wasmvm.I32(7))
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

	expectI32Result(t, inst, "size", 2)
	expectI32Result(t, inst, "grow", 2, wasmvm.I32(2))
	expectI32Result(t, inst, "size", 4)
	expectI32Result(t, inst, "grow", -1, wasmvm.I32(2))

	callExport(t, inst, "fill_null", wasmvm.I32(2), wasmvm.I32(2))
	expectI32Result(t, inst, "is_null", 1, wasmvm.I32(2))
	callExport(t, inst, "fill_b", wasmvm.I32(0), wasmvm.I32(2))
	callExport(t, inst, "copy", wasmvm.I32(2), wasmvm.I32(0), wasmvm.I32(2))
	expectI32Result(t, inst, "is_null", 0, wasmvm.I32(3))
}

// TestTable64Ops checks that i64-indexed tables use i64 operands and results
// for indirect calls and table instructions.
func TestTable64Ops(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $out-i32 (func (result i32)))
			(table $t i64 3 6 funcref)
			(func $a (type $out-i32)
				i32.const 42)
			(func $b (type $out-i32)
				i32.const 7)
			(elem (i64.const 0) func $a)
			(elem $e funcref (ref.func $b))
			(func (export "size") (result i64)
				table.size $t)
			(func (export "call") (param i64) (result i32)
				local.get 0
				call_indirect $t (type $out-i32))
			(func (export "set_b") (param i64)
				local.get 0
				ref.func $b
				table.set $t)
			(func (export "grow") (result i64)
				ref.null func
				i64.const 3
				table.grow $t)
			(func (export "fill_b") (param i64 i64)
				local.get 0
				ref.func $b
				local.get 1
				table.fill $t)
			(func (export "init_b") (param i64)
				local.get 0
				i32.const 0
				i32.const 1
				table.init $t $e)
			(func (export "copy") (param i64 i64 i64)
				local.get 0
				local.get 1
				local.get 2
				table.copy $t $t))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	expectValueResult(t, inst, "size", wasmvm.I64(3))
	expectI32Result(t, inst, "call", 42, wasmvm.I64(0))
	callExport(t, inst, "set_b", wasmvm.I64(1))
	expectI32Result(t, inst, "call", 7, wasmvm.I64(1))
	expectValueResult(t, inst, "grow", wasmvm.I64(3))
	callExport(t, inst, "fill_b", wasmvm.I64(2), wasmvm.I64(2))
	expectI32Result(t, inst, "call", 7, wasmvm.I64(3))
	callExport(t, inst, "init_b", wasmvm.I64(4))
	expectI32Result(t, inst, "call", 7, wasmvm.I64(4))
	callExport(t, inst, "copy", wasmvm.I64(5), wasmvm.I64(0), wasmvm.I64(1))
	expectI32Result(t, inst, "call", 42, wasmvm.I64(5))
}

// TestTableGrowPastI32AddressSpaceFails checks that a grow which would make an
// i32-indexed table larger than the WebAssembly i32 address space fails without
// allocating the requested elements.
func TestTableGrowPastI32AddressSpaceFails(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table 16 funcref)
			(func $f)
			(elem declare func $f)
			(func (export "grow") (result i32)
				ref.func $f
				i32.const -16
				table.grow)
			(func (export "size") (result i32)
				table.size))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	expectI32Result(t, inst, "grow", -1)
	expectI32Result(t, inst, "size", 16)
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
	if got, want := err.Error(), "pc 3 table.init: element segment out of bounds"; got != want {
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

// TestIndirectCallTypeEquivalence checks that call_indirect accepts
// structurally equivalent function types, not only byte-for-byte identical
// indexed reference types.
func TestIndirectCallTypeEquivalence(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $s0 (func (param i32)))
			(type $s1 (func (param i32 (ref $s0))))
			(type $s2 (func (param i32 (ref $s0))))
			(type $t1 (func (param (ref $s1))))
			(type $t2 (func (param (ref $s2))))
			(func $s1 (type $s1))
			(func $s2 (type $s2))
			(func $f1 (type $t1))
			(func $f2 (type $t2))
			(table funcref (elem $f1 $f2))
			(elem declare func $s1 $s2)
			(func (export "run")
				ref.func $s1
				i32.const 1
				call_indirect (type $t1)
				ref.func $s2
				i32.const 0
				call_indirect (type $t2)))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	callExport(t, inst, "run")
}

// TestFunctionImportTypeEquivalence checks that importing an exported
// WebAssembly function compares indexed reference types structurally across the
// exporter and importer modules.
func TestFunctionImportTypeEquivalence(t *testing.T) {
	rt := wasmvm.NewRuntime()
	exporter, err := rt.Instantiate(parseWAT(t, `
		(module
			(type $s0 (func (param i32) (result f32)))
			(type $s1 (func (param i32 (ref $s0)) (result (ref $s0))))
			(type $t1 (func (param (ref $s1))))
			(func (export "f") (param (ref $t1))))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate exporter failed: %v", err)
	}
	f, ok := exporter.ExportedFunc("f")
	if !ok {
		t.Fatal("missing f export")
	}

	_, err = rt.Instantiate(parseWAT(t, `
		(module
			(type $s0 (func (param i32) (result f32)))
			(type $s2 (func (param i32 (ref $s0)) (result (ref $s0))))
			(type $t2 (func (param (ref $s2))))
			(func (import "M" "f") (param (ref $t2))))
	`), wasmvm.Imports{"M": {"f": f}})
	if err != nil {
		t.Fatalf("Instantiate importer failed: %v", err)
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

// TestExternRefRoundTrip checks that externref values keep their opaque
// identity through locals, tables, and function results.
func TestExternRefRoundTrip(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(table 2 externref)
			(func (export "store") (param externref)
				i32.const 1
				local.get 0
				table.set)
			(func (export "load") (result externref)
				i32.const 1
				table.get)
			(func (export "is_null") (param externref) (result i32)
				local.get 0
				ref.is_null))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	ref := wasmvm.ExternRef(17)
	results := callExport(t, inst, "is_null", ref)
	if len(results) != 1 || results[0] != wasmvm.I32(0) {
		t.Fatalf("is_null got results %#v, want i32 0", results)
	}

	callExport(t, inst, "store", ref)
	results = callExport(t, inst, "load")
	if len(results) != 1 || results[0] != ref {
		t.Fatalf("load got results %#v, want externref %#v", results, ref)
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
			(func (export "f32_sub_inf") (result f32) f32.const inf f32.const inf f32.sub)
			(func (export "f64_min") (param f64 f64) (result f64) local.get 0 local.get 1 f64.min)
			(func (export "f64_max") (param f64 f64) (result f64) local.get 0 local.get 1 f64.max)
			(func (export "f64_copysign") (param f64 f64) (result f64) local.get 0 local.get 1 f64.copysign)
			(func (export "f64_div_inf") (result f64) f64.const inf f64.const inf f64.div))
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

	const (
		canonicalF32NaNBits = 0x7fc00000
		canonicalF64NaNBits = 0x7ff8000000000000
	)
	for _, tt := range []struct {
		name string
		lhs  wasmvm.Value
		rhs  wasmvm.Value
		want uint64
	}{
		{name: "f32_min", lhs: wasmvm.F32(float32(math.Inf(-1))), rhs: wasmvm.F32(math.Float32frombits(canonicalF32NaNBits)), want: canonicalF32NaNBits},
		{name: "f32_max", lhs: wasmvm.F32(float32(math.Inf(1))), rhs: wasmvm.F32(math.Float32frombits(canonicalF32NaNBits)), want: canonicalF32NaNBits},
		{name: "f64_min", lhs: wasmvm.F64(math.Inf(-1)), rhs: wasmvm.F64(math.Float64frombits(canonicalF64NaNBits)), want: canonicalF64NaNBits},
		{name: "f64_max", lhs: wasmvm.F64(math.Inf(1)), rhs: wasmvm.F64(math.Float64frombits(canonicalF64NaNBits)), want: canonicalF64NaNBits},
	} {
		results := callExport(t, inst, tt.name, tt.lhs, tt.rhs)
		if len(results) != 1 {
			t.Fatalf("%s got results %#v, want one result", tt.name, results)
		}
		var got uint64
		if results[0].Type == wasmir.ValueTypeF32 {
			got = uint64(math.Float32bits(results[0].F32))
		} else {
			got = math.Float64bits(results[0].F64)
		}
		if got != tt.want {
			t.Fatalf("%s NaN bits = %#x, want %#x", tt.name, got, tt.want)
		}
	}

	results := callExport(t, inst, "f32_sub_inf")
	if len(results) != 1 || math.Float32bits(results[0].F32) != canonicalF32NaNBits {
		t.Fatalf("f32_sub_inf got results %#v, want canonical NaN", results)
	}
	results = callExport(t, inst, "f64_div_inf")
	if len(results) != 1 || math.Float64bits(results[0].F64) != canonicalF64NaNBits {
		t.Fatalf("f64_div_inf got results %#v, want canonical NaN", results)
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

func TestExportedGlobalValue(t *testing.T) {
	// ExportedGlobal exposes the same global state that executing code observes
	// through global.get/global.set.
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(global $g (export "g") (mut i32) (i32.const 7))
			(func (export "set_g") (param i32)
				local.get 0
				global.set $g))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}

	g, ok := inst.ExportedGlobal("g")
	if !ok {
		t.Fatal("missing g export")
	}
	if got, err := g.Value(); err != nil || got != wasmvm.I32(7) {
		t.Fatalf("g.Value() = %#v, %v; want i32 7, nil", got, err)
	}

	callExport(t, inst, "set_g", wasmvm.I32(42))
	if got, err := g.Value(); err != nil || got != wasmvm.I32(42) {
		t.Fatalf("g.Value() after set = %#v, %v; want i32 42, nil", got, err)
	}

	if _, ok := inst.ExportedGlobal("missing"); ok {
		t.Fatal("ExportedGlobal reported missing global as present")
	}
}

// TestGlobalImportSharesExportedGlobal checks that an exported global can be
// imported into another instance and remains shared.
func TestGlobalImportSharesExportedGlobal(t *testing.T) {
	rt := wasmvm.NewRuntime()
	exporter, err := rt.Instantiate(parseWAT(t, `
		(module
			(global $g (export "g") (mut i32) (i32.const 7))
			(func (export "set") (param i32)
				local.get 0
				global.set $g))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate exporter failed: %v", err)
	}
	global, ok := exporter.ExportedGlobal("g")
	if !ok {
		t.Fatal("missing g export")
	}

	importer, err := rt.Instantiate(parseWAT(t, `
		(module
			(import "env" "g" (global $g (mut i32)))
			(func (export "get") (result i32)
				global.get $g)
			(func (export "set") (param i32)
				local.get 0
				global.set $g))
	`), wasmvm.Imports{"env": {"g": global}})
	if err != nil {
		t.Fatalf("Instantiate importer failed: %v", err)
	}

	results := callExport(t, importer, "get")
	if len(results) != 1 || results[0] != wasmvm.I32(7) {
		t.Fatalf("importer get got results %#v, want i32 7", results)
	}
	callExport(t, exporter, "set", wasmvm.I32(11))
	results = callExport(t, importer, "get")
	if len(results) != 1 || results[0] != wasmvm.I32(11) {
		t.Fatalf("importer get after exporter set got results %#v, want i32 11", results)
	}
	callExport(t, importer, "set", wasmvm.I32(23))
	if got, err := global.Value(); err != nil || got != wasmvm.I32(23) {
		t.Fatalf("exported global after importer set = %#v, %v; want i32 23, nil", got, err)
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

// TestStartFunctionRunsDuringInstantiate checks that instantiation executes
// the module's start function before exported functions are called.
func TestStartFunctionRunsDuringInstantiate(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(parseWAT(t, `
		(module
			(memory 1)
			(global $g (mut i32) (i32.const 0))
			(func $start
				i32.const 0
				i32.const 42
				i32.store
				i32.const 7
				global.set $g)
			(start $start)
			(func (export "get_mem") (result i32)
				i32.const 0
				i32.load)
			(func (export "get_global") (result i32)
				global.get $g))
	`), nil)
	if err != nil {
		t.Fatalf("Instantiate failed: %v", err)
	}
	if got, want := callExport(t, inst, "get_mem")[0].I32, int32(42); got != want {
		t.Fatalf("get_mem() = %d, want %d", got, want)
	}
	if got, want := callExport(t, inst, "get_global")[0].I32, int32(7); got != want {
		t.Fatalf("get_global() = %d, want %d", got, want)
	}
}

// TestStartFunctionTrapFailsInstantiate checks that a trap in the module's
// start function fails instantiation.
func TestStartFunctionTrapFailsInstantiate(t *testing.T) {
	rt := wasmvm.NewRuntime()
	_, err := rt.Instantiate(parseWAT(t, `
		(module
			(func $start unreachable)
			(start $start))
	`), nil)
	if err == nil {
		t.Fatal("Instantiate succeeded unexpectedly")
	}
	if got, want := err.Error(), "start function: pc 0 unreachable: unreachable executed"; got != want {
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
