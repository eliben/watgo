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
	printRoundTripFromWAT(t, `
(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`)
}

func TestPrintModule_CustomIndent(t *testing.T) {
	// Custom indentation should affect declaration and instruction levels while
	// preserving round-trip behavior.
	wasm, err := watgo.CompileWATToWASM([]byte(`
(module
  (func (export "f") (result i32)
    i32.const 3
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	m, err := watgo.DecodeWASM(wasm)
	if err != nil {
		t.Fatalf("DecodeWASM failed: %v", err)
	}
	printed, err := PrintModuleWithOptions(m, Options{IndentText: "    "})
	if err != nil {
		t.Fatalf("PrintModuleWithOptions failed: %v", err)
	}
	assertPrintedContains(t, printed, "\n    (type", "\n        i32.const")
	roundTrip, err := watgo.CompileWATToWASM(printed)
	if err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
	if !bytes.Equal(roundTrip, wasm) {
		t.Fatalf("roundtrip mismatch\nprinted:\n%s", printed)
	}
}

func TestPrintModule_NameUnnamed(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte(`
(module
  (type (func (result i32)))
  (global (mut i32) (i32.const 7))
  (func (type 0) (local i32)
    global.get 0
    local.set 0
    local.get 0
  )
  (func (export "call") (result i32)
    call 0
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	m, err := watgo.DecodeWASM(wasm)
	if err != nil {
		t.Fatalf("DecodeWASM failed: %v", err)
	}
	printed, err := PrintModuleWithOptions(m, Options{IndentText: "  ", NameUnnamed: true})
	if err != nil {
		t.Fatalf("PrintModuleWithOptions failed: %v", err)
	}
	assertPrintedContains(t, printed,
		"(type $#type0",
		"(global $#global0",
		"(func $#func0 (type $#type0)",
		"(local $#local0 i32)",
		"global.get $#global0",
		"local.set $#local0",
		"local.get $#local0",
		"call $#func0",
	)
	roundTrip, err := watgo.CompileWATToWASM(printed)
	if err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
	if len(roundTrip) == 0 {
		t.Fatalf("printed WAT compiled to empty wasm\nprinted:\n%s", printed)
	}
	if m.Types[0].Name != "" || m.Funcs[0].Name != "" || m.Funcs[0].LocalNames != nil || m.Globals[0].Name != "" {
		t.Fatalf("NameUnnamed mutated decoded module: %#v", m)
	}
}

func TestPrintModule_NameUnnamedStructField(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte(`
(module
  (type (struct (field i32)))
  (func (param (ref null 0)) (result i32)
    local.get 0
    struct.get 0 0
  )
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	m, err := watgo.DecodeWASM(wasm)
	if err != nil {
		t.Fatalf("DecodeWASM failed: %v", err)
	}
	printed, err := PrintModuleWithOptions(m, Options{IndentText: "  ", NameUnnamed: true})
	if err != nil {
		t.Fatalf("PrintModuleWithOptions failed: %v", err)
	}
	assertPrintedContains(t, printed,
		"(type $#type0 (struct (field $#field0 i32)))",
		"struct.get $#type0 $#field0",
	)
	if _, err := watgo.CompileWATToWASM(printed); err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, printed)
	}
}

func TestPrintModule_Skeleton(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte(`
(module
  (table 1 funcref)
  (memory 1)
  (func (local i32)
    i32.const 1
    drop
  )
  (elem (i32.const 0) func 0)
  (data (i32.const 0) "hello")
)`))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	m, err := watgo.DecodeWASM(wasm)
	if err != nil {
		t.Fatalf("DecodeWASM failed: %v", err)
	}
	opts := DefaultOptions()
	opts.Skeleton = true
	printed, err := PrintModuleWithOptions(m, opts)
	if err != nil {
		t.Fatalf("PrintModuleWithOptions failed: %v", err)
	}
	assertPrintedContains(t, printed,
		"(func (type 0) ...)",
		"(elem (table 0) (offset i32.const 0) ...)",
		"(data (offset i32.const 0) ...)",
	)
	assertPrintedNotContains(t, printed,
		"i32.const 1",
		"drop",
		"(local",
		`"hello"`,
		"func 0",
	)
}

func TestPrintModule_ImportsGlobalAndDataRoundTrip(t *testing.T) {
	// Basic top-level declarations such as imports, globals, and data segments
	// should print to valid WAT and round-trip back to the same bytes.
	printRoundTripFromWAT(t, `
(module
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
	printed := printRoundTripFromWAT(t, `
(module
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
	printed := printRoundTripFromWAT(t, `
(module
  (memory 1)
  (global i32 (i32.add (i32.const 1) (i32.const 2)))
  (data (i32.add (i32.const 4) (i32.const 5)) "x")
)`)
	assertPrintedContains(t, printed,
		"(global i32 i32.const 1 i32.const 2 i32.add)",
		"(data (offset i32.const 4 i32.const 5 i32.add) \"x\")",
	)
}

func TestPrintModule_EmptyTypedElemSegmentRoundTrip(t *testing.T) {
	// Empty passive element segments should preserve their explicit ref type
	// instead of printing the legacy `func` shorthand.
	printed := printRoundTripFromWAT(t, `
(module
  (table 1 funcref)
  (elem funcref)
  (func
    i32.const 0
    i32.const 0
    i32.const 0
    table.init 0
  )
)`)
	assertPrintedContains(t, printed, "(elem funcref)")
	assertPrintedNotContains(t, printed, "(elem func)")
}

func TestPrintModule_GCConstExprRoundTrip(t *testing.T) {
	// GC aggregate constant expressions should print as flat WAT in table
	// initializers and element item expressions.
	printed := printRoundTripFromWAT(t, `
(module
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
	printed := printRoundTripFromWAT(t, `
(module
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
	printed := printRoundTripFromWAT(t, `
(module
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
	printed := printRoundTripFromWAT(t, `
(module
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
	printed := printRoundTripFromWAT(t, `
(module $M
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

func TestPrintModule_CallRefUsesCallTypeIndex(t *testing.T) {
	// call_ref and return_call_ref should print their call type immediate, not
	// an unrelated GC type index field, so reparsing sees the right callee type.
	printed := printRoundTripFromWAT(t, `
(module
  (type $callee (func (result i32)))
  (global $g (ref $callee) (ref.func $f))
  (func $f (type $callee) (result i32)
    i32.const 7
  )
  (func (export "call") (result i32)
    global.get $g
    call_ref $callee
  )
  (func (export "tail") (result i32)
    global.get $g
    return_call_ref $callee
  )
)`)
	assertPrintedContains(t, printed, "call_ref $callee", "return_call_ref $callee")
}

func TestPrintModule_QuotesNonPlainIdentifiers(t *testing.T) {
	// Identifiers with whitespace or non-ASCII text should print back using the
	// quoted $"..." form instead of becoming invalid plain `$name` tokens.
	printed := printRoundTripFromWAT(t, `
(module
  (func $" spaced \t name ")
  (func (call $" spaced \t name "))
  (func $"")
  (func (call $""))
)`)
	assertPrintedContains(t, printed,
		`(func $" spaced \t name "`,
		`call $" spaced \t name "`,
		`(func $"\ef\98\9a\ef\92\a9"`,
		`call $"\ef\98\9a\ef\92\a9"`,
	)
}

func TestPrintModule_SIMDLaneMemoryInstrsRoundTrip(t *testing.T) {
	// SIMD load/store lane instructions should print their required lane
	// immediates in addition to any memory operands.
	printed := printRoundTripFromWAT(t, `
(module
  (memory 1)
  (func (param i32) (param v128) (result v128)
    local.get 0
    local.get 1
    v128.load8_lane offset=3 7
  )
  (func (param i32) (param v128)
    local.get 0
    local.get 1
    v128.store16_lane align=2 5
  )
)`)
	assertPrintedContains(t, printed,
		"v128.load8_lane offset=3 7",
		"v128.store16_lane align=2 5",
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
