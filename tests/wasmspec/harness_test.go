package wasmspec

import (
	"context"
	"os"
	"testing"
)

func TestParseI32WastCommands(t *testing.T) {
	src, err := os.ReadFile("i32.wast")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	commands, err := parseScript(string(src))
	if err != nil {
		t.Fatalf("parseScript failed: %v", err)
	}

	if len(commands) != 29 {
		t.Fatalf("got %d commands, want 29", len(commands))
	}

	wantByKind := map[commandKind]int{
		commandModule:          1,
		commandAssertReturn:    18,
		commandAssertTrap:      5,
		commandAssertInvalid:   3,
		commandAssertMalformed: 2,
	}
	gotByKind := map[commandKind]int{}
	for _, cmd := range commands {
		gotByKind[cmd.kind]++
	}

	for kind, want := range wantByKind {
		if got := gotByKind[kind]; got != want {
			t.Fatalf("kind %s: got %d commands, want %d", kind, got, want)
		}
	}
}

func TestRunI32WastHarness(t *testing.T) {
	if os.Getenv("WATGO_INTEGRATION") == "0" {
		t.Skip("integration tests disabled with WATGO_INTEGRATION=0")
	}

	src, err := os.ReadFile("i32.wast")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	commands, err := parseScript(string(src))
	if err != nil {
		t.Fatalf("parseScript failed: %v", err)
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
