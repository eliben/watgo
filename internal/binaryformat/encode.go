package binaryformat

import (
	"bytes"
	"encoding/binary"
	"fmt"

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
	sectionImportID   byte = 2
	sectionFunctionID byte = 3
	sectionTableID    byte = 4
	sectionMemoryID   byte = 5
	sectionGlobalID   byte = 6
	sectionExportID   byte = 7
	sectionElementID  byte = 9
	sectionCodeID     byte = 10

	// typeCodeFunc tags a function type entry in the type section.
	typeCodeFunc byte = 0x60

	valueTypeI32Code       byte = 0x7f
	valueTypeI64Code       byte = 0x7e
	valueTypeF32Code       byte = 0x7d
	valueTypeF64Code       byte = 0x7c
	valueTypeExternRefCode byte = 0x6f

	exportKindFunctionCode byte = 0x00
	exportKindTableCode    byte = 0x01
	exportKindMemoryCode   byte = 0x02
	exportKindGlobalCode   byte = 0x03

	importKindFunctionCode byte = 0x00
	importKindTableCode    byte = 0x01
	importKindMemoryCode   byte = 0x02
	importKindGlobalCode   byte = 0x03

	refTypeFuncRefCode   byte = 0x70
	refTypeExternRefCode byte = 0x6f

	// Opcodes for the currently supported instruction subset.
	opBlockCode         byte = 0x02
	opLoopCode          byte = 0x03
	opIfCode            byte = 0x04
	opElseCode          byte = 0x05
	opBrCode            byte = 0x0c
	opBrIfCode          byte = 0x0d
	opBrTableCode       byte = 0x0e
	opUnreachableCode   byte = 0x00
	opNopCode           byte = 0x01
	opReturnCode        byte = 0x0f
	opEndCode           byte = 0x0b
	opI32ConstCode      byte = 0x41
	opI64ConstCode      byte = 0x42
	opF32ConstCode      byte = 0x43
	opF64ConstCode      byte = 0x44
	opDropCode          byte = 0x1a
	opSelectCode        byte = 0x1b
	opLocalGetCode      byte = 0x20
	opLocalSetCode      byte = 0x21
	opLocalTeeCode      byte = 0x22
	opGlobalGetCode     byte = 0x23
	opGlobalSetCode     byte = 0x24
	opTableGetCode      byte = 0x25
	opTableSetCode      byte = 0x26
	opCallCode          byte = 0x10
	opCallIndirectCode  byte = 0x11
	opI32LoadCode       byte = 0x28
	opI32StoreCode      byte = 0x36
	opMemoryGrowCode    byte = 0x40
	opI32EqCode         byte = 0x46
	opF32GtCode         byte = 0x5e
	opI32AddCode        byte = 0x6a
	opI32SubCode        byte = 0x6b
	opI32MulCode        byte = 0x6c
	opI32CtzCode        byte = 0x68
	opI32DivSCode       byte = 0x6d
	opI32DivUCode       byte = 0x6e
	opI32RemSCode       byte = 0x6f
	opI32RemUCode       byte = 0x70
	opI32ShlCode        byte = 0x74
	opI32ShrSCode       byte = 0x75
	opI32ShrUCode       byte = 0x76
	opI32EqzCode        byte = 0x45
	opI32LtSCode        byte = 0x48
	opI32LtUCode        byte = 0x49
	opI64AddCode        byte = 0x7c
	opI64EqCode         byte = 0x51
	opI64EqzCode        byte = 0x50
	opI64GtSCode        byte = 0x55
	opI64GtUCode        byte = 0x56
	opI64LeUCode        byte = 0x58
	opI64SubCode        byte = 0x7d
	opI64MulCode        byte = 0x7e
	opI64DivSCode       byte = 0x7f
	opI64DivUCode       byte = 0x80
	opI64RemSCode       byte = 0x81
	opI64RemUCode       byte = 0x82
	opI64ShlCode        byte = 0x86
	opI64ShrSCode       byte = 0x87
	opI64ShrUCode       byte = 0x88
	opI64LtSCode        byte = 0x53
	opI64LtUCode        byte = 0x54
	opI32WrapI64Code    byte = 0xa7
	opI64ExtendI32SCode byte = 0xac
	opI64ExtendI32UCode byte = 0xad
	opF32CeilCode       byte = 0x8d
	opF32FloorCode      byte = 0x8e
	opF32TruncCode      byte = 0x8f
	opF32NearestCode    byte = 0x90
	opF32SqrtCode       byte = 0x91
	opF32NegCode        byte = 0x8c
	opF32AddCode        byte = 0x92
	opF32SubCode        byte = 0x93
	opF32MulCode        byte = 0x94
	opF32DivCode        byte = 0x95
	opF32MinCode        byte = 0x96
	opF32MaxCode        byte = 0x97
	opF64CeilCode       byte = 0x9b
	opF64FloorCode      byte = 0x9c
	opF64TruncCode      byte = 0x9d
	opF64NearestCode    byte = 0x9e
	opF64SqrtCode       byte = 0x9f
	opF64NegCode        byte = 0x9a
	opF64AddCode        byte = 0xa0
	opF64SubCode        byte = 0xa1
	opF64MulCode        byte = 0xa2
	opF64DivCode        byte = 0xa3
	opF64MinCode        byte = 0xa4
	opF64MaxCode        byte = 0xa5
	opRefNullCode       byte = 0xd0
	opRefIsNullCode     byte = 0xd1
	opRefFuncCode       byte = 0xd2

	// blockTypeEmptyCode is the no-result blocktype used by block/loop/if.
	blockTypeEmptyCode byte = 0x40

	// globalMutabilityConstCode marks an immutable global type.
	globalMutabilityConstCode byte = 0x00
	// globalMutabilityVarCode marks a mutable global type.
	globalMutabilityVarCode byte = 0x01

	// limitsFlagMinOnly encodes limits with only a minimum bound.
	limitsFlagMinOnly byte = 0x00
	// limitsFlagMinMax encodes limits with both minimum and maximum bounds.
	limitsFlagMinMax byte = 0x01

	// elemSegmentFlagActiveTable0FuncIndices encodes an active element segment
	// for table 0 using function indices (legacy/table-0 form).
	elemSegmentFlagActiveTable0FuncIndices byte = 0x00
	// elemSegmentFlagActiveExplicitTableFuncIndices encodes an active element
	// segment with an explicit table index and function indices.
	elemSegmentFlagActiveExplicitTableFuncIndices byte = 0x02
	// elemSegmentFlagActiveExplicitTableExprs encodes an active element segment
	// with an explicit table index and reference-typed const expressions.
	elemSegmentFlagActiveExplicitTableExprs byte = 0x06

	// elemKindFuncRef marks legacy function-index element payloads as funcref.
	elemKindFuncRef byte = 0x00
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

	importSection := encodeImportSection(m.Imports, &diags)
	if len(importSection) > 0 {
		writeSection(&out, sectionImportID, importSection)
	}

	functionSection := encodeFunctionSection(m.Funcs)
	if len(functionSection) > 0 {
		writeSection(&out, sectionFunctionID, functionSection)
	}

	tableSection := encodeTableSection(m.Tables, &diags)
	if len(tableSection) > 0 {
		writeSection(&out, sectionTableID, tableSection)
	}

	memorySection := encodeMemorySection(m.Memories, &diags)
	if len(memorySection) > 0 {
		writeSection(&out, sectionMemoryID, memorySection)
	}

	globalSection := encodeGlobalSection(m.Globals, &diags)
	if len(globalSection) > 0 {
		writeSection(&out, sectionGlobalID, globalSection)
	}

	exportSection := encodeExportSection(m.Exports, &diags)
	if len(exportSection) > 0 {
		writeSection(&out, sectionExportID, exportSection)
	}

	elementSection := encodeElementSection(m.Elements, &diags)
	if len(elementSection) > 0 {
		writeSection(&out, sectionElementID, elementSection)
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

// encodeImportSection emits section 2 as a vector of imports.
func encodeImportSection(imports []wasmir.Import, diags *diag.ErrorList) []byte {
	if len(imports) == 0 {
		return nil
	}

	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(imports)))
	for i, imp := range imports {
		moduleName := []byte(imp.Module)
		writeULEB128(&payload, uint32(len(moduleName)))
		payload.Write(moduleName)

		name := []byte(imp.Name)
		writeULEB128(&payload, uint32(len(name)))
		payload.Write(name)

		switch imp.Kind {
		case wasmir.ExternalKindFunction:
			payload.WriteByte(importKindFunctionCode)
			writeULEB128(&payload, imp.TypeIdx)
		case wasmir.ExternalKindTable:
			payload.WriteByte(importKindTableCode)
			refCode, ok := refTypeCode(imp.Table.RefType)
			if !ok {
				diags.Addf("import[%d]: unsupported table ref type %d", i, imp.Table.RefType)
				refCode = refTypeFuncRefCode
			}
			payload.WriteByte(refCode)
			writeLimits(&payload, imp.Table.Min, imp.Table.HasMax, imp.Table.Max)
		case wasmir.ExternalKindMemory:
			payload.WriteByte(importKindMemoryCode)
			writeLimits(&payload, imp.Memory.Min, false, 0)
		case wasmir.ExternalKindGlobal:
			payload.WriteByte(importKindGlobalCode)
			vt, ok := valueTypeCode(imp.GlobalType)
			if !ok {
				diags.Addf("import[%d]: unsupported global value type %d", i, imp.GlobalType)
				vt = valueTypeI32Code
			}
			payload.WriteByte(vt)
			if imp.GlobalMutable {
				payload.WriteByte(globalMutabilityVarCode)
			} else {
				payload.WriteByte(globalMutabilityConstCode)
			}
		default:
			diags.Addf("import[%d]: unsupported kind %d", i, imp.Kind)
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

// encodeTableSection emits section 4 as a vector of defined table entries.
func encodeTableSection(tables []wasmir.Table, diags *diag.ErrorList) []byte {
	definedCount := 0
	for _, tb := range tables {
		if !tb.Imported {
			definedCount++
		}
	}
	if definedCount == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(definedCount))
	for i, tb := range tables {
		if tb.Imported {
			continue
		}
		refCode, ok := refTypeCode(tb.RefType)
		if !ok {
			diags.Addf("table[%d]: unsupported ref type %d", i, tb.RefType)
			refCode = refTypeFuncRefCode
		}
		payload.WriteByte(refCode)
		writeLimits(&payload, tb.Min, tb.HasMax, tb.Max)
	}
	return payload.Bytes()
}

// encodeMemorySection emits section 5 as a vector of memory definitions.
// This encoder currently supports min-only limits.
func encodeMemorySection(memories []wasmir.Memory, _ *diag.ErrorList) []byte {
	if len(memories) == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(memories)))
	for _, mem := range memories {
		writeLimits(&payload, mem.Min, false, 0)
	}
	return payload.Bytes()
}

// encodeGlobalSection emits section 6 as a vector of global definitions.
func encodeGlobalSection(globals []wasmir.Global, diags *diag.ErrorList) []byte {
	definedCount := 0
	for _, g := range globals {
		if !g.Imported {
			definedCount++
		}
	}
	if definedCount == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(definedCount))
	for i, g := range globals {
		if g.Imported {
			continue
		}
		vt, ok := valueTypeCode(g.Type)
		if !ok {
			diags.Addf("global[%d]: unsupported value type %d", i, g.Type)
			vt = valueTypeI32Code
		}
		payload.WriteByte(vt)
		if g.Mutable {
			payload.WriteByte(globalMutabilityVarCode)
		} else {
			payload.WriteByte(globalMutabilityConstCode)
		}
		encodeConstExpr(&payload, fmt.Sprintf("global[%d]", i), g.Init, diags)
	}
	return payload.Bytes()
}

// encodeElementSection emits section 9 as active element segments.
func encodeElementSection(elements []wasmir.ElementSegment, diags *diag.ErrorList) []byte {
	if len(elements) == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(elements)))
	for i, elem := range elements {
		if len(elem.Exprs) > 0 {
			// Active segment with explicit table index and ref-typed expr payload.
			payload.WriteByte(elemSegmentFlagActiveExplicitTableExprs)
			writeULEB128(&payload, elem.TableIndex)
			payload.WriteByte(opI32ConstCode)
			writeSLEB128(&payload, int64(elem.OffsetI32))
			payload.WriteByte(opEndCode)

			refCode, ok := refTypeCode(elem.RefType)
			if !ok {
				diags.Addf("element[%d]: unsupported expr ref type %d", i, elem.RefType)
				refCode = refTypeFuncRefCode
			}
			payload.WriteByte(refCode)

			writeULEB128(&payload, uint32(len(elem.Exprs)))
			for j, expr := range elem.Exprs {
				encodeConstExpr(&payload, fmt.Sprintf("element[%d] expr[%d]", i, j), expr, diags)
			}
			continue
		}

		// Active segment with explicit table index and function-index payload.
		payload.WriteByte(elemSegmentFlagActiveExplicitTableFuncIndices)
		writeULEB128(&payload, elem.TableIndex)
		payload.WriteByte(opI32ConstCode)
		writeSLEB128(&payload, int64(elem.OffsetI32))
		payload.WriteByte(opEndCode)
		// Legacy element kind tag for funcref function-index payloads.
		payload.WriteByte(elemKindFuncRef)
		writeULEB128(&payload, uint32(len(elem.FuncIndices)))
		for _, idx := range elem.FuncIndices {
			writeULEB128(&payload, idx)
		}
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
	case wasmir.InstrNop:
		out.WriteByte(opNopCode)
	case wasmir.InstrBlock:
		out.WriteByte(opBlockCode)
		encodeBlockType(out, funcIdx, instrIdx, "block", instr, diags)
	case wasmir.InstrLoop:
		out.WriteByte(opLoopCode)
		encodeBlockType(out, funcIdx, instrIdx, "loop", instr, diags)
	case wasmir.InstrIf:
		out.WriteByte(opIfCode)
		encodeBlockType(out, funcIdx, instrIdx, "if", instr, diags)
	case wasmir.InstrElse:
		out.WriteByte(opElseCode)
	case wasmir.InstrBr:
		out.WriteByte(opBrCode)
		writeULEB128(out, instr.BranchDepth)
	case wasmir.InstrBrIf:
		out.WriteByte(opBrIfCode)
		writeULEB128(out, instr.BranchDepth)
	case wasmir.InstrBrTable:
		out.WriteByte(opBrTableCode)
		writeULEB128(out, uint32(len(instr.BranchTable)))
		for _, depth := range instr.BranchTable {
			writeULEB128(out, depth)
		}
		writeULEB128(out, instr.BranchDefault)
	case wasmir.InstrUnreachable:
		out.WriteByte(opUnreachableCode)
	case wasmir.InstrReturn:
		out.WriteByte(opReturnCode)
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
	case wasmir.InstrSelect:
		out.WriteByte(opSelectCode)
	case wasmir.InstrLocalGet:
		out.WriteByte(opLocalGetCode)
		writeULEB128(out, instr.LocalIndex)
	case wasmir.InstrLocalSet:
		out.WriteByte(opLocalSetCode)
		writeULEB128(out, instr.LocalIndex)
	case wasmir.InstrLocalTee:
		out.WriteByte(opLocalTeeCode)
		writeULEB128(out, instr.LocalIndex)
	case wasmir.InstrGlobalGet:
		out.WriteByte(opGlobalGetCode)
		writeULEB128(out, instr.GlobalIndex)
	case wasmir.InstrGlobalSet:
		out.WriteByte(opGlobalSetCode)
		writeULEB128(out, instr.GlobalIndex)
	case wasmir.InstrTableGet:
		out.WriteByte(opTableGetCode)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrTableSet:
		out.WriteByte(opTableSetCode)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrCall:
		out.WriteByte(opCallCode)
		writeULEB128(out, instr.FuncIndex)
	case wasmir.InstrCallIndirect:
		out.WriteByte(opCallIndirectCode)
		writeULEB128(out, instr.CallTypeIndex)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrI32Load:
		out.WriteByte(opI32LoadCode)
		align := instr.MemoryAlign
		if align == 0 {
			align = 2
		}
		writeULEB128(out, align)
		writeULEB128(out, instr.MemoryOffset)
	case wasmir.InstrI32Store:
		out.WriteByte(opI32StoreCode)
		align := instr.MemoryAlign
		if align == 0 {
			align = 2
		}
		writeULEB128(out, align)
		writeULEB128(out, instr.MemoryOffset)
	case wasmir.InstrMemoryGrow:
		out.WriteByte(opMemoryGrowCode)
		writeULEB128(out, instr.MemoryIndex)
	case wasmir.InstrI32Eq:
		out.WriteByte(opI32EqCode)
	case wasmir.InstrI32Ctz:
		out.WriteByte(opI32CtzCode)
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
	case wasmir.InstrI32RemS:
		out.WriteByte(opI32RemSCode)
	case wasmir.InstrI32RemU:
		out.WriteByte(opI32RemUCode)
	case wasmir.InstrI32Shl:
		out.WriteByte(opI32ShlCode)
	case wasmir.InstrI32ShrS:
		out.WriteByte(opI32ShrSCode)
	case wasmir.InstrI32ShrU:
		out.WriteByte(opI32ShrUCode)
	case wasmir.InstrI32Eqz:
		out.WriteByte(opI32EqzCode)
	case wasmir.InstrI32LtS:
		out.WriteByte(opI32LtSCode)
	case wasmir.InstrI32LtU:
		out.WriteByte(opI32LtUCode)
	case wasmir.InstrI64Add:
		out.WriteByte(opI64AddCode)
	case wasmir.InstrI64Eq:
		out.WriteByte(opI64EqCode)
	case wasmir.InstrI64Eqz:
		out.WriteByte(opI64EqzCode)
	case wasmir.InstrI64GtS:
		out.WriteByte(opI64GtSCode)
	case wasmir.InstrI64GtU:
		out.WriteByte(opI64GtUCode)
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
	case wasmir.InstrI64RemS:
		out.WriteByte(opI64RemSCode)
	case wasmir.InstrI64RemU:
		out.WriteByte(opI64RemUCode)
	case wasmir.InstrI64Shl:
		out.WriteByte(opI64ShlCode)
	case wasmir.InstrI64ShrS:
		out.WriteByte(opI64ShrSCode)
	case wasmir.InstrI64ShrU:
		out.WriteByte(opI64ShrUCode)
	case wasmir.InstrI64LtS:
		out.WriteByte(opI64LtSCode)
	case wasmir.InstrI64LtU:
		out.WriteByte(opI64LtUCode)
	case wasmir.InstrI32WrapI64:
		out.WriteByte(opI32WrapI64Code)
	case wasmir.InstrI64ExtendI32S:
		out.WriteByte(opI64ExtendI32SCode)
	case wasmir.InstrI64ExtendI32U:
		out.WriteByte(opI64ExtendI32UCode)
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
	case wasmir.InstrF32Neg:
		out.WriteByte(opF32NegCode)
	case wasmir.InstrF32Gt:
		out.WriteByte(opF32GtCode)
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
	case wasmir.InstrF64Neg:
		out.WriteByte(opF64NegCode)
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
	case wasmir.InstrRefNull:
		out.WriteByte(opRefNullCode)
		refCode, ok := refTypeCode(instr.RefType)
		if !ok {
			diags.Addf("func[%d] instruction[%d]: unsupported ref.null type %d", funcIdx, instrIdx, instr.RefType)
			refCode = refTypeFuncRefCode
		}
		out.WriteByte(refCode)
	case wasmir.InstrRefIsNull:
		out.WriteByte(opRefIsNullCode)
	case wasmir.InstrRefFunc:
		out.WriteByte(opRefFuncCode)
		writeULEB128(out, instr.FuncIndex)
	case wasmir.InstrEnd:
		out.WriteByte(opEndCode)
	default:
		diags.Addf("func[%d] instruction[%d]: unsupported instruction kind %d", funcIdx, instrIdx, instr.Kind)
	}
}

// encodeConstExpr emits a constant expression terminated by end.
func encodeConstExpr(out *bytes.Buffer, where string, init wasmir.Instruction, diags *diag.ErrorList) {
	switch init.Kind {
	case wasmir.InstrI32Const:
		out.WriteByte(opI32ConstCode)
		writeSLEB128(out, int64(init.I32Const))
	case wasmir.InstrI64Const:
		out.WriteByte(opI64ConstCode)
		writeSLEB128(out, init.I64Const)
	case wasmir.InstrF32Const:
		out.WriteByte(opF32ConstCode)
		writeU32LE(out, init.F32Const)
	case wasmir.InstrF64Const:
		out.WriteByte(opF64ConstCode)
		writeU64LE(out, init.F64Const)
	case wasmir.InstrRefNull:
		out.WriteByte(opRefNullCode)
		refCode, ok := refTypeCode(init.RefType)
		if !ok {
			diags.Addf("%s: unsupported ref.null type %d", where, init.RefType)
			refCode = refTypeFuncRefCode
		}
		out.WriteByte(refCode)
	case wasmir.InstrRefFunc:
		out.WriteByte(opRefFuncCode)
		writeULEB128(out, init.FuncIndex)
	case wasmir.InstrGlobalGet:
		out.WriteByte(opGlobalGetCode)
		writeULEB128(out, init.GlobalIndex)
	default:
		diags.Addf("%s: unsupported initializer instruction kind %d", where, init.Kind)
		out.WriteByte(opI32ConstCode)
		writeSLEB128(out, 0)
	}
	out.WriteByte(opEndCode)
}

func encodeBlockType(out *bytes.Buffer, funcIdx int, instrIdx int, opname string, instr wasmir.Instruction, diags *diag.ErrorList) {
	if instr.BlockTypeUsesIndex {
		writeSLEB128(out, int64(instr.BlockTypeIndex))
		return
	}
	if instr.BlockHasResult {
		b, ok := valueTypeCode(instr.BlockType)
		if !ok {
			diags.Addf("func[%d] instruction[%d]: unsupported %s result type %d", funcIdx, instrIdx, opname, instr.BlockType)
			b = blockTypeEmptyCode
		}
		out.WriteByte(b)
		return
	}
	out.WriteByte(blockTypeEmptyCode)
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
	case wasmir.ValueTypeFuncRef:
		return refTypeFuncRefCode, true
	case wasmir.ValueTypeExternRef:
		return valueTypeExternRefCode, true
	default:
		return 0, false
	}
}

func exportKindCode(kind wasmir.ExternalKind) (byte, bool) {
	switch kind {
	case wasmir.ExternalKindFunction:
		return exportKindFunctionCode, true
	case wasmir.ExternalKindTable:
		return exportKindTableCode, true
	case wasmir.ExternalKindMemory:
		return exportKindMemoryCode, true
	case wasmir.ExternalKindGlobal:
		return exportKindGlobalCode, true
	default:
		return 0, false
	}
}

func refTypeCode(vt wasmir.ValueType) (byte, bool) {
	switch vt {
	case wasmir.ValueTypeFuncRef:
		return refTypeFuncRefCode, true
	case wasmir.ValueTypeExternRef:
		return refTypeExternRefCode, true
	default:
		return 0, false
	}
}

func writeLimits(out *bytes.Buffer, min uint32, hasMax bool, max uint32) {
	if hasMax {
		out.WriteByte(limitsFlagMinMax)
		writeULEB128(out, min)
		writeULEB128(out, max)
		return
	}
	out.WriteByte(limitsFlagMinOnly)
	writeULEB128(out, min)
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
