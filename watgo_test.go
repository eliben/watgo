package watgo_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/diag"
)

func canonicalAddModuleBytes() []byte {
	return []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x07, 0x01, 0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7f,
		0x03, 0x02, 0x01, 0x00,
		0x07, 0x07, 0x01, 0x03, 0x61, 0x64, 0x64, 0x00, 0x00,
		0x0a, 0x09, 0x01, 0x07, 0x00, 0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b,
	}
}

func TestCompileWAT_PublicAPI(t *testing.T) {
	wat := []byte(`(module
  (func (export "add") (param $a i32) (param $b i32) (result i32)
    local.get $a
    local.get $b
    i32.add
  )
)`)

	got, err := watgo.CompileWAT(wat)
	if err != nil {
		t.Fatalf("CompileWAT failed: %v", err)
	}

	want := canonicalAddModuleBytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("CompileWAT bytes mismatch:\n got=%x\nwant=%x", got, want)
	}
}

func TestCompileWAT_ErrorList_PublicAPI(t *testing.T) {
	_, err := watgo.CompileWAT([]byte("(module"))
	if err == nil {
		t.Fatal("expected CompileWAT to fail")
	}

	if _, ok := errors.AsType[diag.ErrorList](err); !ok {
		t.Fatalf("expected diag.ErrorList, got %T (%v)", err, err)
	}
}
