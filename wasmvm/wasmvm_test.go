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
