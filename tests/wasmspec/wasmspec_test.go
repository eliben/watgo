package tests

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/internal/printer"
)

const wasmSpecScriptsDir = "scripts"
const wasmSpecDebugEnvVar = "WATGO_WASMSPEC_DEBUG"

type wasmSpecBackend struct {
	name                string
	scripts             []string
	requiresIntegration bool
	run                 func(t *testing.T, scriptPath string, commands []scriptCommand, opts runOptions) []commandResult
}

// wasmSpecPrintRoundTripSkippedScripts lists wasmspec scripts intentionally
// excluded from the broad print-roundtrip pass.
//
// These are either binary-only cases that do not normalize through our fixed
// decode/encode path yet, or scripts whose semantics depend on information WAT
// text roundtrips are not meant to preserve.
var wasmSpecPrintRoundTripSkippedScripts = []string{
	// `binary-leb128` exercises binary-only encodings, including non-minimal
	// LEB forms and custom sections, that the broad WAT print roundtrip is not
	// meant to preserve as a whole script.
	"binary-leb128.wast",
	// `elem` still exercises legacy element encodings we don't want to preserve
	// through print -> parse in the broad coverage pass.
	"elem.wast",

	// `custom` depends on custom-section text forms, which print is not meant
	// to preserve through WAT text.
	"custom.wast",

	// `names` relies on byte-for-byte preservation of the binary name section,
	// which print -> parse through WAT intentionally does not keep.
	"names.wast",
}

func wasmSpecDebugEnabled() bool {
	return os.Getenv(wasmSpecDebugEnvVar) != ""
}

// TestWasmSpecScripts runs the WebAssembly spec scripts through the Node.js
// backend.
func TestWasmSpecScripts(t *testing.T) {
	runWasmSpecBackend(t, wasmSpecNodeBackend())
}

// TestWasmSpecScriptsWasmvm runs the WebAssembly spec scripts currently enabled
// for the wasmvm backend.
func TestWasmSpecScriptsWasmvm(t *testing.T) {
	runWasmSpecBackend(t, wasmSpecWasmvmBackend(t))
}

// TestWasmSpecScriptsPrintRoundTrip checks wasmspec module print stability.
func TestWasmSpecScriptsPrintRoundTrip(t *testing.T) {
	runWasmSpecScriptFiles(t, nil, checkWasmSpecScriptPrintRoundTrip)
}

// runWasmSpecBackend runs one execution backend against its configured script
// set.
func runWasmSpecBackend(t *testing.T, backend wasmSpecBackend) {
	t.Helper()

	if backend.requiresIntegration && os.Getenv("WATGO_NODE_TESTS") == "0" {
		t.Skip("node tests disabled with WATGO_NODE_TESTS=0")
	}

	runWasmSpecScriptFiles(t, backend.scripts, func(t *testing.T, scriptPath string) {
		runWasmSpecScriptWithBackend(t, backend, scriptPath)
	})
}

// runWasmSpecScriptFiles discovers or accepts wasmspec script files and runs fn
// for each script as its own subtest.
func runWasmSpecScriptFiles(t *testing.T, scripts []string, fn func(t *testing.T, scriptPath string)) {
	t.Helper()

	if len(scripts) == 0 {
		var err error
		scripts, err = findWasmSpecScripts(wasmSpecScriptsDir)
		if err != nil {
			t.Fatalf("findWasmSpecScripts %q failed: %v", wasmSpecScriptsDir, err)
		}
		for i, script := range scripts {
			scripts[i] = filepath.Join(wasmSpecScriptsDir, script)
		}
	}
	sort.Strings(scripts)

	if len(scripts) == 0 {
		t.Fatalf("no .wast scripts found in %q", wasmSpecScriptsDir)
	}

	for _, script := range scripts {
		name := wasmSpecScriptTestName(script)
		t.Run(name, func(t *testing.T) {
			fn(t, script)
		})
	}
}

// wasmSpecScriptTestName returns the subtest name for a script path.
func wasmSpecScriptTestName(scriptPath string) string {
	name := filepath.ToSlash(scriptPath)
	name = strings.TrimPrefix(name, filepath.ToSlash(wasmSpecScriptsDir)+"/")
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// findWasmSpecScripts returns all .wast scripts below root, relative to root.
func findWasmSpecScripts(root string) ([]string, error) {
	var scripts []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".wast") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		scripts = append(scripts, rel)
		return nil
	})
	return scripts, err
}

// runWasmSpecScriptWithBackend parses one script, runs it through backend, and
// reports command-level failures.
func runWasmSpecScriptWithBackend(t *testing.T, backend wasmSpecBackend, scriptPath string) {
	t.Helper()

	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("ReadFile %q failed: %v", scriptPath, err)
	}

	commands, err := parseScript(string(src))
	if err != nil {
		t.Fatalf("parseScript for %q failed: %v", scriptPath, err)
	}

	var logf func(format string, args ...any)
	opts := runOptions{strictErrorText: false}
	if wasmSpecDebugEnabled() {
		scriptName := filepath.ToSlash(scriptPath)
		logf = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[wasmspec %s] %s\n", scriptName, fmt.Sprintf(format, args...))
		}
		opts.logf = logf
		opts.progress = func(index, total int, cmd scriptCommand) {
			fmt.Fprintf(os.Stderr, "[wasmspec %s] command %d/%d: %s at %s\n",
				scriptName, index+1, total, cmd.kind, cmd.loc)
		}
		opts.progressDone = func(index, total int, cmd scriptCommand, res commandResult, elapsed time.Duration) {
			status := "PASS"
			if !res.status {
				status = "FAIL"
			}
			fmt.Fprintf(os.Stderr, "[wasmspec %s] done command %d/%d: %s at %s -> %s (%s)\n",
				scriptName, index+1, total, cmd.kind, cmd.loc, status, elapsed)
		}
		fmt.Fprintf(os.Stderr, "[wasmspec %s %s] starting backend with %d commands\n", backend.name, scriptName, len(commands))
	}

	results := backend.run(t, scriptPath, commands, opts)
	if got, want := len(results), len(commands); got != want {
		t.Fatalf("got %d command results, want %d", got, want)
	}

	failCount := 0
	passCount := 0
	for _, res := range results {
		if res.status {
			passCount++
		} else {
			failCount++
			t.Logf("FAIL command[%d] %s at %s: %s", res.index, res.kind, res.loc, res.detail)
		}
	}

	if failCount != 0 {
		t.Fatalf("got %d failed commands, want 0", failCount)
	}

	if passCount == 0 {
		t.Fatalf("got %d passed commands, want > 0", passCount)
	}
}

// checkWasmSpecScriptPrintRoundTrip verifies that each valid module compiled
// from a wasmspec script remains byte-stable after a print roundtrip.
func checkWasmSpecScriptPrintRoundTrip(t *testing.T, scriptPath string) {
	t.Helper()

	rel := filepath.ToSlash(strings.TrimPrefix(scriptPath, wasmSpecScriptsDir+string(filepath.Separator)))
	if slices.Contains(wasmSpecPrintRoundTripSkippedScripts, rel) {
		t.Skip("script is intentionally excluded from broad print-roundtrip coverage for now")
	}

	src, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("ReadFile %q failed: %v", scriptPath, err)
	}

	commands, err := parseScript(string(src))
	if err != nil {
		t.Fatalf("parseScript for %q failed: %v", scriptPath, err)
	}

	checked := 0
	for i, cmd := range commands {
		wasmBytes, ok, err := compileWasmSpecCommandForPrintRoundTrip(cmd)
		if err != nil {
			t.Fatalf("compileWasmSpecCommandForPrintRoundTrip %q command[%d] %s at %s failed: %v", scriptPath, i, cmd.kind, cmd.loc, err)
		}
		if !ok {
			continue
		}
		checked++

		decoded, err := watgo.DecodeWASM(wasmBytes)
		if err != nil {
			t.Fatalf("DecodeWASM %q command[%d] %s at %s failed: %v", scriptPath, i, cmd.kind, cmd.loc, err)
		}
		printed, err := printer.PrintModule(decoded)
		if err != nil {
			t.Fatalf("PrintModule %q command[%d] %s at %s failed: %v", scriptPath, i, cmd.kind, cmd.loc, err)
		}
		roundTripped, err := watgo.CompileWATToWASM(printed)
		if err != nil {
			t.Fatalf("CompileWATToWASM(print output for %q command[%d] %s at %s) failed: %v\nprinted:\n%s", scriptPath, i, cmd.kind, cmd.loc, err, printed)
		}
		if !bytes.Equal(roundTripped, wasmBytes) {
			t.Fatalf("print roundtrip changed bytes for %q command[%d] %s at %s\nprinted:\n%s", scriptPath, i, cmd.kind, cmd.loc, printed)
		}
	}

	if checked == 0 {
		t.Skip("no roundtrip-eligible module commands found")
	}
}

// compileWasmSpecCommandForPrintRoundTrip extracts one wasm module from cmd for
// the broad print-roundtrip pass.
//
// It returns:
//   - wasm bytes normalized through roundTripFixedPoint when cmd contributes one
//     concrete wasm module we want to check
//   - ok=false when cmd is not a real wasm module for this purpose, for example
//     non-module commands or script-only module forms that the harness models
//     directly
//   - err when cmd should contribute a module but extracting/compiling that
//     module fails
//
// This helper is intentionally selective: the print-roundtrip pass only wants
// module shapes that correspond to ordinary wasm binaries, not every script
// command that happens to mention a module-like form.
func compileWasmSpecCommandForPrintRoundTrip(cmd scriptCommand) ([]byte, bool, error) {
	switch cmd.kind {
	case commandModule:
		// Skip script-only module forms handled specially by the harness. They do
		// not compile through the ordinary wasm text/binary pipeline.
		if isModuleDefinitionExpr(cmd.moduleExpr) || isModuleInstanceExpr(cmd.moduleExpr) {
			return nil, false, nil
		}
		// `(module quote "...")` and inline module-field scripts already store
		// reconstructed module text.
		if cmd.quotedWAT != "" {
			return compileWasmSpecModuleText(cmd.quotedWAT)
		}
		if cmd.moduleExpr == nil {
			return nil, false, nil
		}
		// `(module binary "...")` contributes a concrete wasm blob directly, so
		// normalize it through the binary fixed-point helper before comparing it
		// against print->parse output.
		if isModuleBinaryExpr(cmd.moduleExpr) {
			bytesBlob, err := parseBinaryModuleBytes(cmd.moduleExpr)
			if err != nil {
				return nil, false, err
			}
			stable, err := roundTripFixedPoint(bytesBlob)
			if err != nil {
				return nil, false, err
			}
			return stable, true, nil
		}
		// Ordinary text `(module ...)` commands are reconstructed back into WAT and
		// compiled through the same text pipeline used by the main harness.
		src, err := sexprToWAT(cmd.moduleExpr)
		if err != nil {
			return nil, false, fmt.Errorf("module text generation failed: %w", err)
		}
		return compileWasmSpecModuleText(src)
	case commandAssertTrap:
		// Spec scripts also allow module trap assertions of the form
		// `(assert_trap (module ...) "...")`; these embed a valid module that is
		// expected to trap during instantiation, so they still participate in
		// printer roundtrips.
		if cmd.moduleExpr == nil {
			return nil, false, nil
		}
		if isModuleBinaryExpr(cmd.moduleExpr) {
			bytesBlob, err := parseBinaryModuleBytes(cmd.moduleExpr)
			if err != nil {
				return nil, false, err
			}
			stable, err := roundTripFixedPoint(bytesBlob)
			if err != nil {
				return nil, false, err
			}
			return stable, true, nil
		}
		src, err := sexprToWAT(cmd.moduleExpr)
		if err != nil {
			return nil, false, fmt.Errorf("module text generation failed: %w", err)
		}
		return compileWasmSpecModuleText(src)
	default:
		return nil, false, nil
	}
}

// compileWasmSpecModuleText compiles one text module source and normalizes it
// through the binary fixed-point pass used by the main wasmspec harness.
func compileWasmSpecModuleText(src string) ([]byte, bool, error) {
	wasmBytes, err := compileWATModule(src)
	if err != nil {
		return nil, false, err
	}
	wasmBytes, err = roundTripFixedPoint(wasmBytes)
	if err != nil {
		return nil, false, err
	}
	return wasmBytes, true, nil
}
