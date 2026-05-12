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
				select (result i64)))
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
