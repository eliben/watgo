package printer

import (
	"bytes"
	"strings"
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

func TestPrintModule_PrintsFormerlyFoldedInstructionsFlat(t *testing.T) {
	// Instructions that used folded printer output only to satisfy parser gaps
	// should now print as ordinary flat instruction lines.
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
  (type $Box (array (ref eq)))
  (memory 1)
  (memory 1)
  (func (param anyref) (result i32)
    local.get 0
    ref.test (ref i31)
    local.get 0
    ref.cast anyref
    drop
  )
  (func (param i32)
    i32.const 0
    i32.const 0
    local.get 0
    memory.copy 1 0
  )
  (func (result (ref $Box))
    i32.const 1
    ref.i31
    array.new_fixed $Box 1
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	printed := printDecodedModule(t, wasm)
	printedText := string(printed)
	for _, folded := range []string{"(ref.test", "(ref.cast", "(memory.copy", "(array.new_fixed"} {
		if strings.Contains(printedText, folded) {
			t.Fatalf("printed WAT contains folded %q form:\n%s", folded, printed)
		}
	}
	for _, flat := range []string{"ref.test (ref i31)", "ref.cast anyref", "memory.copy 1 0", "array.new_fixed 0 1"} {
		if !strings.Contains(printedText, flat) {
			t.Fatalf("printed WAT missing flat %q form:\n%s", flat, printed)
		}
	}

	if _, err := watgo.CompileWATToWASM(printed); err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
}

func TestPrintModule_MultiInstructionConstExprRoundTrip(t *testing.T) {
	// Multi-instruction constant expressions should print as flat instruction
	// sequences that compile back to the same binary.
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
  (memory 1)
  (global i32 (i32.add (i32.const 1) (i32.const 2)))
  (data (i32.add (i32.const 4) (i32.const 5)) "x")
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	printed := printDecodedModule(t, wasm)
	printedText := string(printed)
	for _, want := range []string{
		"(global i32 i32.const 1 i32.const 2 i32.add)",
		"(data (offset i32.const 4 i32.const 5 i32.add) \"x\")",
	} {
		if !strings.Contains(printedText, want) {
			t.Fatalf("printed WAT missing %q:\n%s", want, printed)
		}
	}

	roundTrip, err := watgo.CompileWATToWASM(printed)
	if err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
	if !bytes.Equal(roundTrip, wasm) {
		t.Fatalf("roundtrip mismatch\nprinted:\n%s", printed)
	}
}

func TestPrintModule_GCConstExprRoundTrip(t *testing.T) {
	// GC aggregate constant expressions should print as flat WAT in table
	// initializers and element item expressions.
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
  (type $Arr (array i32))
  (table 1 (ref $Arr) i32.const 4 array.new_default $Arr)
  (elem declare (ref $Arr) (item i32.const 7 i32.const 8 array.new_fixed $Arr 2))
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	printed := printDecodedModule(t, wasm)
	printedText := string(printed)
	for _, want := range []string{
		"i32.const 4 array.new_default 0",
		"(item i32.const 7 i32.const 8 array.new_fixed 0 2)",
	} {
		if !strings.Contains(printedText, want) {
			t.Fatalf("printed WAT missing %q:\n%s", want, printed)
		}
	}
	if _, err := watgo.CompileWATToWASM(printed); err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
}

func TestPrintModule_TryTableRoundTrip(t *testing.T) {
	// try_table should print as a flat structured-control header with catch
	// clauses and compile back to the same binary.
	wasm, err := watgo.CompileWATToWASM([]byte(`(module
  (tag $e)
  (func
    block
      try_table (catch $e 0) (catch_all 0)
        nop
      end
    end
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	printed := printDecodedModule(t, wasm)
	printedText := string(printed)
	if !strings.Contains(printedText, "try_table (catch 0 0) (catch_all 0)") {
		t.Fatalf("printed WAT missing flat try_table header:\n%s", printed)
	}
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
