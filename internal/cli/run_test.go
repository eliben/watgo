package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eliben/watgo"
)

func TestRunParseStdinToStdout(t *testing.T) {
	wat := []byte("(module (func (export \"f\") (result i32) (i32.const 7)))")
	want, err := watgo.CompileWATToWASM(wat)
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"parse"}, bytes.NewReader(wat), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), want) {
		t.Fatalf("parse stdout mismatch:\n got=%x\nwant=%x", stdout.Bytes(), want)
	}
}

func TestRunParseFileToOutputFile(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.wat")
	output := filepath.Join(dir, "output.wasm")
	wat := []byte("(module (func (export \"f\") (result i32) (i32.const 9)))")
	if err := os.WriteFile(input, wat, 0o644); err != nil {
		t.Fatalf("WriteFile input failed: %v", err)
	}
	want, err := watgo.CompileWATToWASM(wat)
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"parse", input, "-o", output}, bytes.NewReader(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("ReadFile output failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("parse output mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestRunParseBinaryWASMToStdout(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 5)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"parse"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !bytes.Equal(stdout.Bytes(), wasm) {
		t.Fatalf("parse binary stdout mismatch:\n got=%x\nwant=%x", stdout.Bytes(), wasm)
	}
}

func TestRunValidateWAT(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(
		[]string{"validate"},
		strings.NewReader("(module (func (export \"f\") (result i32) (i32.const 1)))"),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("Run returned %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunValidateInvalidWAT(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(
		[]string{"validate"},
		strings.NewReader("(module (func (result i32)))"),
		&stdout,
		&stderr,
	)
	if code == 0 {
		t.Fatal("Run succeeded, want failure")
	}
	if !strings.Contains(stderr.String(), "validate") {
		t.Fatalf("stderr %q does not mention validate", stderr.String())
	}
}

func TestRunValidateWASM(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"validate"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
}

func TestRunPrintBinaryWASMToStdout(t *testing.T) {
	// `watgo print` should render basic binary wasm input as WAT on stdout.
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	roundTrip, err := watgo.CompileWATToWASM(stdout.Bytes())
	if err != nil {
		t.Fatalf("CompileWATToWASM(print output) failed: %v\nprinted:\n%s", err, stdout.String())
	}
	if !bytes.Equal(roundTrip, wasm) {
		t.Fatalf("print roundtrip mismatch:\nprinted:\n%s", stdout.String())
	}
}

func TestRunPrintIndent(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print", "--indent", "4"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "\n    (type") {
		t.Fatalf("stdout %q does not use four-space indentation", stdout.String())
	}
}

func TestRunPrintIndentTextTakesPriority(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print", "--indent", "4", "--indent-text", "\t"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "\n\t(type") {
		t.Fatalf("stdout %q does not use indent-text indentation", stdout.String())
	}
	if strings.Contains(stdout.String(), "\n    (type") {
		t.Fatalf("stdout %q used --indent despite --indent-text", stdout.String())
	}
}

func TestRunPrintNameUnnamed(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print", "--name-unnamed"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "(type $#type0") {
		t.Fatalf("stdout %q does not synthesize type name", stdout.String())
	}
	if !strings.Contains(stdout.String(), "(func $#func0") {
		t.Fatalf("stdout %q does not synthesize function name", stdout.String())
	}
}

func TestRunPrintSkeleton(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print", "--skeleton"}, bytes.NewReader(wasm), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "(func (type 0) (result i32) ...)") {
		t.Fatalf("stdout %q does not elide function body", stdout.String())
	}
	if strings.Contains(stdout.String(), "i32.const 3") {
		t.Fatalf("stdout %q did not elide function body instruction", stdout.String())
	}
}

func TestRunPrintAcceptsFlagsAfterInput(t *testing.T) {
	wasm, err := watgo.CompileWATToWASM([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWATToWASM failed: %v", err)
	}
	dir := t.TempDir()
	input := filepath.Join(dir, "input.wasm")
	if err := os.WriteFile(input, wasm, 0o644); err != nil {
		t.Fatalf("WriteFile input failed: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"print", input, "--name-unnamed", "--indent=4"}, bytes.NewReader(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "\n    (type $#type0") {
		t.Fatalf("stdout %q does not reflect flags after input", stdout.String())
	}
}

func TestRunPrintRejectsTextInputForNow(t *testing.T) {
	// The initial `print` command should reject text input until WAT emission is
	// implemented.
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print"}, strings.NewReader("(module)"), &stdout, &stderr)
	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "only binary wasm input is supported for now") {
		t.Fatalf("stderr %q does not contain text-input rejection", stderr.String())
	}
}

func TestRunRootHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout %q does not contain usage", stdout.String())
	}
	if !strings.Contains(stdout.String(), "watgo parse") {
		t.Fatalf("stdout %q does not mention parse", stdout.String())
	}
	if !strings.Contains(stdout.String(), "watgo print") {
		t.Fatalf("stdout %q does not mention print", stdout.String())
	}
	if !strings.Contains(stdout.String(), "--version") {
		t.Fatalf("stdout %q does not mention version", stdout.String())
	}
}

func TestRunNoArgsPrintsRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run returned %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr %q does not contain usage", stderr.String())
	}
}

func TestRunUnknownSubcommandPrintsRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"bogus"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run returned %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("stderr %q does not mention unknown subcommand", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr %q does not contain usage", stderr.String())
	}
}

func TestRunParseHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"parse", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "watgo parse [OPTIONS] [INPUT]") {
		t.Fatalf("stderr %q does not contain parse usage", stderr.String())
	}
}

func TestRunHelpParse(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help", "parse"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "watgo parse [OPTIONS] [INPUT]") {
		t.Fatalf("stderr %q does not contain parse usage", stderr.String())
	}
}

func TestRunPrintHelp(t *testing.T) {
	// `watgo print --help` should show the print subcommand usage.
	var stdout, stderr bytes.Buffer
	code := Run([]string{"print", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "watgo print [OPTIONS] [INPUT]") {
		t.Fatalf("stderr %q does not contain print usage", stderr.String())
	}
}

func TestRunHelpPrint(t *testing.T) {
	// `watgo help print` should show the same print subcommand usage.
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help", "print"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "watgo print [OPTIONS] [INPUT]") {
		t.Fatalf("stderr %q does not contain print usage", stderr.String())
	}
}

func TestRunValidateHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"validate", "--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "watgo validate [INPUT]") {
		t.Fatalf("stderr %q does not contain validate usage", stderr.String())
	}
}

func TestRunHelpValidate(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help", "validate"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "watgo validate [INPUT]") {
		t.Fatalf("stderr %q does not contain validate usage", stderr.String())
	}
}

func TestRunUnknownHelpTopicPrintsRootUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help", "bogus"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run returned %d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown help topic") {
		t.Fatalf("stderr %q does not mention unknown help topic", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr %q does not contain usage", stderr.String())
	}
}

func TestRunShortVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-V"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != versionString() {
		t.Fatalf("stdout %q, want %q", got, versionString())
	}
}

func TestRunLongVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"--version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != versionString() {
		t.Fatalf("stdout %q, want %q", got, versionString())
	}
}
