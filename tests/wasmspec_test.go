package tests

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const wasmSpecScriptsDir = "wasmspec-scripts"

func TestWasmSpecScripts(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	entries, err := os.ReadDir(wasmSpecScriptsDir)
	if err != nil {
		t.Fatalf("ReadDir %q failed: %v", wasmSpecScriptsDir, err)
	}

	var scripts []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".wast") {
			scripts = append(scripts, e.Name())
		}
	}
	sort.Strings(scripts)

	if len(scripts) == 0 {
		t.Fatalf("no .wast scripts found in %q", wasmSpecScriptsDir)
	}

	for _, script := range scripts {
		name := strings.TrimSuffix(script, filepath.Ext(script))
		t.Run(name, func(t *testing.T) {
			runWasmSpecScriptFile(t, filepath.Join(wasmSpecScriptsDir, script))
		})
	}
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

	runner := newScriptRunner(context.Background())
	defer func() {
		if closeErr := runner.close(); closeErr != nil {
			t.Fatalf("wazero runtime close failed: %v", closeErr)
		}
	}()

	summary := runner.run(commands, runOptions{strictErrorText: false})
	if got, want := len(summary.results), len(commands); got != want {
		t.Fatalf("got %d command results, want %d", got, want)
	}

	if failCount := summary.statusCount(statusFail); failCount != 0 {
		for _, res := range summary.results {
			if res.status == statusFail {
				t.Logf("FAIL command[%d] %s at %s: %s", res.index, res.kind, res.loc, res.detail)
			}
		}
		t.Fatalf("got %d failed commands, want 0", failCount)
	}

	if passCount := summary.statusCount(statusPass); passCount == 0 {
		t.Fatalf("got %d passed commands, want > 0", passCount)
	}
}
