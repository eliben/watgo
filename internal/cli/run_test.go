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
	want, err := watgo.CompileWAT(wat)
	if err != nil {
		t.Fatalf("CompileWAT failed: %v", err)
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
	want, err := watgo.CompileWAT(wat)
	if err != nil {
		t.Fatalf("CompileWAT failed: %v", err)
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
	wasm, err := watgo.CompileWAT([]byte("(module (func (export \"f\") (result i32) (i32.const 3)))"))
	if err != nil {
		t.Fatalf("CompileWAT failed: %v", err)
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
