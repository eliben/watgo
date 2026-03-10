package binaryformat

import (
	"bytes"
	"encoding/binary"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
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
	// valueTypeI64Code is the binary encoding of i64.
	valueTypeI64Code byte = 0x7e
	// valueTypeF32Code is the binary encoding of f32.
	valueTypeF32Code byte = 0x7d
	// valueTypeF64Code is the binary encoding of f64.
	valueTypeF64Code byte = 0x7c

	// exportKindFunctionCode tags a function export entry.
	exportKindFunctionCode byte = 0x00

	// Opcodes for the currently supported instruction subset.
	opIfCode         byte = 0x04
	opElseCode       byte = 0x05
	opEndCode        byte = 0x0b
	opI32ConstCode   byte = 0x41
	opI64ConstCode   byte = 0x42
	opF32ConstCode   byte = 0x43
	opF64ConstCode   byte = 0x44
	opDropCode       byte = 0x1a
	opLocalGetCode   byte = 0x20
	opCallCode       byte = 0x10
	opI32AddCode     byte = 0x6a
	opI32SubCode     byte = 0x6b
	opI32MulCode     byte = 0x6c
	opI32DivSCode    byte = 0x6d
	opI32DivUCode    byte = 0x6e
	opI64AddCode     byte = 0x7c
	opI64EqzCode     byte = 0x50
	opI64LeUCode     byte = 0x58
	opI64SubCode     byte = 0x7d
	opI64MulCode     byte = 0x7e
	opI64DivSCode    byte = 0x7f
	opI64DivUCode    byte = 0x80
	opF32CeilCode    byte = 0x8d
	opF32FloorCode   byte = 0x8e
	opF32TruncCode   byte = 0x8f
	opF32NearestCode byte = 0x90
	opF32SqrtCode    byte = 0x91
	opF32AddCode     byte = 0x92
	opF32SubCode     byte = 0x93
	opF32MulCode     byte = 0x94
	opF32DivCode     byte = 0x95
	opF32MinCode     byte = 0x96
	opF32MaxCode     byte = 0x97
	opF64CeilCode    byte = 0x9b
	opF64FloorCode   byte = 0x9c
	opF64TruncCode   byte = 0x9d
	opF64NearestCode byte = 0x9e
	opF64SqrtCode    byte = 0x9f
	opF64AddCode     byte = 0xa0
	opF64SubCode     byte = 0xa1
	opF64MulCode     byte = 0xa2
	opF64DivCode     byte = 0xa3
	opF64MinCode     byte = 0xa4
	opF64MaxCode     byte = 0xa5

	// blockTypeEmptyCode is the no-result blocktype used by block/loop/if.
	blockTypeEmptyCode byte = 0x40
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
	case wasmir.InstrIf:
		out.WriteByte(opIfCode)
		if instr.BlockHasResult {
			b, ok := valueTypeCode(instr.BlockType)
			if !ok {
				diags.Addf("func[%d] instruction[%d]: unsupported if result type %d", funcIdx, instrIdx, instr.BlockType)
				b = blockTypeEmptyCode
			}
			out.WriteByte(b)
		} else {
			out.WriteByte(blockTypeEmptyCode)
		}
	case wasmir.InstrElse:
		out.WriteByte(opElseCode)
	case wasmir.InstrI32Const:
		out.WriteByte(opI32ConstCode)
		writeSLEB128(out, int64(instr.I32Const))
	case wasmir.InstrI64Const:
		out.WriteByte(opI64ConstCode)
		writeSLEB128(out, instr.I64Const)
	case wasmir.InstrF32Const:
		out.WriteByte(opF32ConstCode)
		writeU32LE(out, instr.F32Const)
	case wasmir.InstrF64Const:
		out.WriteByte(opF64ConstCode)
		writeU64LE(out, instr.F64Const)
	case wasmir.InstrDrop:
		out.WriteByte(opDropCode)
	case wasmir.InstrLocalGet:
		out.WriteByte(opLocalGetCode)
		writeULEB128(out, instr.LocalIndex)
	case wasmir.InstrCall:
		out.WriteByte(opCallCode)
		writeULEB128(out, instr.FuncIndex)
	case wasmir.InstrI32Add:
		out.WriteByte(opI32AddCode)
	case wasmir.InstrI32Sub:
		out.WriteByte(opI32SubCode)
	case wasmir.InstrI32Mul:
		out.WriteByte(opI32MulCode)
	case wasmir.InstrI32DivS:
		out.WriteByte(opI32DivSCode)
	case wasmir.InstrI32DivU:
		out.WriteByte(opI32DivUCode)
	case wasmir.InstrI64Add:
		out.WriteByte(opI64AddCode)
	case wasmir.InstrI64Eqz:
		out.WriteByte(opI64EqzCode)
	case wasmir.InstrI64LeU:
		out.WriteByte(opI64LeUCode)
	case wasmir.InstrI64Sub:
		out.WriteByte(opI64SubCode)
	case wasmir.InstrI64Mul:
		out.WriteByte(opI64MulCode)
	case wasmir.InstrI64DivS:
		out.WriteByte(opI64DivSCode)
	case wasmir.InstrI64DivU:
		out.WriteByte(opI64DivUCode)
	case wasmir.InstrF32Add:
		out.WriteByte(opF32AddCode)
	case wasmir.InstrF32Sub:
		out.WriteByte(opF32SubCode)
	case wasmir.InstrF32Mul:
		out.WriteByte(opF32MulCode)
	case wasmir.InstrF32Div:
		out.WriteByte(opF32DivCode)
	case wasmir.InstrF32Sqrt:
		out.WriteByte(opF32SqrtCode)
	case wasmir.InstrF32Min:
		out.WriteByte(opF32MinCode)
	case wasmir.InstrF32Max:
		out.WriteByte(opF32MaxCode)
	case wasmir.InstrF32Ceil:
		out.WriteByte(opF32CeilCode)
	case wasmir.InstrF32Floor:
		out.WriteByte(opF32FloorCode)
	case wasmir.InstrF32Trunc:
		out.WriteByte(opF32TruncCode)
	case wasmir.InstrF32Nearest:
		out.WriteByte(opF32NearestCode)
	case wasmir.InstrF64Add:
		out.WriteByte(opF64AddCode)
	case wasmir.InstrF64Sub:
		out.WriteByte(opF64SubCode)
	case wasmir.InstrF64Mul:
		out.WriteByte(opF64MulCode)
	case wasmir.InstrF64Div:
		out.WriteByte(opF64DivCode)
	case wasmir.InstrF64Sqrt:
		out.WriteByte(opF64SqrtCode)
	case wasmir.InstrF64Min:
		out.WriteByte(opF64MinCode)
	case wasmir.InstrF64Max:
		out.WriteByte(opF64MaxCode)
	case wasmir.InstrF64Ceil:
		out.WriteByte(opF64CeilCode)
	case wasmir.InstrF64Floor:
		out.WriteByte(opF64FloorCode)
	case wasmir.InstrF64Trunc:
		out.WriteByte(opF64TruncCode)
	case wasmir.InstrF64Nearest:
		out.WriteByte(opF64NearestCode)
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
	case wasmir.ValueTypeI64:
		return valueTypeI64Code, true
	case wasmir.ValueTypeF32:
		return valueTypeF32Code, true
	case wasmir.ValueTypeF64:
		return valueTypeF64Code, true
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
	var buf [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(buf[:], uint64(v))
	out.Write(buf[:n])
}

// writeSLEB128 writes v as a WebAssembly signed LEB128 integer.
//
// Important: this cannot use encoding/binary's PutVarint/AppendVarint.
// Those functions implement Go's generic signed varint format (with
// zig-zag-style mapping), which intentionally differs from WASM's signed
// LEB128 byte encoding used for immediates in the binary format.
//
// Example mismatch:
//   - value -1 in WASM SLEB128 is 0x7f
//   - binary.PutVarint(-1) emits 0x01
//
// For correctness and round-tripping with other WASM tools/runtimes, we emit
// canonical signed LEB128 bytes directly here.
func writeSLEB128(out *bytes.Buffer, v int64) {
	for {
		b := byte(v & 0x7f)
		v >>= 7

		signBitSet := (b & 0x40) != 0
		done := (v == 0 && !signBitSet) || (v == -1 && signBitSet)
		if done {
			out.WriteByte(b)
			return
		}

		out.WriteByte(b | 0x80)
	}
}

// writeU32LE writes v as a 4-byte little-endian integer.
func writeU32LE(out *bytes.Buffer, v uint32) {
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], v)
	out.Write(buf[:])
}

// writeU64LE writes v as an 8-byte little-endian integer.
func writeU64LE(out *bytes.Buffer, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	out.Write(buf[:])
}
