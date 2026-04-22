package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/internal/printer"
)

const wasmWatSamplesDir = "."

// unsupportedPrintRoundTripSamples tracks sample directories that currently hit
// known printer limitations during WAT -> wasm -> WAT -> wasm roundtrips.
var unsupportedPrintRoundTripSamples = map[string]string{
	"gc-array-of-structs":   "printer does not yet emit valid WAT for typed GC refs",
	"gc-cast-check-and-i31": "printer does not yet emit valid WAT for typed GC refs",
	"gc-cast-type":          "printer does not yet emit valid WAT for typed GC refs",
	"gc-collect-demo":       "printer does not yet emit valid WAT for typed GC refs",
	"gc-linked-list":        "printer does not yet emit valid WAT for typed GC refs",
	"gc-print-scheme-pairs": "printer does not yet emit valid WAT for typed GC refs",
	"memory-multiple":       "printer does not yet emit valid WAT for multi-memory instructions",
	"reference-types":       "printer does not yet emit valid WAT for typed function references",
}

func TestWasmWatSamples(t *testing.T) {
	// The upstream sample corpus should compile from source WAT and pass its
	// JavaScript integration checks unchanged.
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	runWasmWatSamples(t, false)
}

func TestWasmWatSamplesPrintRoundTrip(t *testing.T) {
	// The sample corpus should still pass its integration checks after each WAT
	// module is compiled to wasm, printed back to WAT, and recompiled.
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	runWasmWatSamples(t, true)
}

// runWasmWatSamples executes the sample corpus, optionally forcing each module
// through a print roundtrip before running its JavaScript integration check.
func runWasmWatSamples(t *testing.T, printRoundTrip bool) {
	t.Helper()

	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node executable not found (set WATGO_INTEGRATION=0 to skip integration tests): %v", err)
	}

	entries, err := os.ReadDir(wasmWatSamplesDir)
	if err != nil {
		t.Fatalf("ReadDir %q failed: %v", wasmWatSamplesDir, err)
	}

	var samples []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(entry.Name(), "_") {
			samples = append(samples, entry.Name())
		}
	}
	sort.Strings(samples)

	if len(samples) == 0 {
		t.Fatalf("no sample directories found in %q", wasmWatSamplesDir)
	}

	for _, sample := range samples {
		t.Run(sample, func(t *testing.T) {
			if printRoundTrip {
				if reason, ok := unsupportedPrintRoundTripSamples[sample]; ok {
					t.Skip(reason)
				}
			}
			runWasmWatSample(t, nodePath, filepath.Join(wasmWatSamplesDir, sample), printRoundTrip)
		})
	}
}

// runWasmWatSample prepares one sample directory, compiles its WAT files, and
// runs the sample's Node.js test script. When printRoundTrip is true, each
// module is recompiled from printer output before execution.
func runWasmWatSample(t *testing.T, nodePath, srcDir string, printRoundTrip bool) {
	t.Helper()

	workDir := filepath.Join(t.TempDir(), filepath.Base(srcDir))
	if err := copyDirFiles(srcDir, workDir); err != nil {
		t.Fatalf("copyDirFiles %q failed: %v", srcDir, err)
	}

	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatalf("ReadDir %q failed: %v", workDir, err)
	}

	watCount := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wat") {
			continue
		}

		watCount++
		watPath := filepath.Join(workDir, entry.Name())
		src, err := os.ReadFile(watPath)
		if err != nil {
			t.Fatalf("ReadFile %q failed: %v", watPath, err)
		}

		wasmBytes, err := watgo.CompileWATToWASM(src)
		if err != nil {
			t.Fatalf("CompileWATToWASM %q failed: %v", watPath, err)
		}
		if printRoundTrip {
			decoded, err := watgo.DecodeWASM(wasmBytes)
			if err != nil {
				t.Fatalf("DecodeWASM %q failed: %v", watPath, err)
			}
			printed, err := printer.PrintModule(decoded)
			if err != nil {
				t.Fatalf("PrintModule %q failed: %v", watPath, err)
			}
			wasmBytes, err = watgo.CompileWATToWASM(printed)
			if err != nil {
				t.Fatalf("CompileWATToWASM(print output for %q) failed: %v\nprinted:\n%s", watPath, err, printed)
			}
		}

		wasmPath := strings.TrimSuffix(watPath, filepath.Ext(watPath)) + ".wasm"
		if err := os.WriteFile(wasmPath, wasmBytes, 0o644); err != nil {
			t.Fatalf("WriteFile %q failed: %v", wasmPath, err)
		}
	}

	if watCount == 0 {
		t.Fatalf("no .wat files found in %q", workDir)
	}

	testJSPath := filepath.Join(workDir, "test.js")
	if _, err := os.Stat(testJSPath); err != nil {
		t.Fatalf("Stat %q failed: %v", testJSPath, err)
	}

	cmd := exec.Command(nodePath, "test.js")
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node %q failed: %v\noutput:\n%s", testJSPath, err, out)
	}
}

func copyDirFiles(srcDir, dstDir string) error {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(srcDir, entry.Name())
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dstDir, entry.Name())
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
