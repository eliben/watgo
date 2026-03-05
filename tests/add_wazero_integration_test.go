package tests

import (
	"context"
	"os"
	"testing"

	"github.com/eliben/watgo"
	"github.com/tetratelabs/wazero"
)

func TestAddModuleEndToEndWithWazero(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	wat := `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`

	wasmBytes, err := watgo.CompileWAT([]byte(wat))
	if err != nil {
		t.Fatalf("CompileWAT failed: %v", err)
	}

	ctx := context.Background()
	runtime := wazero.NewRuntime(ctx)
	defer func() {
		if closeErr := runtime.Close(ctx); closeErr != nil {
			t.Fatalf("wazero runtime close failed: %v", closeErr)
		}
	}()

	compiled, err := runtime.CompileModule(ctx, wasmBytes)
	if err != nil {
		t.Fatalf("CompileModule failed: %v", err)
	}
	defer compiled.Close(ctx)

	module, err := runtime.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		t.Fatalf("InstantiateModule failed: %v", err)
	}
	defer module.Close(ctx)

	addFn := module.ExportedFunction("add")
	if addFn == nil {
		t.Fatal("exported function add not found")
	}

	results, err := addFn.Call(ctx, 5, 7)
	if err != nil {
		t.Fatalf("wazero call failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}

	got := uint32(results[0])
	if got != 12 {
		t.Fatalf("got result %d, want 12", got)
	}
}
