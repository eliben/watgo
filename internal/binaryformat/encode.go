package binaryformat

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

const (
	// wasmMagic is the 4-byte preamble a WASM binary starts with.
	wasmMagic = "\x00asm"
	// wasmVersion is the current WASM binary format version in little-endian.
	// Version 1 is encoded as bytes {0x01, 0x00, 0x00, 0x00}.
	wasmVersion = "\x01\x00\x00\x00"

	// Section IDs follow the core binary spec's section id table.
	sectionTypeID      byte = 1
	sectionImportID    byte = 2
	sectionFunctionID  byte = 3
	sectionTableID     byte = 4
	sectionMemoryID    byte = 5
	sectionGlobalID    byte = 6
	sectionExportID    byte = 7
	sectionStartID     byte = 8
	sectionElementID   byte = 9
	sectionCodeID      byte = 10
	sectionDataID      byte = 11
	sectionDataCountID byte = 12

	// Type section entry forms.
	typeCodeFunc     byte = 0x60
	typeCodeArray    byte = 0x5e
	typeCodeStruct   byte = 0x5f
	typeCodeSubFinal byte = 0x4f
	typeCodeSub      byte = 0x50

	valueTypeI32Code       byte = 0x7f
	valueTypeI64Code       byte = 0x7e
	valueTypeF32Code       byte = 0x7d
	valueTypeF64Code       byte = 0x7c
	valueTypeV128Code      byte = 0x7b
	packedTypeI16Code      byte = 0x77
	packedTypeI8Code       byte = 0x78
	refTypeNoFuncCode      byte = 0x73
	refTypeNoExternCode    byte = 0x72
	refTypeNoneCode        byte = 0x71
	refTypeArrayCode       byte = 0x6a
	refTypeStructCode      byte = 0x6b
	refTypeI31Code         byte = 0x6c
	refTypeEqCode          byte = 0x6d
	refTypeAnyCode         byte = 0x6e
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
	fieldImmutableCode   byte = 0x00
	fieldMutableCode     byte = 0x01

	// blockTypeEmptyCode is the no-result blocktype used by block/loop/if.
	blockTypeEmptyCode byte = 0x40

	// typeCodeRec starts a recursive type group in the GC type section.
	typeCodeRec byte = 0x4e

	// globalMutabilityConstCode marks an immutable global type.
	globalMutabilityConstCode byte = 0x00
	// globalMutabilityVarCode marks a mutable global type.
	globalMutabilityVarCode byte = 0x01

	// limitsFlagMinOnly encodes limits with only a minimum bound.
	limitsFlagMinOnly byte = 0x00
	// limitsFlagMinMax encodes limits with both minimum and maximum bounds.
	limitsFlagMinMax byte = 0x01
	// limitsFlagMinOnly64 encodes a memory64 minimum without maximum.
	limitsFlagMinOnly64 byte = 0x04
	// limitsFlagMinMax64 encodes a memory64 min/max pair.
	limitsFlagMinMax64 byte = 0x05
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

	// dataSegmentFlagActiveMem0 encodes an active data segment for memory 0.
	dataSegmentFlagActiveMem0 byte = 0x00
	// dataSegmentFlagPassive encodes a passive data segment.
	dataSegmentFlagPassive byte = 0x01
	// dataSegmentFlagActiveExplicitMemory encodes an active data segment with
	// an explicit memory index.
	dataSegmentFlagActiveExplicitMemory byte = 0x02
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

	startSection := encodeStartSection(m)
	if len(startSection) > 0 {
		writeSection(&out, sectionStartID, startSection)
	}

	elementSection := encodeElementSection(m.Elements, &diags)
	if len(elementSection) > 0 {
		writeSection(&out, sectionElementID, elementSection)
	}

	dataCountSection := encodeDataCountSection(m)
	if len(dataCountSection) > 0 {
		writeSection(&out, sectionDataCountID, dataCountSection)
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

// encodeStartSection emits section 8 when the module has a start function.
func encodeStartSection(m *wasmir.Module) []byte {
	if m == nil || m.StartFuncIndex == nil {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, *m.StartFuncIndex)
	return payload.Bytes()
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
func encodeTypeSection(types []wasmir.FuncType, diags *diag.ErrorList) []byte {
	if len(types) == 0 {
		return nil
	}

	var payload bytes.Buffer

	// Module.Types is flattened even when the source had "(rec ...)" groups.
	// The binary type section counts logical entries, so one rec group counts
	// as a single entry regardless of how many type defs it contains.
	entryCount := uint32(0)
	for i := 0; i < len(types); {
		if types[i].RecGroupSize > 0 {
			entryCount++
			i += int(types[i].RecGroupSize)
			continue
		}
		entryCount++
		i++
	}
	writeULEB128(&payload, entryCount)

	// Reassemble the flattened type list into the binary section layout. A type
	// whose RecGroupSize is non-zero starts a recursive group and emits that
	// many consecutive type defs under one rec wrapper. All other entries are
	// encoded as standalone type definitions.
	for i := 0; i < len(types); {
		ft := types[i]
		if ft.RecGroupSize > 0 {
			payload.WriteByte(typeCodeRec)
			writeULEB128(&payload, ft.RecGroupSize)
			for j := uint32(0); j < ft.RecGroupSize && i+int(j) < len(types); j++ {
				encodeOneTypeDef(&payload, i+int(j), types[i+int(j)], diags)
			}
			i += int(ft.RecGroupSize)
			continue
		}
		encodeOneTypeDef(&payload, i, ft, diags)
		i++
	}

	return payload.Bytes()
}

func encodeOneTypeDef(payload *bytes.Buffer, i int, ft wasmir.FuncType, diags *diag.ErrorList) {
	if ft.SubType {
		if ft.Final {
			payload.WriteByte(typeCodeSubFinal)
		} else {
			payload.WriteByte(typeCodeSub)
		}
		writeULEB128(payload, uint32(len(ft.SuperTypes)))
		for _, super := range ft.SuperTypes {
			writeULEB128(payload, super)
		}
	}
	switch ft.Kind {
	case wasmir.TypeDefKindFunc:
		payload.WriteByte(typeCodeFunc)

		writeULEB128(payload, uint32(len(ft.Params)))
		for j, p := range ft.Params {
			if !encodeValueType(payload, p) {
				diags.Addf("type[%d] param[%d]: unsupported value type %s", i, j, p)
				payload.WriteByte(0)
			}
		}

		writeULEB128(payload, uint32(len(ft.Results)))
		for j, r := range ft.Results {
			if !encodeValueType(payload, r) {
				diags.Addf("type[%d] result[%d]: unsupported value type %s", i, j, r)
				payload.WriteByte(0)
			}
		}
	case wasmir.TypeDefKindStruct:
		payload.WriteByte(typeCodeStruct)
		writeULEB128(payload, uint32(len(ft.Fields)))
		for j, field := range ft.Fields {
			if !encodeFieldStorageType(payload, field) {
				diags.Addf("type[%d] field[%d]: unsupported storage type", i, j)
				payload.WriteByte(0)
			}
			if field.Mutable {
				payload.WriteByte(fieldMutableCode)
			} else {
				payload.WriteByte(fieldImmutableCode)
			}
		}
	case wasmir.TypeDefKindArray:
		payload.WriteByte(typeCodeArray)
		if !encodeFieldStorageType(payload, ft.ElemField) {
			diags.Addf("type[%d] element: unsupported storage type", i)
			payload.WriteByte(0)
		}
		if ft.ElemField.Mutable {
			payload.WriteByte(fieldMutableCode)
		} else {
			payload.WriteByte(fieldImmutableCode)
		}
	default:
		diags.Addf("type[%d]: unsupported type kind %d", i, ft.Kind)
		payload.WriteByte(typeCodeFunc)
		writeULEB128(payload, 0)
		writeULEB128(payload, 0)
	}
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
			writeTableLimits(&payload, imp.Table)
		case wasmir.ExternalKindMemory:
			payload.WriteByte(importKindMemoryCode)
			writeMemoryLimits(&payload, imp.Memory)
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
		if tb.ImportModule == "" {
			definedCount++
		}
	}
	if definedCount == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(definedCount))
	for i, tb := range tables {
		if tb.ImportModule != "" {
			continue
		}
		if len(tb.Init) > 0 {
			payload.WriteByte(tableFlagHasInit)
			payload.WriteByte(0x00)
			if !encodeValueType(&payload, tb.RefType) {
				diags.Addf("table[%d]: unsupported ref type %s", i, tb.RefType)
				payload.WriteByte(refTypeFuncRefCode)
			}
			writeLimits(&payload, tb.Min, tb.Max != nil, derefUint64(tb.Max))
			encodeConstExprInstrs(&payload, fmt.Sprintf("table[%d]", i), tb.Init, diags)
			continue
		}
		if !encodeValueType(&payload, tb.RefType) {
			diags.Addf("table[%d]: unsupported ref type %s", i, tb.RefType)
			payload.WriteByte(refTypeFuncRefCode)
		}
		writeTableLimits(&payload, tb)
	}
	return payload.Bytes()
}

// encodeMemorySection emits section 5 as a vector of defined memory entries.
func encodeMemorySection(memories []wasmir.Memory, _ *diag.ErrorList) []byte {
	definedCount := 0
	for _, mem := range memories {
		if mem.ImportModule == "" {
			definedCount++
		}
	}
	if definedCount == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(definedCount))
	for _, mem := range memories {
		if mem.ImportModule != "" {
			continue
		}
		writeMemoryLimits(&payload, mem)
	}
	return payload.Bytes()
}

// encodeGlobalSection emits section 6 as a vector of global definitions.
func encodeGlobalSection(globals []wasmir.Global, diags *diag.ErrorList) []byte {
	definedCount := 0
	for _, g := range globals {
		if g.ImportModule == "" {
			definedCount++
		}
	}
	if definedCount == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(definedCount))
	for i, g := range globals {
		if g.ImportModule != "" {
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
		encodeConstExprInstrs(&payload, fmt.Sprintf("global[%d]", i), g.Init, diags)
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
				encodeElemOffsetExpr(&payload, i, elem, diags)
			}

			if !encodeValueType(&payload, elem.RefType) {
				diags.Addf("element[%d]: unsupported expr ref type %s", i, elem.RefType)
				payload.WriteByte(refTypeFuncRefCode)
			}

			writeULEB128(&payload, uint32(len(elem.Exprs)))
			for j, expr := range elem.Exprs {
				encodeConstExprInstrs(&payload, fmt.Sprintf("element[%d] expr[%d]", i, j), expr, diags)
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
			encodeElemOffsetExpr(&payload, i, elem, diags)
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

func encodeElemOffsetExpr(out *bytes.Buffer, elemIdx int, elem wasmir.ElementSegment, diags *diag.ErrorList) {
	switch elem.OffsetType {
	case wasmir.ValueTypeI32:
		writeInstructionOpcode(out, wasmir.InstrI32Const)
	case wasmir.ValueTypeI64:
		writeInstructionOpcode(out, wasmir.InstrI64Const)
	default:
		diags.Addf("element[%d]: unsupported offset type %s", elemIdx, elem.OffsetType)
		writeInstructionOpcode(out, wasmir.InstrI32Const)
	}
	writeSLEB128(out, elem.OffsetI64)
	writeInstructionOpcode(out, wasmir.InstrEnd)
}

// encodeDataSection emits section 11 as active or passive data segments.
func encodeDataSection(data []wasmir.DataSegment, diags *diag.ErrorList) []byte {
	if len(data) == 0 {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(data)))
	for i, seg := range data {
		switch seg.Mode {
		case wasmir.DataSegmentModePassive:
			payload.WriteByte(dataSegmentFlagPassive)
		case wasmir.DataSegmentModeActive:
			if seg.MemoryIndex == 0 {
				payload.WriteByte(dataSegmentFlagActiveMem0)
			} else {
				payload.WriteByte(dataSegmentFlagActiveExplicitMemory)
				writeULEB128(&payload, seg.MemoryIndex)
			}
			encodeDataOffsetExpr(&payload, i, seg, diags)
		default:
			diags.Addf("data[%d]: unsupported segment mode %d", i, seg.Mode)
			payload.WriteByte(dataSegmentFlagActiveMem0)
			encodeDataOffsetExpr(&payload, i, seg, diags)
		}
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

// encodeDataCountSection emits section 12 when code references data segments
// through bulk-memory instructions like memory.init or data.drop.
func encodeDataCountSection(m *wasmir.Module) []byte {
	if m == nil || !moduleUsesDataIndexInstructions(m) {
		return nil
	}
	var payload bytes.Buffer
	writeULEB128(&payload, uint32(len(m.Data)))
	return payload.Bytes()
}

func moduleUsesDataIndexInstructions(m *wasmir.Module) bool {
	for _, fn := range m.Funcs {
		for _, instr := range fn.Body {
			switch instr.Kind {
			case wasmir.InstrMemoryInit, wasmir.InstrDataDrop, wasmir.InstrArrayNewData, wasmir.InstrArrayInitData:
				return true
			}
		}
	}
	return false
}

// encodeMemArg writes a memory instruction immediate.
//
// In the multi-memory encoding, bit 6 in the alignment field signals that an
// explicit memory index follows before the offset. Memory index 0 uses the
// compact MVP memarg form without that extra index field.
func encodeMemArg(out *bytes.Buffer, instr wasmir.Instruction) {
	if instr.MemoryIndex == 0 {
		writeULEB128(out, instr.MemoryAlign)
		writeULEB64(out, instr.MemoryOffset)
		return
	}
	writeULEB128(out, instr.MemoryAlign+(1<<6))
	writeULEB128(out, instr.MemoryIndex)
	writeULEB64(out, instr.MemoryOffset)
}

func encodeDataOffsetExpr(out *bytes.Buffer, dataIdx int, seg wasmir.DataSegment, diags *diag.ErrorList) {
	switch seg.OffsetType {
	case wasmir.ValueTypeI32:
		writeInstructionOpcode(out, wasmir.InstrI32Const)
		writeSLEB128(out, seg.OffsetI64)
	case wasmir.ValueTypeI64:
		writeInstructionOpcode(out, wasmir.InstrI64Const)
		writeSLEB128(out, seg.OffsetI64)
	default:
		diags.Addf("data[%d]: unsupported offset type %s", dataIdx, seg.OffsetType)
		writeInstructionOpcode(out, wasmir.InstrI32Const)
		writeSLEB128(out, 0)
	}
	writeInstructionOpcode(out, wasmir.InstrEnd)
}

// encodeSimpleInstruction emits one no-immediate instruction that is described
// in wasmir's shared instruction catalog.
func encodeSimpleInstruction(out *bytes.Buffer, instr wasmir.Instruction) bool {
	if instr.Kind == wasmir.InstrSelect && instr.SelectType != nil {
		return false
	}
	def, ok := instrdef.LookupInstructionByKind(instr.Kind)
	if !ok || def.Binary.Encoding != instrdef.BinaryEncodingSimple {
		return false
	}
	if def.Binary.Prefix == 0 {
		out.WriteByte(byte(def.Binary.Opcode))
		return true
	}
	out.WriteByte(def.Binary.Prefix)
	writeULEB128(out, def.Binary.Opcode)
	return true
}

// writeInstructionOpcode emits the opcode or prefixed subopcode from wasmir's
// shared instruction catalog for instructions that still use handwritten
// immediate encoding.
func writeInstructionOpcode(out *bytes.Buffer, kind wasmir.InstrKind) bool {
	def, ok := instrdef.LookupInstructionByKind(kind)
	if !ok || def.Binary.Encoding == instrdef.BinaryEncodingNone {
		return false
	}
	if def.Binary.Prefix == 0 {
		out.WriteByte(byte(def.Binary.Opcode))
		return true
	}
	out.WriteByte(def.Binary.Prefix)
	writeULEB128(out, def.Binary.Opcode)
	return true
}

// encodeInstr maps semantic instruction kinds to binary opcodes.
func encodeInstr(out *bytes.Buffer, funcIdx int, instrIdx int, instr wasmir.Instruction, diags *diag.ErrorList) {
	if encodeSimpleInstruction(out, instr) {
		return
	}
	switch instr.Kind {
	case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf:
		writeInstructionOpcode(out, instr.Kind)
		encodeBlockType(out, funcIdx, instrIdx, instructionName(instr.Kind), instr, diags)
	case wasmir.InstrSelect:
		if instr.SelectType != nil {
			out.WriteByte(0x1c)
			writeULEB128(out, 1)
			if !encodeValueType(out, *instr.SelectType) {
				diags.Addf("func[%d] instruction[%d]: unsupported select result type %s", funcIdx, instrIdx, *instr.SelectType)
			}
			return
		}
		writeInstructionOpcode(out, instr.Kind)
	case wasmir.InstrBr, wasmir.InstrBrIf, wasmir.InstrBrOnNull, wasmir.InstrBrOnNonNull:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.BranchDepth)
	case wasmir.InstrBrOnCast, wasmir.InstrBrOnCastFail:
		writeInstructionOpcode(out, instr.Kind)
		out.WriteByte(castFlags(instr.SourceRefType, instr.RefType))
		writeULEB128(out, instr.BranchDepth)
		writeHeapTypeImmediate(out, instr.SourceRefType)
		writeHeapTypeImmediate(out, instr.RefType)
	case wasmir.InstrBrTable:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, uint32(len(instr.BranchTable)))
		for _, depth := range instr.BranchTable {
			writeULEB128(out, depth)
		}
		writeULEB128(out, instr.BranchDefault)
	case wasmir.InstrI32Const:
		writeInstructionOpcode(out, instr.Kind)
		writeSLEB128(out, int64(instr.I32Const))
	case wasmir.InstrI64Const:
		writeInstructionOpcode(out, instr.Kind)
		writeSLEB128(out, instr.I64Const)
	case wasmir.InstrF32Const:
		writeInstructionOpcode(out, instr.Kind)
		writeU32LE(out, instr.F32Const)
	case wasmir.InstrF64Const:
		writeInstructionOpcode(out, instr.Kind)
		writeU64LE(out, instr.F64Const)
	case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.LocalIndex)
	case wasmir.InstrGlobalGet, wasmir.InstrGlobalSet:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.GlobalIndex)
	case wasmir.InstrTableGet, wasmir.InstrTableSet, wasmir.InstrTableGrow, wasmir.InstrTableSize, wasmir.InstrTableFill:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrTableCopy:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TableIndex)
		writeULEB128(out, instr.SourceTableIndex)
	case wasmir.InstrTableInit:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.ElemIndex)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrElemDrop:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.ElemIndex)
	case wasmir.InstrCall:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.FuncIndex)
	case wasmir.InstrCallIndirect:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.CallTypeIndex)
		writeULEB128(out, instr.TableIndex)
	case wasmir.InstrCallRef:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.CallTypeIndex)
	case wasmir.InstrStructNew, wasmir.InstrStructNewDefault, wasmir.InstrArrayNew, wasmir.InstrArrayNewDefault:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
	case wasmir.InstrStructGet, wasmir.InstrStructGetS, wasmir.InstrStructGetU, wasmir.InstrStructSet:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
		writeULEB128(out, instr.FieldIndex)
	case wasmir.InstrArrayNewData, wasmir.InstrArrayInitData:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
		writeULEB128(out, instr.DataIndex)
	case wasmir.InstrArrayNewElem, wasmir.InstrArrayInitElem:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
		writeULEB128(out, instr.ElemIndex)
	case wasmir.InstrArrayNewFixed:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
		writeULEB128(out, instr.FixedCount)
	case wasmir.InstrArrayGet, wasmir.InstrArrayGetS, wasmir.InstrArrayGetU, wasmir.InstrArraySet, wasmir.InstrArrayFill:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
	case wasmir.InstrArrayCopy:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.TypeIndex)
		writeULEB128(out, instr.SourceTypeIndex)
	case wasmir.InstrRefTest:
		out.WriteByte(0xfb)
		if instr.RefType.Nullable {
			writeULEB128(out, 0x15)
		} else {
			writeULEB128(out, 0x14)
		}
		writeHeapTypeImmediate(out, instr.RefType)
	case wasmir.InstrRefCast:
		out.WriteByte(0xfb)
		if instr.RefType.Nullable {
			writeULEB128(out, 0x17)
		} else {
			writeULEB128(out, 0x16)
		}
		writeHeapTypeImmediate(out, instr.RefType)
	case wasmir.InstrI32Load, wasmir.InstrI64Load, wasmir.InstrF32Load, wasmir.InstrF64Load,
		wasmir.InstrV128Load, wasmir.InstrV128Load8x8S, wasmir.InstrV128Load8x8U,
		wasmir.InstrV128Load16x4S, wasmir.InstrV128Load16x4U, wasmir.InstrV128Load32x2S,
		wasmir.InstrV128Load32x2U, wasmir.InstrV128Load8Splat, wasmir.InstrV128Load16Splat,
		wasmir.InstrV128Load32Splat, wasmir.InstrV128Load64Splat,
		wasmir.InstrV128Load32Zero, wasmir.InstrV128Load64Zero,
		wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane,
		wasmir.InstrV128Store8Lane, wasmir.InstrV128Store16Lane, wasmir.InstrV128Store32Lane, wasmir.InstrV128Store64Lane,
		wasmir.InstrI32Load8S,
		wasmir.InstrI32Load8U, wasmir.InstrI32Load16S, wasmir.InstrI32Load16U,
		wasmir.InstrI64Load8S, wasmir.InstrI64Load8U, wasmir.InstrI64Load16S,
		wasmir.InstrI64Load16U, wasmir.InstrI64Load32S, wasmir.InstrI64Load32U,
		wasmir.InstrI32Store, wasmir.InstrI64Store, wasmir.InstrF32Store, wasmir.InstrF64Store,
		wasmir.InstrV128Store, wasmir.InstrI32Store8, wasmir.InstrI32Store16,
		wasmir.InstrI64Store8, wasmir.InstrI64Store16, wasmir.InstrI64Store32:
		writeInstructionOpcode(out, instr.Kind)
		encodeMemArg(out, instr)
		if instr.Kind == wasmir.InstrV128Load8Lane || instr.Kind == wasmir.InstrV128Load16Lane ||
			instr.Kind == wasmir.InstrV128Load32Lane || instr.Kind == wasmir.InstrV128Load64Lane ||
			instr.Kind == wasmir.InstrV128Store8Lane || instr.Kind == wasmir.InstrV128Store16Lane ||
			instr.Kind == wasmir.InstrV128Store32Lane || instr.Kind == wasmir.InstrV128Store64Lane {
			out.WriteByte(byte(instr.LaneIndex))
		}
	case wasmir.InstrMemorySize, wasmir.InstrMemoryGrow, wasmir.InstrMemoryFill:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.MemoryIndex)
	case wasmir.InstrMemoryCopy:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.MemoryIndex)
		writeULEB128(out, instr.SourceMemoryIndex)
	case wasmir.InstrMemoryInit:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.DataIndex)
		writeULEB128(out, instr.MemoryIndex)
	case wasmir.InstrDataDrop:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.DataIndex)
	case wasmir.InstrRefNull:
		writeInstructionOpcode(out, instr.Kind)
		if instr.RefType.UsesTypeIndex() {
			writeSLEB128(out, int64(instr.RefType.HeapType.TypeIndex))
			return
		}
		refCode, ok := refTypeCode(instr.RefType)
		if !ok {
			diags.Addf("func[%d] instruction[%d]: unsupported ref.null type %s", funcIdx, instrIdx, instr.RefType)
			refCode = refTypeFuncRefCode
		}
		out.WriteByte(refCode)
	case wasmir.InstrRefFunc:
		writeInstructionOpcode(out, instr.Kind)
		writeULEB128(out, instr.FuncIndex)
	case wasmir.InstrV128Const:
		writeInstructionOpcode(out, instr.Kind)
		out.Write(instr.V128Const[:])
	case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU, wasmir.InstrI8x16ReplaceLane,
		wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU, wasmir.InstrI16x8ReplaceLane,
		wasmir.InstrI32x4ExtractLane, wasmir.InstrI32x4ReplaceLane,
		wasmir.InstrI64x2ExtractLane, wasmir.InstrI64x2ReplaceLane,
		wasmir.InstrF32x4ExtractLane, wasmir.InstrF32x4ReplaceLane,
		wasmir.InstrF64x2ExtractLane, wasmir.InstrF64x2ReplaceLane:
		writeInstructionOpcode(out, instr.Kind)
		out.WriteByte(byte(instr.LaneIndex))
	case wasmir.InstrI8x16Shuffle:
		writeInstructionOpcode(out, instr.Kind)
		out.Write(instr.ShuffleLanes[:])
	default:
		diags.Addf("func[%d] instruction[%d]: unsupported instruction kind %d", funcIdx, instrIdx, instr.Kind)
	}
}

func encodeConstExprInstrs(out *bytes.Buffer, where string, instrs []wasmir.Instruction, diags *diag.ErrorList) {
	for _, init := range instrs {
		encodeConstExprInstr(out, where, init, diags)
	}
	writeInstructionOpcode(out, wasmir.InstrEnd)
}

func encodeConstExprInstr(out *bytes.Buffer, where string, init wasmir.Instruction, diags *diag.ErrorList) {
	switch init.Kind {
	case wasmir.InstrI32Const:
		writeInstructionOpcode(out, init.Kind)
		writeSLEB128(out, int64(init.I32Const))
	case wasmir.InstrI64Const:
		writeInstructionOpcode(out, init.Kind)
		writeSLEB128(out, init.I64Const)
	case wasmir.InstrF32Const:
		writeInstructionOpcode(out, init.Kind)
		writeU32LE(out, init.F32Const)
	case wasmir.InstrF64Const:
		writeInstructionOpcode(out, init.Kind)
		writeU64LE(out, init.F64Const)
	case wasmir.InstrV128Const:
		writeInstructionOpcode(out, init.Kind)
		out.Write(init.V128Const[:])
	case wasmir.InstrRefNull:
		writeInstructionOpcode(out, init.Kind)
		if init.RefType.UsesTypeIndex() {
			writeSLEB128(out, int64(init.RefType.HeapType.TypeIndex))
			return
		}
		refCode, ok := refTypeCode(init.RefType)
		if !ok {
			diags.Addf("%s: unsupported ref.null type %s", where, init.RefType)
			refCode = refTypeFuncRefCode
		}
		out.WriteByte(refCode)
	case wasmir.InstrRefFunc:
		writeInstructionOpcode(out, init.Kind)
		writeULEB128(out, init.FuncIndex)
	case wasmir.InstrGlobalGet:
		writeInstructionOpcode(out, init.Kind)
		writeULEB128(out, init.GlobalIndex)
	case wasmir.InstrArrayNew, wasmir.InstrStructNew, wasmir.InstrStructNewDefault, wasmir.InstrArrayNewDefault:
		writeInstructionOpcode(out, init.Kind)
		writeULEB128(out, init.TypeIndex)
	case wasmir.InstrArrayNewFixed:
		writeInstructionOpcode(out, init.Kind)
		writeULEB128(out, init.TypeIndex)
		writeULEB128(out, init.FixedCount)
	case wasmir.InstrExternConvertAny, wasmir.InstrAnyConvertExtern, wasmir.InstrRefI31:
		writeInstructionOpcode(out, init.Kind)
	default:
		diags.Addf("%s: unsupported initializer instruction kind %d", where, init.Kind)
		writeInstructionOpcode(out, wasmir.InstrI32Const)
		writeSLEB128(out, 0)
	}
}

func encodeBlockType(out *bytes.Buffer, funcIdx int, instrIdx int, opname string, instr wasmir.Instruction, diags *diag.ErrorList) {
	if instr.BlockTypeUsesIndex {
		writeSLEB128(out, int64(instr.BlockTypeIndex))
		return
	}
	if instr.BlockType != nil {
		if !encodeValueType(out, *instr.BlockType) {
			diags.Addf("func[%d] instruction[%d]: unsupported %s result type %s", funcIdx, instrIdx, opname, *instr.BlockType)
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
	case wasmir.ValueKindV128:
		return valueTypeV128Code, true
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

func encodeFieldStorageType(out *bytes.Buffer, ft wasmir.FieldType) bool {
	switch ft.Packed {
	case wasmir.PackedTypeI8:
		out.WriteByte(packedTypeI8Code)
		return true
	case wasmir.PackedTypeI16:
		out.WriteByte(packedTypeI16Code)
		return true
	default:
		return encodeValueType(out, ft.Type)
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
	if vt.Kind != wasmir.ValueKindRef {
		return 0, false
	}
	switch vt.HeapType.Kind {
	case wasmir.HeapKindNoFunc:
		return refTypeNoFuncCode, true
	case wasmir.HeapKindNoExtern:
		return refTypeNoExternCode, true
	case wasmir.HeapKindNone:
		return refTypeNoneCode, true
	case wasmir.HeapKindArray:
		return refTypeArrayCode, true
	case wasmir.HeapKindStruct:
		return refTypeStructCode, true
	case wasmir.HeapKindI31:
		return refTypeI31Code, true
	case wasmir.HeapKindEq:
		return refTypeEqCode, true
	case wasmir.HeapKindAny:
		return refTypeAnyCode, true
	case wasmir.HeapKindFunc:
		return refTypeFuncRefCode, true
	case wasmir.HeapKindExtern:
		return refTypeExternRefCode, true
	default:
		return 0, false
	}
}

func writeRefTypeImmediate(out *bytes.Buffer, vt wasmir.ValueType) {
	if vt.Nullable {
		if vt.UsesTypeIndex() {
			out.WriteByte(refNullPrefixCode)
			writeSLEB128(out, int64(vt.HeapType.TypeIndex))
			return
		}
		if b, ok := refTypeCode(vt); ok {
			out.WriteByte(b)
			return
		}
	}
	out.WriteByte(refPrefixCode)
	writeHeapTypeImmediate(out, vt)
}

func writeHeapTypeImmediate(out *bytes.Buffer, vt wasmir.ValueType) {
	if vt.UsesTypeIndex() {
		writeSLEB128(out, int64(vt.HeapType.TypeIndex))
		return
	}
	b, ok := refTypeCode(vt)
	if !ok {
		out.WriteByte(refTypeFuncRefCode)
		return
	}
	out.WriteByte(b)
}

func castFlags(src, dst wasmir.ValueType) byte {
	var flags byte
	if src.Nullable {
		flags |= 0x01
	}
	if dst.Nullable {
		flags |= 0x02
	}
	return flags
}

func writeLimits(out *bytes.Buffer, min uint64, hasMax bool, max uint64) {
	if hasMax {
		out.WriteByte(limitsFlagMinMax)
		writeULEB64(out, min)
		writeULEB64(out, max)
		return
	}
	out.WriteByte(limitsFlagMinOnly)
	writeULEB64(out, min)
}

func derefUint64(v *uint64) uint64 {
	if v == nil {
		return 0
	}
	return *v
}

func writeTableLimits(out *bytes.Buffer, table wasmir.Table) {
	if table.AddressType == wasmir.ValueTypeI64 {
		if table.Max != nil {
			out.WriteByte(limitsFlagMinMax64)
			writeULEB64(out, table.Min)
			writeULEB64(out, *table.Max)
			return
		}
		out.WriteByte(limitsFlagMinOnly64)
		writeULEB64(out, table.Min)
		return
	}
	writeLimits(out, table.Min, table.Max != nil, derefUint64(table.Max))
}

func writeMemoryLimits(out *bytes.Buffer, mem wasmir.Memory) {
	if mem.AddressType == wasmir.ValueTypeI64 {
		if mem.Max != nil {
			out.WriteByte(limitsFlagMinMax64)
			writeULEB64(out, mem.Min)
			writeULEB64(out, *mem.Max)
			return
		}
		out.WriteByte(limitsFlagMinOnly64)
		writeULEB64(out, mem.Min)
		return
	}
	if mem.Max != nil {
		out.WriteByte(limitsFlagMinMax)
		writeULEB64(out, mem.Min)
		writeULEB64(out, *mem.Max)
		return
	}
	out.WriteByte(limitsFlagMinOnly)
	writeULEB64(out, mem.Min)
}

// writeULEB128 writes v as an unsigned LEB128-encoded integer.
func writeULEB128(out *bytes.Buffer, v uint32) {
	var buf [binary.MaxVarintLen32]byte
	n := binary.PutUvarint(buf[:], uint64(v))
	out.Write(buf[:n])
}

// writeULEB64 writes v as an unsigned LEB128-encoded integer.
func writeULEB64(out *bytes.Buffer, v uint64) {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
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
