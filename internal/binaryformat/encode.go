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
	sectionDataID     byte = 11

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
	refNullPrefixCode    byte = 0x63
	refPrefixCode        byte = 0x64

	// Opcodes for the currently supported instruction subset.
	opBlockCode             byte = 0x02
	opLoopCode              byte = 0x03
	opIfCode                byte = 0x04
	opElseCode              byte = 0x05
	opBrCode                byte = 0x0c
	opBrIfCode              byte = 0x0d
	opBrTableCode           byte = 0x0e
	opUnreachableCode       byte = 0x00
	opNopCode               byte = 0x01
	opReturnCode            byte = 0x0f
	opEndCode               byte = 0x0b
	opI32ConstCode          byte = 0x41
	opI64ConstCode          byte = 0x42
	opF32ConstCode          byte = 0x43
	opF64ConstCode          byte = 0x44
	opDropCode              byte = 0x1a
	opSelectCode            byte = 0x1b
	opLocalGetCode          byte = 0x20
	opLocalSetCode          byte = 0x21
	opLocalTeeCode          byte = 0x22
	opGlobalGetCode         byte = 0x23
	opGlobalSetCode         byte = 0x24
	opTableGetCode          byte = 0x25
	opTableSetCode          byte = 0x26
	opCallCode              byte = 0x10
	opCallIndirectCode      byte = 0x11
	opCallRefCode           byte = 0x14
	opI32LoadCode           byte = 0x28
	opI64LoadCode           byte = 0x29
	opF32LoadCode           byte = 0x2a
	opF64LoadCode           byte = 0x2b
	opI32Load8SCode         byte = 0x2c
	opI32Load8UCode         byte = 0x2d
	opI32Load16SCode        byte = 0x2e
	opI32Load16UCode        byte = 0x2f
	opI64Load8SCode         byte = 0x30
	opI64Load8UCode         byte = 0x31
	opI64Load16SCode        byte = 0x32
	opI64Load16UCode        byte = 0x33
	opI64Load32SCode        byte = 0x34
	opI64Load32UCode        byte = 0x35
	opI32StoreCode          byte = 0x36
	opI64StoreCode          byte = 0x37
	opF32StoreCode          byte = 0x38
	opF64StoreCode          byte = 0x39
	opI32Store8Code         byte = 0x3a
	opI32Store16Code        byte = 0x3b
	opI64Store8Code         byte = 0x3c
	opI64Store16Code        byte = 0x3d
	opI64Store32Code        byte = 0x3e
	opMemorySizeCode        byte = 0x3f
	opMemoryGrowCode        byte = 0x40
	opI32EqzCode            byte = 0x45
	opI32EqCode             byte = 0x46
	opI32NeCode             byte = 0x47
	opI32LtSCode            byte = 0x48
	opI32LtUCode            byte = 0x49
	opI32GtSCode            byte = 0x4a
	opI32GtUCode            byte = 0x4b
	opI32LeSCode            byte = 0x4c
	opI32LeUCode            byte = 0x4d
	opI32GeSCode            byte = 0x4e
	opI32GeUCode            byte = 0x4f
	opI64EqzCode            byte = 0x50
	opI64EqCode             byte = 0x51
	opI64NeCode             byte = 0x52
	opI64LtSCode            byte = 0x53
	opI64LtUCode            byte = 0x54
	opI64GtSCode            byte = 0x55
	opI64GtUCode            byte = 0x56
	opI64LeSCode            byte = 0x57
	opI64LeUCode            byte = 0x58
	opI64GeSCode            byte = 0x59
	opI64GeUCode            byte = 0x5a
	opF32EqCode             byte = 0x5b
	opF32NeCode             byte = 0x5c
	opF32LtCode             byte = 0x5d
	opF32GtCode             byte = 0x5e
	opI32ClzCode            byte = 0x67
	opI32CtzCode            byte = 0x68
	opI32PopcntCode         byte = 0x69
	opI32AddCode            byte = 0x6a
	opI32SubCode            byte = 0x6b
	opI32MulCode            byte = 0x6c
	opI32DivSCode           byte = 0x6d
	opI32DivUCode           byte = 0x6e
	opI32RemSCode           byte = 0x6f
	opI32RemUCode           byte = 0x70
	opI32AndCode            byte = 0x71
	opI32OrCode             byte = 0x72
	opI32XorCode            byte = 0x73
	opI32ShlCode            byte = 0x74
	opI32ShrSCode           byte = 0x75
	opI32ShrUCode           byte = 0x76
	opI32RotlCode           byte = 0x77
	opI32RotrCode           byte = 0x78
	opI64AddCode            byte = 0x7c
	opI64SubCode            byte = 0x7d
	opI64MulCode            byte = 0x7e
	opI64DivSCode           byte = 0x7f
	opI64DivUCode           byte = 0x80
	opI64RemSCode           byte = 0x81
	opI64RemUCode           byte = 0x82
	opI64AndCode            byte = 0x83
	opI64OrCode             byte = 0x84
	opI64XorCode            byte = 0x85
	opI64ShlCode            byte = 0x86
	opI64ShrSCode           byte = 0x87
	opI64ShrUCode           byte = 0x88
	opI64RotlCode           byte = 0x89
	opI64RotrCode           byte = 0x8a
	opI64ClzCode            byte = 0x79
	opI64CtzCode            byte = 0x7a
	opI64PopcntCode         byte = 0x7b
	opI32WrapI64Code        byte = 0xa7
	opI64ExtendI32SCode     byte = 0xac
	opI64ExtendI32UCode     byte = 0xad
	opF32ConvertI32SCode    byte = 0xb2
	opF64ConvertI64SCode    byte = 0xb9
	opF32CeilCode           byte = 0x8d
	opF32FloorCode          byte = 0x8e
	opF32TruncCode          byte = 0x8f
	opF32NearestCode        byte = 0x90
	opF32SqrtCode           byte = 0x91
	opF32NegCode            byte = 0x8c
	opF32AddCode            byte = 0x92
	opF32SubCode            byte = 0x93
	opF32MulCode            byte = 0x94
	opF32DivCode            byte = 0x95
	opF32MinCode            byte = 0x96
	opF32MaxCode            byte = 0x97
	opF64CeilCode           byte = 0x9b
	opF64FloorCode          byte = 0x9c
	opF64TruncCode          byte = 0x9d
	opF64NearestCode        byte = 0x9e
	opF64EqCode             byte = 0x61
	opF64LeCode             byte = 0x65
	opF64SqrtCode           byte = 0x9f
	opF64NegCode            byte = 0x9a
	opF64AddCode            byte = 0xa0
	opF64SubCode            byte = 0xa1
	opF64MulCode            byte = 0xa2
	opF64DivCode            byte = 0xa3
	opF64MinCode            byte = 0xa4
	opF64MaxCode            byte = 0xa5
	opRefNullCode           byte = 0xd0
	opRefIsNullCode         byte = 0xd1
	opRefFuncCode           byte = 0xd2
	opRefAsNonNullCode      byte = 0xd4
	opBrOnNullCode          byte = 0xd5
	opBrOnNonNullCode       byte = 0xd6
	opPrefixFCCode          byte = 0xfc
	opF64ReinterpretI64Code byte = 0xbf
	opI32Extend8SCode       byte = 0xc0
	opI32Extend16SCode      byte = 0xc1
	opI64Extend8SCode       byte = 0xc2
	opI64Extend16SCode      byte = 0xc3
	opI64Extend32SCode      byte = 0xc4

	// FC-prefixed table instruction subopcodes.
	subopMemoryCopyCode uint32 = 0x0a
	subopMemoryFillCode uint32 = 0x0b
	subopTableGrowCode  uint32 = 0x0f
	subopTableSizeCode  uint32 = 0x10

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
	// tableFlagHasInit marks a table definition carrying an inline init expr.
	tableFlagHasInit byte = 0x40

	// elemSegmentFlagActiveTable0FuncIndices encodes an active element segment
	// for table 0 using function indices (legacy/table-0 form).
	elemSegmentFlagActiveTable0FuncIndices byte = 0x00
	// elemSegmentFlagActiveExplicitTableFuncIndices encodes an active element
	// segment with an explicit table index and function indices.
	elemSegmentFlagActiveExplicitTableFuncIndices byte = 0x02
	// elemSegmentFlagPassiveFuncIndices encodes a passive function-index segment.
	elemSegmentFlagPassiveFuncIndices byte = 0x01
	// elemSegmentFlagDeclarativeFuncIndices encodes a declarative function-index segment.
	elemSegmentFlagDeclarativeFuncIndices byte = 0x03
	// elemSegmentFlagActiveExplicitTableExprs encodes an active element segment
	// with an explicit table index and reference-typed const expressions.
	elemSegmentFlagActiveExplicitTableExprs byte = 0x06
	// elemSegmentFlagPassiveExprs encodes a passive ref-expression segment.
	elemSegmentFlagPassiveExprs byte = 0x05
	// elemSegmentFlagDeclarativeExprs encodes a declarative ref-expression segment.
	elemSegmentFlagDeclarativeExprs byte = 0x07

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

	dataSection := encodeDataSection(m.Data, &diags)
	if len(dataSection) > 0 {
		writeSection(&out, sectionDataID, dataSection)
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
			if !encodeValueType(&payload, p) {
				diags.Addf("type[%d] param[%d]: unsupported value type %s", i, j, p)
				payload.WriteByte(0)
			}
		}

		writeULEB128(&payload, uint32(len(ft.Results)))
		for j, r := range ft.Results {
			if !encodeValueType(&payload, r) {
				diags.Addf("type[%d] result[%d]: unsupported value type %s", i, j, r)
				payload.WriteByte(0)
			}
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
			if !encodeValueType(&payload, imp.Table.RefType) {
				diags.Addf("import[%d]: unsupported table ref type %s", i, imp.Table.RefType)
				payload.WriteByte(refTypeFuncRefCode)
			}
			writeLimits(&payload, imp.Table.Min, imp.Table.HasMax, imp.Table.Max)
		case wasmir.ExternalKindMemory:
			payload.WriteByte(importKindMemoryCode)
			writeLimits(&payload, imp.Memory.Min, imp.Memory.HasMax, imp.Memory.Max)
		case wasmir.ExternalKindGlobal:
			payload.WriteByte(importKindGlobalCode)
			if !encodeValueType(&payload, imp.GlobalType) {
				diags.Addf("import[%d]: unsupported global value type %s", i, imp.GlobalType)
				payload.WriteByte(valueTypeI32Code)
			}
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
		if tb.HasInit {
			payload.WriteByte(tableFlagHasInit)
			payload.WriteByte(0x00)
			if !encodeValueType(&payload, tb.RefType) {
				diags.Addf("table[%d]: unsupported ref type %s", i, tb.RefType)
				payload.WriteByte(refTypeFuncRefCode)
			}
			writeLimits(&payload, tb.Min, tb.HasMax, tb.Max)
			encodeConstExpr(&payload, fmt.Sprintf("table[%d]", i), tb.Init, diags)
			continue
		}
		if !encodeValueType(&payload, tb.RefType) {
			diags.Addf("table[%d]: unsupported ref type %s", i, tb.RefType)
			payload.WriteByte(refTypeFuncRefCode)
		}
		writeLimits(&payload, tb.Min, tb.HasMax, tb.Max)
	}
	return payload.Bytes()
}

// encodeMemorySection emits section 5 as a vector of defined memory entries.
func encodeMemorySection(memories []wasmir.Memory, _ *diag.ErrorList) []byte {
	definedCount := 0
	for _, mem := range memories {
		if !mem.Imported {
			definedCount++
		}
	}
	if definedCount == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(definedCount))
	for _, mem := range memories {
		if mem.Imported {
			continue
		}
		writeLimits(&payload, mem.Min, mem.HasMax, mem.Max)
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
		if !encodeValueType(&payload, g.Type) {
			diags.Addf("global[%d]: unsupported value type %s", i, g.Type)
			payload.WriteByte(valueTypeI32Code)
		}
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
			switch elem.Mode {
			case wasmir.ElemSegmentModePassive:
				payload.WriteByte(elemSegmentFlagPassiveExprs)
			case wasmir.ElemSegmentModeDeclarative:
				payload.WriteByte(elemSegmentFlagDeclarativeExprs)
			default:
				payload.WriteByte(elemSegmentFlagActiveExplicitTableExprs)
				writeULEB128(&payload, elem.TableIndex)
				payload.WriteByte(opI32ConstCode)
				writeSLEB128(&payload, int64(elem.OffsetI32))
				payload.WriteByte(opEndCode)
			}

			if !encodeValueType(&payload, elem.RefType) {
				diags.Addf("element[%d]: unsupported expr ref type %s", i, elem.RefType)
				payload.WriteByte(refTypeFuncRefCode)
			}

			writeULEB128(&payload, uint32(len(elem.Exprs)))
			for j, expr := range elem.Exprs {
				encodeConstExpr(&payload, fmt.Sprintf("element[%d] expr[%d]", i, j), expr, diags)
			}
			continue
		}

		switch elem.Mode {
		case wasmir.ElemSegmentModePassive:
			payload.WriteByte(elemSegmentFlagPassiveFuncIndices)
		case wasmir.ElemSegmentModeDeclarative:
			payload.WriteByte(elemSegmentFlagDeclarativeFuncIndices)
		default:
			// Active segment with explicit table index and function-index payload.
			payload.WriteByte(elemSegmentFlagActiveExplicitTableFuncIndices)
			writeULEB128(&payload, elem.TableIndex)
			payload.WriteByte(opI32ConstCode)
			writeSLEB128(&payload, int64(elem.OffsetI32))
			payload.WriteByte(opEndCode)
		}
		// Legacy element kind tag for function-index payloads.
		payload.WriteByte(elemKindFuncRef)
		writeULEB128(&payload, uint32(len(elem.FuncIndices)))
		for _, idx := range elem.FuncIndices {
			writeULEB128(&payload, idx)
		}
	}
	return payload.Bytes()
}

// encodeDataSection emits section 11 as active data segments for memory 0.
func encodeDataSection(data []wasmir.DataSegment, diags *diag.ErrorList) []byte {
	if len(data) == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(data)))
	for i, seg := range data {
		if seg.MemoryIndex != 0 {
			diags.Addf("data[%d]: only memory index 0 is supported", i)
			payload.WriteByte(0x00)
		} else {
			payload.WriteByte(0x00)
		}
		payload.WriteByte(opI32ConstCode)
		writeSLEB128(&payload, int64(seg.OffsetI32))
		payload.WriteByte(opEndCode)
		writeULEB128(&payload, uint32(len(seg.Init)))
		payload.Write(seg.Init)
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
			if !encodeValueType(&body, localTy) {
				diags.Addf("func[%d] local[%d]: unsupported value type %s", i, j, localTy)
				body.WriteByte(0)
			}
		}

		for j, instr := range fn.Body {
			encodeInstr(&body, i, j, instr, diags)
		}

		writeULEB128(&payload, uint32(body.Len()))
		payload.Write(body.Bytes())
	}

	return payload.Bytes()
}

// encodeMemArg writes a memory instruction immediate.
//
// In the multi-memory encoding, bit 6 in the alignment field signals that an
// explicit memory index follows before the offset. Memory index 0 uses the
// compact MVP memarg form without that extra index field.
func encodeMemArg(out *bytes.Buffer, instr wasmir.Instruction) {
	if instr.MemoryIndex == 0 {
		writeULEB128(out, instr.MemoryAlign)
		writeULEB128(out, instr.MemoryOffset)
		return
	}
	writeULEB128(out, instr.MemoryAlign+(1<<6))
	writeULEB128(out, instr.MemoryIndex)
	writeULEB128(out, instr.MemoryOffset)
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
	case wasmir.InstrBrOnNull:
		out.WriteByte(opBrOnNullCode)
		writeULEB128(out, instr.BranchDepth)
	case wasmir.InstrBrOnNonNull:
		out.WriteByte(opBrOnNonNullCode)
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
	case wasmir.InstrTableGrow:
		out.WriteByte(opPrefixFCCode)
		writeULEB128(out, subopTableGrowCode)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrTableSize:
		out.WriteByte(opPrefixFCCode)
		writeULEB128(out, subopTableSizeCode)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrCall:
		out.WriteByte(opCallCode)
		writeULEB128(out, instr.FuncIndex)
	case wasmir.InstrCallIndirect:
		out.WriteByte(opCallIndirectCode)
		writeULEB128(out, instr.CallTypeIndex)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrCallRef:
		out.WriteByte(opCallRefCode)
		writeULEB128(out, instr.CallTypeIndex)
	case wasmir.InstrI32Load:
		out.WriteByte(opI32LoadCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load:
		out.WriteByte(opI64LoadCode)
		encodeMemArg(out, instr)
	case wasmir.InstrF32Load:
		out.WriteByte(opF32LoadCode)
		encodeMemArg(out, instr)
	case wasmir.InstrF64Load:
		out.WriteByte(opF64LoadCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Load8S:
		out.WriteByte(opI32Load8SCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Load8U:
		out.WriteByte(opI32Load8UCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Load16S:
		out.WriteByte(opI32Load16SCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Load16U:
		out.WriteByte(opI32Load16UCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load8S:
		out.WriteByte(opI64Load8SCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load8U:
		out.WriteByte(opI64Load8UCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load16S:
		out.WriteByte(opI64Load16SCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load16U:
		out.WriteByte(opI64Load16UCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load32S:
		out.WriteByte(opI64Load32SCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Load32U:
		out.WriteByte(opI64Load32UCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Store:
		out.WriteByte(opI32StoreCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Store:
		out.WriteByte(opI64StoreCode)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Store8:
		out.WriteByte(opI32Store8Code)
		encodeMemArg(out, instr)
	case wasmir.InstrI32Store16:
		out.WriteByte(opI32Store16Code)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Store8:
		out.WriteByte(opI64Store8Code)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Store16:
		out.WriteByte(opI64Store16Code)
		encodeMemArg(out, instr)
	case wasmir.InstrI64Store32:
		out.WriteByte(opI64Store32Code)
		encodeMemArg(out, instr)
	case wasmir.InstrF32Store:
		out.WriteByte(opF32StoreCode)
		encodeMemArg(out, instr)
	case wasmir.InstrF64Store:
		out.WriteByte(opF64StoreCode)
		encodeMemArg(out, instr)
	case wasmir.InstrMemorySize:
		out.WriteByte(opMemorySizeCode)
		writeULEB128(out, instr.MemoryIndex)
	case wasmir.InstrMemoryGrow:
		out.WriteByte(opMemoryGrowCode)
		writeULEB128(out, instr.MemoryIndex)
	case wasmir.InstrMemoryCopy:
		out.WriteByte(opPrefixFCCode)
		writeULEB128(out, subopMemoryCopyCode)
		writeULEB128(out, instr.MemoryIndex)
		writeULEB128(out, instr.SourceMemoryIndex)
	case wasmir.InstrMemoryFill:
		out.WriteByte(opPrefixFCCode)
		writeULEB128(out, subopMemoryFillCode)
		writeULEB128(out, instr.MemoryIndex)
	case wasmir.InstrI32Eq:
		out.WriteByte(opI32EqCode)
	case wasmir.InstrI32Ne:
		out.WriteByte(opI32NeCode)
	case wasmir.InstrI32GtS:
		out.WriteByte(opI32GtSCode)
	case wasmir.InstrI32GtU:
		out.WriteByte(opI32GtUCode)
	case wasmir.InstrI32GeS:
		out.WriteByte(opI32GeSCode)
	case wasmir.InstrI32Clz:
		out.WriteByte(opI32ClzCode)
	case wasmir.InstrI32Ctz:
		out.WriteByte(opI32CtzCode)
	case wasmir.InstrI32Popcnt:
		out.WriteByte(opI32PopcntCode)
	case wasmir.InstrI32Add:
		out.WriteByte(opI32AddCode)
	case wasmir.InstrI32Sub:
		out.WriteByte(opI32SubCode)
	case wasmir.InstrI32Mul:
		out.WriteByte(opI32MulCode)
	case wasmir.InstrI32Or:
		out.WriteByte(opI32OrCode)
	case wasmir.InstrI32Xor:
		out.WriteByte(opI32XorCode)
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
	case wasmir.InstrI32Rotl:
		out.WriteByte(opI32RotlCode)
	case wasmir.InstrI32Rotr:
		out.WriteByte(opI32RotrCode)
	case wasmir.InstrI32Eqz:
		out.WriteByte(opI32EqzCode)
	case wasmir.InstrI32LtS:
		out.WriteByte(opI32LtSCode)
	case wasmir.InstrI32LtU:
		out.WriteByte(opI32LtUCode)
	case wasmir.InstrI32LeS:
		out.WriteByte(opI32LeSCode)
	case wasmir.InstrI32LeU:
		out.WriteByte(opI32LeUCode)
	case wasmir.InstrI32GeU:
		out.WriteByte(opI32GeUCode)
	case wasmir.InstrI32And:
		out.WriteByte(opI32AndCode)
	case wasmir.InstrI32Extend8S:
		out.WriteByte(opI32Extend8SCode)
	case wasmir.InstrI32Extend16S:
		out.WriteByte(opI32Extend16SCode)
	case wasmir.InstrI64Add:
		out.WriteByte(opI64AddCode)
	case wasmir.InstrI64And:
		out.WriteByte(opI64AndCode)
	case wasmir.InstrI64Or:
		out.WriteByte(opI64OrCode)
	case wasmir.InstrI64Xor:
		out.WriteByte(opI64XorCode)
	case wasmir.InstrI64Eq:
		out.WriteByte(opI64EqCode)
	case wasmir.InstrI64Ne:
		out.WriteByte(opI64NeCode)
	case wasmir.InstrI64Eqz:
		out.WriteByte(opI64EqzCode)
	case wasmir.InstrI64GtS:
		out.WriteByte(opI64GtSCode)
	case wasmir.InstrI64GtU:
		out.WriteByte(opI64GtUCode)
	case wasmir.InstrI64GeS:
		out.WriteByte(opI64GeSCode)
	case wasmir.InstrI64GeU:
		out.WriteByte(opI64GeUCode)
	case wasmir.InstrI64LeS:
		out.WriteByte(opI64LeSCode)
	case wasmir.InstrI64LeU:
		out.WriteByte(opI64LeUCode)
	case wasmir.InstrI64Clz:
		out.WriteByte(opI64ClzCode)
	case wasmir.InstrI64Ctz:
		out.WriteByte(opI64CtzCode)
	case wasmir.InstrI64Popcnt:
		out.WriteByte(opI64PopcntCode)
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
	case wasmir.InstrI64Rotl:
		out.WriteByte(opI64RotlCode)
	case wasmir.InstrI64Rotr:
		out.WriteByte(opI64RotrCode)
	case wasmir.InstrI64LtS:
		out.WriteByte(opI64LtSCode)
	case wasmir.InstrI64LtU:
		out.WriteByte(opI64LtUCode)
	case wasmir.InstrI64Extend8S:
		out.WriteByte(opI64Extend8SCode)
	case wasmir.InstrI64Extend16S:
		out.WriteByte(opI64Extend16SCode)
	case wasmir.InstrI64Extend32S:
		out.WriteByte(opI64Extend32SCode)
	case wasmir.InstrI32WrapI64:
		out.WriteByte(opI32WrapI64Code)
	case wasmir.InstrI64ExtendI32S:
		out.WriteByte(opI64ExtendI32SCode)
	case wasmir.InstrI64ExtendI32U:
		out.WriteByte(opI64ExtendI32UCode)
	case wasmir.InstrF32ConvertI32S:
		out.WriteByte(opF32ConvertI32SCode)
	case wasmir.InstrF64ConvertI64S:
		out.WriteByte(opF64ConvertI64SCode)
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
	case wasmir.InstrF32Eq:
		out.WriteByte(opF32EqCode)
	case wasmir.InstrF32Lt:
		out.WriteByte(opF32LtCode)
	case wasmir.InstrF32Gt:
		out.WriteByte(opF32GtCode)
	case wasmir.InstrF32Ne:
		out.WriteByte(opF32NeCode)
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
	case wasmir.InstrF64Eq:
		out.WriteByte(opF64EqCode)
	case wasmir.InstrF64Le:
		out.WriteByte(opF64LeCode)
	case wasmir.InstrF64ReinterpretI64:
		out.WriteByte(opF64ReinterpretI64Code)
	case wasmir.InstrRefNull:
		out.WriteByte(opRefNullCode)
		if instr.RefType.UsesTypeIndex() {
			writeSLEB128(out, int64(instr.RefType.HeapType.TypeIndex))
			break
		}
		refCode, ok := refTypeCode(instr.RefType)
		if !ok {
			diags.Addf("func[%d] instruction[%d]: unsupported ref.null type %s", funcIdx, instrIdx, instr.RefType)
			refCode = refTypeFuncRefCode
		}
		out.WriteByte(refCode)
	case wasmir.InstrRefIsNull:
		out.WriteByte(opRefIsNullCode)
	case wasmir.InstrRefAsNonNull:
		out.WriteByte(opRefAsNonNullCode)
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
		if init.RefType.UsesTypeIndex() {
			writeSLEB128(out, int64(init.RefType.HeapType.TypeIndex))
			break
		}
		refCode, ok := refTypeCode(init.RefType)
		if !ok {
			diags.Addf("%s: unsupported ref.null type %s", where, init.RefType)
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
		if !encodeValueType(out, instr.BlockType) {
			diags.Addf("func[%d] instruction[%d]: unsupported %s result type %s", funcIdx, instrIdx, opname, instr.BlockType)
			out.WriteByte(blockTypeEmptyCode)
		}
		return
	}
	out.WriteByte(blockTypeEmptyCode)
}

func valueTypeCode(vt wasmir.ValueType) (byte, bool) {
	switch vt.Kind {
	case wasmir.ValueKindI32:
		return valueTypeI32Code, true
	case wasmir.ValueKindI64:
		return valueTypeI64Code, true
	case wasmir.ValueKindF32:
		return valueTypeF32Code, true
	case wasmir.ValueKindF64:
		return valueTypeF64Code, true
	case wasmir.ValueKindRef:
		if !vt.Nullable {
			return 0, false
		}
		switch vt.HeapType.Kind {
		case wasmir.HeapKindFunc:
			return refTypeFuncRefCode, true
		case wasmir.HeapKindExtern:
			return valueTypeExternRefCode, true
		default:
			return 0, false
		}
	default:
		return 0, false
	}
}

func encodeValueType(out *bytes.Buffer, vt wasmir.ValueType) bool {
	if vt.IsRef() {
		if vt.UsesTypeIndex() {
			if vt.Nullable {
				out.WriteByte(refNullPrefixCode)
			} else {
				out.WriteByte(refPrefixCode)
			}
			writeSLEB128(out, int64(vt.HeapType.TypeIndex))
			return true
		}
		if vt.Nullable {
			b, ok := refTypeCode(vt)
			if !ok {
				return false
			}
			out.WriteByte(b)
			return true
		}
		out.WriteByte(refPrefixCode)
		b, ok := refTypeCode(vt)
		if !ok {
			return false
		}
		out.WriteByte(b)
		return true
	}
	b, ok := valueTypeCode(vt)
	if !ok {
		return false
	}
	out.WriteByte(b)
	return true
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
	if vt.Kind != wasmir.ValueKindRef {
		return 0, false
	}
	switch vt.HeapType.Kind {
	case wasmir.HeapKindFunc:
		return refTypeFuncRefCode, true
	case wasmir.HeapKindExtern:
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
