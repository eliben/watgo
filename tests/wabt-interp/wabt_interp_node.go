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

type wabtInterpNodePayload struct {
	WasmPath            string                 `json:"wasmPath"`
	Exports             []wabtInterpExport     `json:"exports"`
	Imports             []wabtInterpImport     `json:"imports"`
	Invocations         []wabtInterpInvocation `json:"invocations"`
	V128ResultHelperB64 string                 `json:"v128ResultHelperB64"`
	HostPrint           bool                   `json:"hostPrint"`
	DummyImportFunc     bool                   `json:"dummyImportFunc"`
	HostPrintResultKind string                 `json:"hostPrintResultKind"`
}

// wabtInterpNodeBackend returns the Node-backed WABT interp execution backend.
func wabtInterpNodeBackend() wabtInterpBackend {
	return wabtInterpBackend{
		name:                "node",
		requiresIntegration: true,
		run:                 runWABTInterpNodeFixture,
	}
}

// runWABTInterpNodeFixture executes one compiled fixture through Node.
func runWABTInterpNodeFixture(t *testing.T, fixture wabtInterpCompiledFixture) (wabtInterpRunResult, error) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node executable not found (set WATGO_INTEGRATION=0 to skip integration tests): %v", err)
	}

	exports, err := wabtInterpExports(fixture.m)
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("wabtInterpExports %q failed: %w", fixture.path, err)
	}

	tmpDir := t.TempDir()
	wasmPath := filepath.Join(tmpDir, strings.TrimSuffix(filepath.Base(fixture.path), ".txt")+".wasm")
	if err := os.WriteFile(wasmPath, fixture.wasmBytes, 0o644); err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("WriteFile %q failed: %w", wasmPath, err)
	}

	hostPrintResultKind, err := wabtInterpHostPrintResultKind(fixture.m)
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("wabtInterpHostPrintResultKind %q failed: %w", fixture.path, err)
	}

	imports, err := wabtInterpImports(fixture.m)
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("wabtInterpImports %q failed: %w", fixture.path, err)
	}

	return runWABTInterpNode(nodePath, wasmPath, exports, imports, fixture.tc.runArgs, hostPrintResultKind)
}

// runWABTInterpNode instantiates the compiled wasm in Node and executes the
// requested exports in order.
//
// Node reports results back as JSON rather than preformatted WABT-style text.
// For floats, the JS side returns raw IEEE-754 bit patterns so Go can format
// them with strconv.FormatFloat, which matches WABT's large-number output much
// more closely than JS number formatting does.
func runWABTInterpNode(nodePath, wasmPath string, exports []wabtInterpExport, imports []wabtInterpImport, runArgs []string, hostPrintResultKind string) (wabtInterpRunResult, error) {
	invocations, hostPrint, dummyImportFunc, err := wabtInterpInvocations(exports, runArgs)
	if err != nil {
		return wabtInterpRunResult{}, err
	}
	if runResult, ok := wabtInterpValidateInvocations(exports, invocations); ok {
		return runResult, nil
	}
	helperB64, err := wabtInterpV128ResultHelperBase64(exports)
	if err != nil {
		return wabtInterpRunResult{}, err
	}
	return runWABTInterpNodeWithInvocations(nodePath, wasmPath, exports, imports, invocations, helperB64, hostPrint, dummyImportFunc, hostPrintResultKind)
}

// runWABTInterpNodeWithInvocations sends one invocation plan to the Node
// runner and converts its JSON response into WABT-style stdout.
func runWABTInterpNodeWithInvocations(nodePath, wasmPath string, exports []wabtInterpExport, imports []wabtInterpImport, invocations []wabtInterpInvocation, helperB64 string, hostPrint bool, dummyImportFunc bool, hostPrintResultKind string) (wabtInterpRunResult, error) {
	payloadBytes, err := json.Marshal(wabtInterpNodePayload{
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
		return wabtInterpRunResult{}, err
	}
	tmpDir := filepath.Dir(wasmPath)
	payloadPath := filepath.Join(tmpDir, "wabt_interp_payload.json")
	if err := os.WriteFile(payloadPath, payloadBytes, 0o644); err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("WriteFile %q failed: %w", payloadPath, err)
	}
	runnerPath := filepath.Join(".", "wabt_interp.js")

	out, err := exec.Command(nodePath, runnerPath, payloadPath).CombinedOutput()
	if err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("node failed: %w\noutput:\n%s", err, out)
	}

	var payload struct {
		Stdout   []string           `json:"stdout"`
		Stderr   []string           `json:"stderr"`
		ExitCode int                `json:"exitCode"`
		Results  []wabtInterpResult `json:"results"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return wabtInterpRunResult{}, fmt.Errorf("decode node JSON: %w", err)
	}

	stdout := make([]string, 0, len(payload.Stdout)+len(payload.Results))
	nextStdout := 0
	for _, result := range payload.Results {
		for nextStdout < result.StdoutCount && nextStdout < len(payload.Stdout) {
			stdout = append(stdout, payload.Stdout[nextStdout])
			nextStdout++
		}
		line, err := formatWABTInterpResult(result)
		if err != nil {
			return wabtInterpRunResult{}, err
		}
		stdout = append(stdout, line)
	}
	stdout = append(stdout, payload.Stdout[nextStdout:]...)

	return wabtInterpRunResult{
		Stdout:   strings.Join(stdout, "\n"),
		Stderr:   strings.Join(payload.Stderr, "\n"),
		ExitCode: payload.ExitCode,
	}, nil
}

var (
	wabtInterpV128ResultHelperOnce sync.Once
	wabtInterpV128ResultHelperB64  string
	wabtInterpV128ResultHelperErr  error
)

// wabtInterpV128ResultHelperBase64 returns a helper module for reading v128
// results through Node's public WebAssembly API.
func wabtInterpV128ResultHelperBase64(exports []wabtInterpExport) (string, error) {
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

	wabtInterpV128ResultHelperOnce.Do(func() {
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
			wabtInterpV128ResultHelperErr = err
			return
		}
		wabtInterpV128ResultHelperB64 = base64.StdEncoding.EncodeToString(wasmBytes)
	})
	return wabtInterpV128ResultHelperB64, wabtInterpV128ResultHelperErr
}
