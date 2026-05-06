package wasmvm_test

import (
	"testing"

	"github.com/eliben/watgo/wasmir"
	"github.com/eliben/watgo/wasmvm"
)

func makeAddModule() *wasmir.Module {
	return &wasmir.Module{
		Types: []wasmir.TypeDef{{
			Kind:    wasmir.TypeDefKindFunc,
			Params:  []wasmir.ValueType{wasmir.ValueTypeI32, wasmir.ValueTypeI32},
			Results: []wasmir.ValueType{wasmir.ValueTypeI32},
		}},
		Funcs: []wasmir.Function{{
			TypeIdx: 0,
			Body: []wasmir.Instruction{
				{Kind: wasmir.InstrLocalGet, LocalIndex: 0},
				{Kind: wasmir.InstrLocalGet, LocalIndex: 1},
				{Kind: wasmir.InstrI32Add},
				{Kind: wasmir.InstrEnd},
			},
		}},
		Exports: []wasmir.Export{{
			Name:  "add",
			Kind:  wasmir.ExternalKindFunction,
			Index: 0,
		}},
	}
}

func TestExportedAdd(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(makeAddModule(), nil)
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

func makeCallImportModule() *wasmir.Module {
	return &wasmir.Module{
		Types: []wasmir.TypeDef{{
			Kind:    wasmir.TypeDefKindFunc,
			Params:  []wasmir.ValueType{wasmir.ValueTypeI32},
			Results: []wasmir.ValueType{wasmir.ValueTypeI32},
		}},
		Imports: []wasmir.Import{{
			Module:  "env",
			Name:    "inc",
			Kind:    wasmir.ExternalKindFunction,
			TypeIdx: 0,
		}},
		Funcs: []wasmir.Function{{
			TypeIdx: 0,
			Body: []wasmir.Instruction{
				{Kind: wasmir.InstrLocalGet, LocalIndex: 0},
				{Kind: wasmir.InstrCall, FuncIndex: 0},
				{Kind: wasmir.InstrEnd},
			},
		}},
		Exports: []wasmir.Export{{
			Name:  "call_inc",
			Kind:  wasmir.ExternalKindFunction,
			Index: 1,
		}},
	}
}

func TestHostFunctionImport(t *testing.T) {
	rt := wasmvm.NewRuntime()
	inst, err := rt.Instantiate(makeCallImportModule(), wasmvm.Imports{
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
