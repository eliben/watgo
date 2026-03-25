package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eliben/watgo"
)

const wasmWatSamplesDir = "wasm-wat-samples"

func TestWasmWatSamples(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

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
		if entry.IsDir() {
			samples = append(samples, entry.Name())
		}
	}
	sort.Strings(samples)

	if len(samples) == 0 {
		t.Fatalf("no sample directories found in %q", wasmWatSamplesDir)
	}

	for _, sample := range samples {
		t.Run(sample, func(t *testing.T) {
			runWasmWatSample(t, nodePath, filepath.Join(wasmWatSamplesDir, sample))
		})
	}
}

func runWasmWatSample(t *testing.T, nodePath, srcDir string) {
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
