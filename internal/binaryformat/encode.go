package binaryformat

import (
	"bytes"

	"github.com/eliben/watgo/internal/diag"
	"github.com/eliben/watgo/internal/wasmir"
)

const (
	// wasmMagic is the 4-byte preamble a WASM binary starts with.
	wasmMagic = "\x00asm"
	// wasmVersion is the current WASM binary format version in little-endian.
	// Version 1 is encoded as bytes {0x01, 0x00, 0x00, 0x00}.
	wasmVersion = "\x01\x00\x00\x00"

	// Section IDs follow the core binary spec's section id table.
	sectionTypeID     byte = 1
	sectionFunctionID byte = 3
	sectionExportID   byte = 7
	sectionCodeID     byte = 10

	// typeCodeFunc tags a function type entry in the type section.
	typeCodeFunc byte = 0x60

	// valueTypeI32Code is the binary encoding of i32.
	valueTypeI32Code byte = 0x7f

	// exportKindFunctionCode tags a function export entry.
	exportKindFunctionCode byte = 0x00

	// Opcodes for the currently supported instruction subset.
	opLocalGetCode byte = 0x20
	opI32AddCode   byte = 0x6a
	opEndCode      byte = 0x0b
)

// EncodeModule encodes m into WASM binary format and returns bytes and all
// diagnostics collected during encoding.
// It returns nil error on success. On any failure, it returns diag.ErrorList.
func EncodeModule(m *wasmir.Module) ([]byte, error) {
	if m == nil {
		return nil, diag.Fromf("module is nil")
	}

	var diags diag.ErrorList
	var out bytes.Buffer
	// Module preamble: magic then binary format version.
	out.WriteString(wasmMagic)
	out.WriteString(wasmVersion)

	// This MVP encoder emits only type/function/export/code sections.
	// They are written in the prescribed module order.
	typeSection := encodeTypeSection(m.Types, &diags)
	if len(typeSection) > 0 {
		writeSection(&out, sectionTypeID, typeSection)
	}

	functionSection := encodeFunctionSection(m.Funcs)
	if len(functionSection) > 0 {
		writeSection(&out, sectionFunctionID, functionSection)
	}

	exportSection := encodeExportSection(m.Exports, &diags)
	if len(exportSection) > 0 {
		writeSection(&out, sectionExportID, exportSection)
	}

	codeSection := encodeCodeSection(m.Funcs, &diags)
	if len(codeSection) > 0 {
		writeSection(&out, sectionCodeID, codeSection)
	}

	if diags.HasAny() {
		return nil, diags
	}
	return out.Bytes(), nil
}

// writeSection emits one section as:
//
//	section-id byte, section-size (u32 LEB128), section-payload bytes.
func writeSection(out *bytes.Buffer, id byte, payload []byte) {
	out.WriteByte(id)
	writeULEB128(out, uint32(len(payload)))
	out.Write(payload)
}

// encodeTypeSection emits section 1.
// In this slice we encode a vector of function types only.
func encodeTypeSection(types []wasmir.FuncType, diags *diag.ErrorList) []byte {
	if len(types) == 0 {
		return nil
	}

	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(types)))

	for i, ft := range types {
		payload.WriteByte(typeCodeFunc)

		writeULEB128(&payload, uint32(len(ft.Params)))
		for j, p := range ft.Params {
			b, ok := valueTypeCode(p)
			if !ok {
				diags.Addf("type[%d] param[%d]: unsupported value type %d", i, j, p)
				b = 0
			}
			payload.WriteByte(b)
		}

		writeULEB128(&payload, uint32(len(ft.Results)))
		for j, r := range ft.Results {
			b, ok := valueTypeCode(r)
			if !ok {
				diags.Addf("type[%d] result[%d]: unsupported value type %d", i, j, r)
				b = 0
			}
			payload.WriteByte(b)
		}
	}

	return payload.Bytes()
}

// encodeFunctionSection emits section 3.
// The payload is a vector of type indices classifying defined functions.
func encodeFunctionSection(funcs []wasmir.Function) []byte {
	if len(funcs) == 0 {
		return nil
	}

	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(funcs)))
	for _, fn := range funcs {
		writeULEB128(&payload, fn.TypeIdx)
	}
	return payload.Bytes()
}

// encodeExportSection emits section 7 as a vector of exports.
// Each export entry is: name, external kind tag, and external index.
func encodeExportSection(exports []wasmir.Export, diags *diag.ErrorList) []byte {
	if len(exports) == 0 {
		return nil
	}

	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(exports)))

	for i, exp := range exports {
		name := []byte(exp.Name)
		writeULEB128(&payload, uint32(len(name)))
		payload.Write(name)

		kindByte, ok := exportKindCode(exp.Kind)
		if !ok {
			diags.Addf("export[%d]: unsupported kind %d", i, exp.Kind)
			kindByte = 0
		}
		payload.WriteByte(kindByte)
		writeULEB128(&payload, exp.Index)
	}

	return payload.Bytes()
}

// encodeCodeSection emits section 10.
// The payload is a vector of code entries; each code entry is:
//
//	byte size of function code, then function code bytes.
//
// Function code bytes are:
//
//	local declarations vector, then the instruction expression body.
func encodeCodeSection(funcs []wasmir.Function, diags *diag.ErrorList) []byte {
	if len(funcs) == 0 {
		return nil
	}

	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(funcs)))

	for i, fn := range funcs {
		var body bytes.Buffer

		// Local declarations are encoded as (count, value_type) entries.
		// We currently emit one entry per local with count=1.
		writeULEB128(&body, uint32(len(fn.Locals)))
		for j, localTy := range fn.Locals {
			writeULEB128(&body, 1)
			b, ok := valueTypeCode(localTy)
			if !ok {
				diags.Addf("func[%d] local[%d]: unsupported value type %d", i, j, localTy)
				b = 0
			}
			body.WriteByte(b)
		}

		for j, instr := range fn.Body {
			encodeInstr(&body, i, j, instr, diags)
		}

		writeULEB128(&payload, uint32(body.Len()))
		payload.Write(body.Bytes())
	}

	return payload.Bytes()
}

// encodeInstr maps semantic instruction kinds to binary opcodes.
func encodeInstr(out *bytes.Buffer, funcIdx int, instrIdx int, instr wasmir.Instruction, diags *diag.ErrorList) {
	switch instr.Kind {
	case wasmir.InstrLocalGet:
		out.WriteByte(opLocalGetCode)
		writeULEB128(out, instr.LocalIndex)
	case wasmir.InstrI32Add:
		out.WriteByte(opI32AddCode)
	case wasmir.InstrEnd:
		out.WriteByte(opEndCode)
	default:
		diags.Addf("func[%d] instruction[%d]: unsupported instruction kind %d", funcIdx, instrIdx, instr.Kind)
	}
}

func valueTypeCode(vt wasmir.ValueType) (byte, bool) {
	switch vt {
	case wasmir.ValueTypeI32:
		return valueTypeI32Code, true
	default:
		return 0, false
	}
}

func exportKindCode(kind wasmir.ExternalKind) (byte, bool) {
	switch kind {
	case wasmir.ExternalKindFunction:
		return exportKindFunctionCode, true
	default:
		return 0, false
	}
}

// writeULEB128 writes v as an unsigned LEB128-encoded integer.
func writeULEB128(out *bytes.Buffer, v uint32) {
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b |= 0x80
		}
		out.WriteByte(b)
		if v == 0 {
			return
		}
	}
}
