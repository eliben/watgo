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
	printRoundTripFromWAT(t, `(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`)
}

func TestPrintModule_ImportsGlobalAndDataRoundTrip(t *testing.T) {
	// Basic top-level declarations such as imports, globals, and data segments
	// should print to valid WAT and round-trip back to the same bytes.
	printRoundTripFromWAT(t, `(module
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
)`)
}

func TestPrintModule_PrintsFormerlyFoldedInstructionsFlat(t *testing.T) {
	// Instructions that used folded printer output only to satisfy parser gaps
	// should now print as ordinary flat instruction lines.
	printed := printRoundTripFromWAT(t, `(module
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
)`)
	assertPrintedNotContains(t, printed, "(ref.test", "(ref.cast", "(memory.copy", "(array.new_fixed")
	assertPrintedContains(t, printed, "ref.test (ref i31)", "ref.cast anyref", "memory.copy 1 0", "array.new_fixed $Box 1")
}

func TestPrintModule_MultiInstructionConstExprRoundTrip(t *testing.T) {
	// Multi-instruction constant expressions should print as flat instruction
	// sequences that compile back to the same binary.
	printed := printRoundTripFromWAT(t, `(module
  (memory 1)
  (global i32 (i32.add (i32.const 1) (i32.const 2)))
  (data (i32.add (i32.const 4) (i32.const 5)) "x")
)`)
	assertPrintedContains(t, printed,
		"(global i32 i32.const 1 i32.const 2 i32.add)",
		"(data (offset i32.const 4 i32.const 5 i32.add) \"x\")",
	)
}

func TestPrintModule_GCConstExprRoundTrip(t *testing.T) {
	// GC aggregate constant expressions should print as flat WAT in table
	// initializers and element item expressions.
	printed := printRoundTripFromWAT(t, `(module
  (type $Arr (array i32))
  (table 1 (ref $Arr) i32.const 4 array.new_default $Arr)
  (elem declare (ref $Arr) (item i32.const 7 i32.const 8 array.new_fixed $Arr 2))
)`)
	assertPrintedContains(t, printed,
		"i32.const 4 array.new_default $Arr",
		"(item i32.const 7 i32.const 8 array.new_fixed $Arr 2)",
	)
}

func TestPrintModule_PreservesNaNPayloads(t *testing.T) {
	// Floating-point constants should preserve NaN payload bits through printer
	// output so recompiling the printed WAT yields identical bytes.
	printed := printRoundTripFromWAT(t, `(module
  (func (result i32)
    f32.const nan:0x20
    i32.reinterpret_f32
  )
  (func (result i64)
    f64.const -nan:0x20
    i64.reinterpret_f64
  )
)`)
	assertPrintedContains(t, printed, "f32.const nan:0x20", "f64.const -nan:0x20")
}

func TestPrintModule_TryTableRoundTrip(t *testing.T) {
	// try_table should print as a flat structured-control header with catch
	// clauses and compile back to the same binary.
	printed := printRoundTripFromWAT(t, `(module
  (tag $e)
  (func
    block
      try_table (catch $e 0) (catch_all 0)
        nop
      end
    end
  )
)`)
	assertPrintedContains(t, printed, "try_table (catch $e 0) (catch_all 0)")
}

func TestPrintModule_RecursiveSubtypeRoundTrip(t *testing.T) {
	// Recursive and subtype type declarations should print using `(rec ...)`
	// and `(sub ...)` wrappers that compile back to the same binary.
	printed := printRoundTripFromWAT(t, `(module
  (rec
    (type $base (sub (struct)))
    (type $child (sub final $base (struct (field i32))))
  )
)`)
	assertPrintedContains(t, printed,
		"(rec",
		"(type $base (sub (struct)))",
		"(type $child (sub final $base (struct (field i32))))",
	)
}

func TestPrintModule_PrefersNamedReferences(t *testing.T) {
	// Named declarations from source/debug info should be reused for internal
	// references instead of printing raw indices when wasmir carries the names.
	printed := printRoundTripFromWAT(t, `(module $M
  (type $T (func))
  (type $S (struct (field $f i32)))
  (type $Reader (func (param (ref $S)) (result i32)))
  (tag $e (param i32))
  (global $g (mut i32) (i32.const 0))
  (func $callee (type $T))
  (func $maker (result (ref $S))
    struct.new_default $S
  )
  (func $reader (type $Reader) (param $s (ref $S)) (result i32)
    local.get $s
    struct.get $S $f
  )
  (func $starter (type $T)
    call $callee
    global.get $g
    drop
    i32.const 1
    throw $e
    unreachable
  )
  (start $starter)
)`)
	assertPrintedContains(t, printed,
		"(module $M",
		"(func $callee (type $T))",
		"struct.new_default $S",
		"struct.get $S $f",
		"call $callee",
		"throw $e",
		"(start $starter)",
	)
}

// printRoundTripFromWAT compiles wat, prints the decoded module back to WAT,
// recompiles the printed text, and checks that the wasm bytes are preserved.
func printRoundTripFromWAT(t *testing.T, wat string) []byte {
	t.Helper()

	wasm, err := watgo.CompileWATToWASM([]byte(wat))
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
	return printed
}

// assertPrintedContains checks that every wanted substring appears in printed.
func assertPrintedContains(t *testing.T, printed []byte, wants ...string) {
	t.Helper()

	printedText := string(printed)
	for _, want := range wants {
		if !strings.Contains(printedText, want) {
			t.Fatalf("printed WAT missing %q:\n%s", want, printed)
		}
	}
}

// assertPrintedNotContains checks that none of the rejected substrings appear
// in printed.
func assertPrintedNotContains(t *testing.T, printed []byte, rejects ...string) {
	t.Helper()

	printedText := string(printed)
	for _, reject := range rejects {
		if strings.Contains(printedText, reject) {
			t.Fatalf("printed WAT contains %q:\n%s", reject, printed)
		}
	}
}

// printDecodedModule decodes wasm into wasmir and runs the printer on the
// decoded module.
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
