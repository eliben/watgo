package printer

import (
	"bytes"
	"testing"

	"github.com/eliben/watgo"
)

func TestPrintModule_AddFunctionRoundTrip(t *testing.T) {
	// A simple function-only module should print to WAT that watgo can compile
	// back to the original bytes.
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	printed := printDecodedModule(t, wasm)
	roundTrip, err := watgo.CompileWATToWASM(printed)
	if err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
	if !bytes.Equal(roundTrip, wasm) {
		t.Fatalf("roundtrip mismatch\nprinted:\n%s", printed)
	}
}

func TestPrintModule_ImportsGlobalAndDataRoundTrip(t *testing.T) {
	// Basic top-level declarations such as imports, globals, and data segments
	// should print to valid WAT and round-trip back to the same bytes.
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
  (import "env" "f" (func (param i32) (result i32)))
  (memory 1)
  (global (mut i32) (i32.const 7))
  (data (i32.const 0) "hi")
  (func (export "run") (param i32) (result i32)
    global.get 0
    local.get 0
    call 0
    i32.add
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	printed := printDecodedModule(t, wasm)
	roundTrip, err := watgo.CompileWATToWASM(printed)
	if err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
	if !bytes.Equal(roundTrip, wasm) {
		t.Fatalf("roundtrip mismatch\nprinted:\n%s", printed)
	}
}

func printDecodedModule(t *testing.T, wasm []byte) []byte {
	t.Helper()

	m, err := watgo.DecodeWASM(wasm)
	if err != nil {
		t.Fatalf("DecodeWASM failed: %v", err)
	}
	printed, err := PrintModule(m)
	if err != nil {
		t.Fatalf("PrintModule failed: %v", err)
	}
	return printed
}
