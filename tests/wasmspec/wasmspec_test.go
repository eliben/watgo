package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

const wasmSpecScriptsDir = "scripts"
const wasmSpecDebugEnvVar = "WATGO_WASMSPEC_DEBUG"

func wasmSpecDebugEnabled() bool {
	return os.Getenv(wasmSpecDebugEnvVar) != ""
}

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
