package tests

import (
	"bytes"
	"context"
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

func TestWasmSpecScripts(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	runWasmSpecScriptsWith(t, runWasmSpecScriptFile)
}

func TestWasmSpecScriptsPrintRoundTrip(t *testing.T) {
	runWasmSpecScriptsWith(t, checkWasmSpecScriptPrintRoundTrip)
}

// runWasmSpecScriptsWith discovers the wasmspec script files once and runs fn
// for each script as its own subtest.
func runWasmSpecScriptsWith(t *testing.T, fn func(t *testing.T, scriptPath string)) {
	t.Helper()

	scripts, err := findWasmSpecScripts(wasmSpecScriptsDir)
	if err != nil {
		t.Fatalf("findWasmSpecScripts %q failed: %v", wasmSpecScriptsDir, err)
	}
	sort.Strings(scripts)

	if len(scripts) == 0 {
		t.Fatalf("no .wast scripts found in %q", wasmSpecScriptsDir)
	}

	for _, script := range scripts {
		name := filepath.ToSlash(strings.TrimSuffix(script, filepath.Ext(script)))
		t.Run(name, func(t *testing.T) {
			fn(t, filepath.Join(wasmSpecScriptsDir, script))
		})
	}
}

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

func runWasmSpecScriptFile(t *testing.T, scriptPath string) {
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
		fmt.Fprintf(os.Stderr, "[wasmspec %s] starting runner.run with %d commands\n", scriptName, len(commands))
	}

	runner, err := newScriptRunner(context.Background())
	if err != nil {
		t.Fatalf("spec runner bootstrap failed: %v", err)
	}
	defer func() {
		if wasmSpecDebugEnabled() {
			fmt.Fprintf(os.Stderr, "[wasmspec %s] closing node runner\n", filepath.ToSlash(scriptPath))
		}
		if closeErr := runner.closeWithLogf(logf); closeErr != nil {
			t.Fatalf("spec runner close failed: %v", closeErr)
		}
		if wasmSpecDebugEnabled() {
			fmt.Fprintf(os.Stderr, "[wasmspec %s] closed node runner\n", filepath.ToSlash(scriptPath))
		}
	}()
	results := runner.run(commands, opts)
	if wasmSpecDebugEnabled() {
		fmt.Fprintf(os.Stderr, "[wasmspec %s] finished runner.run\n", filepath.ToSlash(scriptPath))
	}
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
		if cmd.moduleExpr == nil {
			return nil, false, nil
		}
		// Skip script-only module forms handled specially by the harness. They do
		// not compile through the ordinary wasm text/binary pipeline.
		if isModuleDefinitionExpr(cmd.moduleExpr) || isModuleInstanceExpr(cmd.moduleExpr) {
			return nil, false, nil
		}
		// `(module quote "...")` already stores reconstructed module text.
		if cmd.quotedWAT != "" {
			return compileWasmSpecModuleText(cmd.quotedWAT)
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
	if trimmed := strings.TrimSpace(src); !strings.HasPrefix(trimmed, "(module") {
		src = "(module\n" + src + "\n)"
	}
	wasmBytes, err := watgo.CompileWATToWASM([]byte(src))
	if err != nil {
		return nil, false, err
	}
	wasmBytes, err = roundTripFixedPoint(wasmBytes)
	if err != nil {
		return nil, false, err
	}
	return wasmBytes, true, nil
}
