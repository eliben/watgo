package binaryformat

import (
	"bytes"
	"testing"

	"github.com/eliben/watgo/wasmir"
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

func TestDecodeModule_AddModule(t *testing.T) {
	m, err := DecodeModule(canonicalAddModuleBytes())
	if err != nil {
		t.Fatalf("DecodeModule failed: %v", err)
	}

	if len(m.Types) != 1 {
		t.Fatalf("got %d types, want 1", len(m.Types))
	}
	if len(m.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1", len(m.Funcs))
	}
	if len(m.Exports) != 1 {
		t.Fatalf("got %d exports, want 1", len(m.Exports))
	}

	ft := m.Types[0]
	if len(ft.Params) != 2 || ft.Params[0] != wasmir.ValueTypeI32 || ft.Params[1] != wasmir.ValueTypeI32 {
		t.Fatalf("unexpected params: %#v", ft.Params)
	}
	if len(ft.Results) != 1 || ft.Results[0] != wasmir.ValueTypeI32 {
		t.Fatalf("unexpected results: %#v", ft.Results)
	}

	fn := m.Funcs[0]
	if fn.TypeIdx != 0 {
		t.Fatalf("got typeidx=%d, want 0", fn.TypeIdx)
	}
	if len(fn.Locals) != 0 {
		t.Fatalf("got %d locals, want 0", len(fn.Locals))
	}
	if len(fn.Body) != 4 {
		t.Fatalf("got %d body instructions, want 4", len(fn.Body))
	}

	if fn.Body[0].Kind != wasmir.InstrLocalGet || fn.Body[0].LocalIndex != 0 {
		t.Fatalf("body[0]=%#v, want local.get 0", fn.Body[0])
	}
	if fn.Body[1].Kind != wasmir.InstrLocalGet || fn.Body[1].LocalIndex != 1 {
		t.Fatalf("body[1]=%#v, want local.get 1", fn.Body[1])
	}
	if fn.Body[2].Kind != wasmir.InstrI32Add {
		t.Fatalf("body[2]=%#v, want i32.add", fn.Body[2])
	}
	if fn.Body[3].Kind != wasmir.InstrEnd {
		t.Fatalf("body[3]=%#v, want end", fn.Body[3])
	}

	exp := m.Exports[0]
	if exp.Name != "add" || exp.Kind != wasmir.ExternalKindFunction || exp.Index != 0 {
		t.Fatalf("unexpected export: %#v", exp)
	}
}

func TestDecodeEncodeRoundTrip_AddModule(t *testing.T) {
	orig := canonicalAddModuleBytes()

	m, err := DecodeModule(orig)
	if err != nil {
		t.Fatalf("DecodeModule failed: %v", err)
	}

	got, err := EncodeModule(m)
	if err != nil {
		t.Fatalf("EncodeModule failed: %v", err)
	}

	if !bytes.Equal(got, orig) {
		t.Fatalf("roundtrip mismatch:\n got=%x\nwant=%x", got, orig)
	}
}
