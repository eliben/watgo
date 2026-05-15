package tests

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/eliben/watgo"
)

type wabtNodePayload struct {
	WasmPath            string           `json:"wasmPath"`
	Exports             []wabtExport     `json:"exports"`
	Imports             []wabtImport     `json:"imports"`
	Invocations         []wabtInvocation `json:"invocations"`
	V128ResultHelperB64 string           `json:"v128ResultHelperB64"`
	HostPrint           bool             `json:"hostPrint"`
	DummyImportFunc     bool             `json:"dummyImportFunc"`
	HostPrintResultKind string           `json:"hostPrintResultKind"`
}

// wabtNodeBackend returns the Node-backed WABT interp execution backend.
func wabtNodeBackend() wabtBackend {
	return wabtBackend{
		name:                "node",
		requiresIntegration: true,
		run:                 runWABTNodeFixture,
	}
}

// runWABTNodeFixture executes one compiled fixture through Node.
func runWABTNodeFixture(t *testing.T, fixture wabtCompiledFixture) (wabtRunResult, error) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node executable not found (set WATGO_INTEGRATION=0 to skip integration tests): %v", err)
	}

	exports, err := wabtExports(fixture.m)
	if err != nil {
		return wabtRunResult{}, fmt.Errorf("wabtExports %q failed: %w", fixture.path, err)
	}

	tmpDir := t.TempDir()
	wasmPath := filepath.Join(tmpDir, strings.TrimSuffix(filepath.Base(fixture.path), ".txt")+".wasm")
	if err := os.WriteFile(wasmPath, fixture.wasmBytes, 0o644); err != nil {
		return wabtRunResult{}, fmt.Errorf("WriteFile %q failed: %w", wasmPath, err)
	}

	hostPrintResultKind, err := wabtHostPrintResultKind(fixture.m)
	if err != nil {
		return wabtRunResult{}, fmt.Errorf("wabtHostPrintResultKind %q failed: %w", fixture.path, err)
	}

	imports, err := wabtImports(fixture.m)
	if err != nil {
		return wabtRunResult{}, fmt.Errorf("wabtImports %q failed: %w", fixture.path, err)
	}

	return runWABTNode(nodePath, wasmPath, exports, imports, fixture.tc.runArgs, hostPrintResultKind)
}

// runWABTNode instantiates the compiled wasm in Node and executes the
// requested exports in order.
//
// Node reports results back as JSON rather than preformatted WABT-style text.
// For floats, the JS side returns raw IEEE-754 bit patterns so Go can format
// them with strconv.FormatFloat, which matches WABT's large-number output much
// more closely than JS number formatting does.
func runWABTNode(nodePath, wasmPath string, exports []wabtExport, imports []wabtImport, runArgs []string, hostPrintResultKind string) (wabtRunResult, error) {
	invocations, hostPrint, dummyImportFunc, err := wabtInvocations(exports, runArgs)
	if err != nil {
		return wabtRunResult{}, err
	}
	if runResult, ok := wabtValidateInvocations(exports, invocations); ok {
		return runResult, nil
	}
	helperB64, err := wabtV128ResultHelperBase64(exports)
	if err != nil {
		return wabtRunResult{}, err
	}
	return runWABTNodeWithInvocations(nodePath, wasmPath, exports, imports, invocations, helperB64, hostPrint, dummyImportFunc, hostPrintResultKind)
}

// runWABTNodeWithInvocations sends one invocation plan to the Node
// runner and converts its JSON response into WABT-style stdout.
func runWABTNodeWithInvocations(nodePath, wasmPath string, exports []wabtExport, imports []wabtImport, invocations []wabtInvocation, helperB64 string, hostPrint bool, dummyImportFunc bool, hostPrintResultKind string) (wabtRunResult, error) {
	payloadBytes, err := json.Marshal(wabtNodePayload{
		WasmPath:            wasmPath,
		Exports:             exports,
		Imports:             imports,
		Invocations:         invocations,
		V128ResultHelperB64: helperB64,
		HostPrint:           hostPrint,
		DummyImportFunc:     dummyImportFunc,
		HostPrintResultKind: hostPrintResultKind,
	})
	if err != nil {
		return wabtRunResult{}, err
	}
	tmpDir := filepath.Dir(wasmPath)
	payloadPath := filepath.Join(tmpDir, "wabt_interp_payload.json")
	if err := os.WriteFile(payloadPath, payloadBytes, 0o644); err != nil {
		return wabtRunResult{}, fmt.Errorf("WriteFile %q failed: %w", payloadPath, err)
	}
	runnerPath := filepath.Join(".", "wabt_interp.js")

	out, err := exec.Command(nodePath, runnerPath, payloadPath).CombinedOutput()
	if err != nil {
		return wabtRunResult{}, fmt.Errorf("node failed: %w\noutput:\n%s", err, out)
	}

	var payload struct {
		Stdout   []string     `json:"stdout"`
		Stderr   []string     `json:"stderr"`
		ExitCode int          `json:"exitCode"`
		Results  []wabtResult `json:"results"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return wabtRunResult{}, fmt.Errorf("decode node JSON: %w", err)
	}

	stdout := make([]string, 0, len(payload.Stdout)+len(payload.Results))
	nextStdout := 0
	for _, result := range payload.Results {
		for nextStdout < result.StdoutCount && nextStdout < len(payload.Stdout) {
			stdout = append(stdout, payload.Stdout[nextStdout])
			nextStdout++
		}
		line, err := formatWABTResult(result)
		if err != nil {
			return wabtRunResult{}, err
		}
		stdout = append(stdout, line)
	}
	stdout = append(stdout, payload.Stdout[nextStdout:]...)

	return wabtRunResult{
		Stdout:   strings.Join(stdout, "\n"),
		Stderr:   strings.Join(payload.Stderr, "\n"),
		ExitCode: payload.ExitCode,
	}, nil
}

var (
	wabtV128ResultHelperOnce sync.Once
	wabtV128ResultHelperB64  string
	wabtV128ResultHelperErr  error
)

// wabtV128ResultHelperBase64 returns a helper module for reading v128
// results through Node's public WebAssembly API.
func wabtV128ResultHelperBase64(exports []wabtExport) (string, error) {
	needsHelper := false
	for _, exp := range exports {
		if exp.Kind == "func" && exp.ResultKind == "v128" {
			needsHelper = true
			break
		}
	}
	if !needsHelper {
		return "", nil
	}

	wabtV128ResultHelperOnce.Do(func() {
		const helperWAT = `
(module
  (import "m" "f" (func $f (result v128)))
  (memory (export "mem") 1)
  (func (export "call")
    (i32.const 0)
    (call $f)
    v128.store))`
		wasmBytes, err := watgo.CompileWATToWASM([]byte(helperWAT))
		if err != nil {
			wabtV128ResultHelperErr = err
			return
		}
		wabtV128ResultHelperB64 = base64.StdEncoding.EncodeToString(wasmBytes)
	})
	return wabtV128ResultHelperB64, wabtV128ResultHelperErr
}
