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
			runWasmSpecScriptFile(t, filepath.Join(wasmSpecScriptsDir, script))
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

	runner, err := newScriptRunner(context.Background())
	if err != nil {
		t.Fatalf("spec runner bootstrap failed: %v", err)
	}
	defer func() {
		if closeErr := runner.close(); closeErr != nil {
			t.Fatalf("spec runner close failed: %v", closeErr)
		}
	}()

	results := runner.run(commands, runOptions{strictErrorText: false})
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
