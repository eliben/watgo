package binaryformat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

// maxDecodedLocals is an implementation cap for eager local expansion during
// binary decode. The Wasm binary format allows up to 2^32-1 locals in a
// function body, but watgo materializes locals as a flat slice of value types,
// so decoding stops well before that point to avoid excessive allocation from
// malformed or hostile inputs.
const maxDecodedLocals = 1 << 20

type decodedFuncBody struct {
	locals []wasmir.ValueType
	body   []wasmir.Instruction
}

// DecodeModule decodes a WASM binary module into semantic IR.
// It returns a decoded module and nil error on success. On failure, it returns
// a (possibly partial) module and a diag.ErrorList.
func DecodeModule(bin []byte) (*wasmir.Module, error) {
	out := &wasmir.Module{}
	var diags diag.ErrorList

	r := bytes.NewReader(bin)
	decodePreamble(r, &diags)

	var funcTypeIdxs []uint32
	var funcBodies []decodedFuncBody
	expectedDataCount := -1

	seenType := false
	seenImport := false
	seenFunction := false
	seenTable := false
	seenMemory := false
	seenTag := false
	seenGlobal := false
	seenExport := false
	seenStart := false
	seenElement := false
	seenCode := false
	seenData := false
	lastSectionOrder := 0

	for !atEOF(r) {
		sectionID, err := readByte(r)
		if err != nil {
			diags.Addf("failed to read section id: %v", err)
			break
		}

		sectionSize, err := readU32(r)
		if err != nil {
			diags.Addf("failed to read section %d size: %v", sectionID, err)
			break
		}
		sectionPayload, err := readN(r, int(sectionSize))
		if err != nil {
			diags.Addf("failed to read section %d payload: %v", sectionID, err)
			break
		}
		sr := bytes.NewReader(sectionPayload)

		// Non-custom sections must appear in monotonically increasing standard
		// order and may not repeat. Custom sections (id 0) are exempt and may
		// appear anywhere, so they intentionally do not participate in this
		// ordering check.
		if sectionID != 0 {
			order := sectionOrderRank(sectionID)
			if order == 0 {
				diags.Addf("unsupported section id %d", sectionID)
			} else if order < lastSectionOrder {
				diags.Addf("unexpected content after last section")
			} else {
				lastSectionOrder = order
			}
		}

		switch sectionID {
		case 0:
			decodeCustomSection(sr, &diags)
		case sectionTypeID:
			if seenType {
				diags.Addf("duplicate type section")
				break
			}
			seenType = true
			out.Types = decodeTypeSection(sr, &diags)

		case sectionImportID:
			if seenImport {
				diags.Addf("duplicate import section")
				break
			}
			seenImport = true
			imports := decodeImportSection(sr, &diags)
			out.Imports = append(out.Imports, imports...)
			for _, imp := range imports {
				switch imp.Kind {
				case wasmir.ExternalKindTable:
					out.Tables = append(out.Tables, imp.Table)
				case wasmir.ExternalKindMemory:
					out.Memories = append(out.Memories, imp.Memory)
				case wasmir.ExternalKindGlobal:
					out.Globals = append(out.Globals, wasmir.Global{
						Type:         imp.GlobalType,
						Mutable:      imp.GlobalMutable,
						ImportModule: imp.Module,
						ImportName:   imp.Name,
					})
				}
			}

		case sectionFunctionID:
			if seenFunction {
				diags.Addf("duplicate function section")
				break
			}
			seenFunction = true
			funcTypeIdxs = decodeFunctionSection(sr, &diags)

		case sectionTableID:
			if seenTable {
				diags.Addf("duplicate table section")
				break
			}
			seenTable = true
			out.Tables = append(out.Tables, decodeTableSection(sr, &diags)...)

		case sectionMemoryID:
			if seenMemory {
				diags.Addf("duplicate memory section")
				break
			}
			seenMemory = true
			out.Memories = append(out.Memories, decodeMemorySection(sr, &diags)...)

		case sectionTagID:
			if seenTag {
				diags.Addf("duplicate tag section")
				break
			}
			seenTag = true
			out.Tags = append(out.Tags, decodeTagSection(sr, &diags)...)

		case sectionGlobalID:
			if seenGlobal {
				diags.Addf("duplicate global section")
				break
			}
			seenGlobal = true
			out.Globals = append(out.Globals, decodeGlobalSection(sr, &diags)...)

		case sectionExportID:
			if seenExport {
				diags.Addf("duplicate export section")
				break
			}
			seenExport = true
			out.Exports = decodeExportSection(sr, &diags)

		case sectionStartID:
			if seenStart {
				diags.Addf("duplicate start section")
				break
			}
			seenStart = true
			startIndex, err := readU32(sr)
			if err != nil {
				diags.Addf("start section: invalid function index: %v", err)
				break
			}
			out.StartFuncIndex = &startIndex

		case sectionElementID:
			if seenElement {
				diags.Addf("duplicate element section")
				break
			}
			seenElement = true
			out.Elements = decodeElementSection(sr, &diags)

		case sectionCodeID:
			if seenCode {
				diags.Addf("duplicate code section")
				break
			}
			seenCode = true
			funcBodies = decodeCodeSection(sr, &diags)
		case sectionDataID:
			if seenData {
				diags.Addf("duplicate data section")
				break
			}
			seenData = true
			out.Data = decodeDataSection(sr, &diags)
		case sectionDataCountID:
			if expectedDataCount >= 0 {
				diags.Addf("duplicate data count section")
				break
			}
			count, err := readU32(sr)
			if err != nil {
				diags.Addf("data count section: invalid count: %v", err)
				break
			}
			expectedDataCount = int(count)

		default:
			diags.Addf("unsupported section id %d", sectionID)
		}

		if !atEOF(sr) {
			diags.Addf("section %d was not fully consumed", sectionID)
		}
	}

	if len(funcTypeIdxs) != len(funcBodies) {
		diags.Addf("function/code count mismatch: %d type indices vs %d code entries", len(funcTypeIdxs), len(funcBodies))
	}

	pairCount := min(len(funcTypeIdxs), len(funcBodies))
	for i := 0; i < pairCount; i++ {
		out.Funcs = append(out.Funcs, wasmir.Function{
			TypeIdx: funcTypeIdxs[i],
			Locals:  funcBodies[i].locals,
			Body:    funcBodies[i].body,
		})
	}

	if expectedDataCount < 0 && moduleUsesBulkMemoryData(out.Funcs) {
		diags.Addf("data count section required")
	}

	if expectedDataCount >= 0 && expectedDataCount != len(out.Data) {
		diags.Addf("data count mismatch: section says %d, data section has %d segments", expectedDataCount, len(out.Data))
	}

	if diags.HasAny() {
		return out, diags
	}
	return out, nil
}

func decodeCustomSection(r *bytes.Reader, diags *diag.ErrorList) {
	nameLen, err := readU32(r)
	if err != nil {
		diags.Addf("custom section: invalid name length: %v", err)
		return
	}
	name, err := readN(r, int(nameLen))
	if err != nil {
		diags.Addf("custom section: invalid name: %v", err)
		return
	}
	if !utf8.Valid(name) {
		diags.Addf("custom section: malformed UTF-8 in name")
	}
}

func sectionOrderRank(sectionID byte) int {
	// This is the semantic section order from the binary module grammar, not
	// raw section-id order.
	// Spec reference for permissible section order:
	// https://webassembly.github.io/spec/core/binary/modules.html#binary-module
	switch sectionID {
	case sectionTypeID:
		return 1
	case sectionImportID:
		return 2
	case sectionFunctionID:
		return 3
	case sectionTableID:
		return 4
	case sectionMemoryID:
		return 5
	case sectionTagID:
		return 6
	case sectionGlobalID:
		return 7
	case sectionExportID:
		return 8
	case sectionStartID:
		return 9
	case sectionElementID:
		return 10
	case sectionDataCountID:
		return 11
	case sectionCodeID:
		return 12
	case sectionDataID:
		return 13
	default:
		return 0
	}
}

func moduleUsesBulkMemoryData(funcs []wasmir.Function) bool {
	for _, fn := range funcs {
		for _, ins := range fn.Body {
			if ins.Kind == wasmir.InstrMemoryInit || ins.Kind == wasmir.InstrDataDrop {
				return true
			}
		}
	}
	return false
}

func decodePreamble(r *bytes.Reader, diags *diag.ErrorList) {
	magic, err := readN(r, len(wasmMagic))
	if err != nil {
		diags.Addf("failed to read wasm magic: %v", err)
		return
	}
	if string(magic) != wasmMagic {
		diags.Addf("bad wasm magic: got %x", magic)
		return
	}

	version, err := readN(r, len(wasmVersion))
	if err != nil {
		diags.Addf("failed to read wasm version: %v", err)
		return
	}
	if string(version) != wasmVersion {
		diags.Addf("unsupported wasm version: got %x", version)
	}
}

func decodeTypeSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.FuncType {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("type section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("type section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.FuncType, 0, capN)
	for i := uint32(0); i < n; i++ {
		form, err := readByte(r)
		if err != nil {
			diags.Addf("type[%d]: failed to read form: %v", i, err)
			break
		}
		switch form {
		case typeCodeRec:
			groupLen, err := readU32(r)
			if err != nil {
				diags.Addf("type[%d]: invalid rec group length: %v", i, err)
				break
			}
			groupStart := len(out)
			for j := uint32(0); j < groupLen; j++ {
				typeDef, ok := decodeOneTypeDef(r, groupStart+int(j), diags)
				if !ok {
					break
				}
				out = append(out, typeDef)
			}
			if groupLen > 0 && groupStart < len(out) {
				out[groupStart].RecGroupSize = groupLen
			}
		case typeCodeFunc, typeCodeStruct, typeCodeArray, typeCodeSubFinal, typeCodeSub:
			if err := r.UnreadByte(); err != nil {
				diags.Addf("type[%d]: failed to unread form: %v", i, err)
				break
			}
			typeDef, ok := decodeOneTypeDef(r, len(out), diags)
			if !ok {
				break
			}
			out = append(out, typeDef)
		default:
			diags.Addf("type[%d]: unsupported type form 0x%x", i, form)
			break
		}
	}
	return out
}

func decodeOneTypeDef(r *bytes.Reader, index int, diags *diag.ErrorList) (wasmir.FuncType, bool) {
	form, err := readByte(r)
	if err != nil {
		diags.Addf("type[%d]: failed to read form: %v", index, err)
		return wasmir.FuncType{}, false
	}
	typeDef := wasmir.FuncType{}
	if form == typeCodeSub || form == typeCodeSubFinal {
		typeDef.SubType = true
		typeDef.Final = form == typeCodeSubFinal
		superCount, err := readU32(r)
		if err != nil {
			diags.Addf("type[%d]: invalid supertype count: %v", index, err)
			return wasmir.FuncType{}, false
		}
		typeDef.SuperTypes = make([]uint32, 0, superCount)
		for j := uint32(0); j < superCount; j++ {
			superIndex, err := readU32(r)
			if err != nil {
				diags.Addf("type[%d] super[%d]: invalid type index: %v", index, j, err)
				return wasmir.FuncType{}, false
			}
			typeDef.SuperTypes = append(typeDef.SuperTypes, superIndex)
		}
		form, err = readByte(r)
		if err != nil {
			diags.Addf("type[%d]: failed to read subtype body: %v", index, err)
			return wasmir.FuncType{}, false
		}
	}
	switch form {
	case typeCodeFunc:
		params := decodeValueTypeVec(r, fmt.Sprintf("type[%d] params", index), diags)
		results := decodeValueTypeVec(r, fmt.Sprintf("type[%d] results", index), diags)
		typeDef.Kind = wasmir.TypeDefKindFunc
		typeDef.Params = params
		typeDef.Results = results
		return typeDef, true
	case typeCodeStruct:
		fieldCount, err := readU32(r)
		if err != nil {
			diags.Addf("type[%d]: invalid struct field count: %v", index, err)
			return wasmir.FuncType{}, false
		}
		fields := make([]wasmir.FieldType, 0, fieldCount)
		for j := uint32(0); j < fieldCount; j++ {
			field, err := decodeFieldType(r)
			if err != nil {
				diags.Addf("type[%d] field[%d]: %v", index, j, err)
				return wasmir.FuncType{}, false
			}
			fields = append(fields, field)
		}
		typeDef.Kind = wasmir.TypeDefKindStruct
		typeDef.Fields = fields
		return typeDef, true
	case typeCodeArray:
		field, err := decodeFieldType(r)
		if err != nil {
			diags.Addf("type[%d] element: %v", index, err)
			return wasmir.FuncType{}, false
		}
		typeDef.Kind = wasmir.TypeDefKindArray
		typeDef.ElemField = field
		return typeDef, true
	default:
		diags.Addf("type[%d]: unsupported type form 0x%x", index, form)
		return wasmir.FuncType{}, false
	}
}

func decodeFieldType(r *bytes.Reader) (wasmir.FieldType, error) {
	b, err := readByte(r)
	if err != nil {
		return wasmir.FieldType{}, err
	}
	field := wasmir.FieldType{}
	switch b {
	case packedTypeI8Code:
		field.Packed = wasmir.PackedTypeI8
	case packedTypeI16Code:
		field.Packed = wasmir.PackedTypeI16
	default:
		vt, err := decodeValueTypeFromLeadingByte(r, b)
		if err != nil {
			return wasmir.FieldType{}, err
		}
		field.Type = vt
	}
	mut, err := readByte(r)
	if err != nil {
		return wasmir.FieldType{}, err
	}
	switch mut {
	case fieldImmutableCode:
		field.Mutable = false
	case fieldMutableCode:
		field.Mutable = true
	default:
		return wasmir.FieldType{}, fmt.Errorf("invalid mutability 0x%x", mut)
	}
	return field, nil
}

func decodeImportSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Import {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("import section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("import section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.Import, 0, capN)
	for i := uint32(0); i < n; i++ {
		moduleName, err := readName(r)
		if err != nil {
			diags.Addf("import[%d]: invalid module name: %v", i, err)
			break
		}
		name, err := readName(r)
		if err != nil {
			diags.Addf("import[%d]: invalid name: %v", i, err)
			break
		}
		kind, err := readByte(r)
		if err != nil {
			diags.Addf("import[%d]: missing kind: %v", i, err)
			break
		}
		imp := wasmir.Import{Module: moduleName, Name: name}
		switch kind {
		case importKindFunctionCode:
			typeIdx, err := readU32(r)
			if err != nil {
				diags.Addf("import[%d]: invalid function type index: %v", i, err)
				break
			}
			imp.Kind = wasmir.ExternalKindFunction
			imp.TypeIdx = typeIdx
		case importKindTableCode:
			refType, err := decodeRefTypeFromReader(r)
			if err != nil {
				diags.Addf("import[%d]: invalid table ref type: %v", i, err)
				break
			}
			addrType, min, hasMax, max, err := decodeTableLimits(r)
			if err != nil {
				diags.Addf("import[%d]: invalid table limits: %v", i, err)
				break
			}
			imp.Kind = wasmir.ExternalKindTable
			table := wasmir.Table{
				AddressType:  addrType,
				Min:          min,
				RefType:      refType,
				ImportModule: moduleName,
				ImportName:   name,
			}
			if hasMax {
				table.Max = &max
			}
			imp.Table = table
		case importKindMemoryCode:
			addrType, min, hasMax, max, err := decodeMemoryLimits(r)
			if err != nil {
				diags.Addf("import[%d]: invalid memory limits: %v", i, err)
				break
			}
			imp.Kind = wasmir.ExternalKindMemory
			mem := wasmir.Memory{
				AddressType:  addrType,
				Min:          min,
				ImportModule: moduleName,
				ImportName:   name,
			}
			if hasMax {
				mem.Max = &max
			}
			imp.Memory = mem
		case importKindGlobalCode:
			ty, err := decodeValueTypeFromReader(r)
			if err != nil {
				diags.Addf("import[%d]: invalid global value type: %v", i, err)
				break
			}
			mut, err := readByte(r)
			if err != nil {
				diags.Addf("import[%d]: missing global mutability: %v", i, err)
				break
			}
			if mut != globalMutabilityConstCode && mut != globalMutabilityVarCode {
				diags.Addf("import[%d]: unsupported global mutability 0x%x", i, mut)
				break
			}
			imp.Kind = wasmir.ExternalKindGlobal
			imp.GlobalType = ty
			imp.GlobalMutable = mut == globalMutabilityVarCode
		case importKindTagCode:
			attr, err := readByte(r)
			if err != nil {
				diags.Addf("import[%d]: missing tag attribute: %v", i, err)
				break
			}
			if attr != tagAttributeException {
				diags.Addf("import[%d]: unsupported tag attribute 0x%x", i, attr)
				break
			}
			typeIdx, err := readU32(r)
			if err != nil {
				diags.Addf("import[%d]: invalid tag type index: %v", i, err)
				break
			}
			imp.Kind = wasmir.ExternalKindTag
			imp.TypeIdx = typeIdx
		default:
			diags.Addf("import[%d]: unsupported kind 0x%x", i, kind)
			break
		}
		out = append(out, imp)
	}
	return out
}

func decodeTagSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Tag {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("tag section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("tag section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.Tag, 0, capN)
	for i := uint32(0); i < n; i++ {
		attr, err := readByte(r)
		if err != nil {
			diags.Addf("tag[%d]: missing attribute: %v", i, err)
			break
		}
		if attr != tagAttributeException {
			diags.Addf("tag[%d]: unsupported attribute 0x%x", i, attr)
			break
		}
		typeIdx, err := readU32(r)
		if err != nil {
			diags.Addf("tag[%d]: invalid type index: %v", i, err)
			break
		}
		out = append(out, wasmir.Tag{TypeIdx: typeIdx})
	}
	return out
}

func decodeFunctionSection(r *bytes.Reader, diags *diag.ErrorList) []uint32 {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("function section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("function section: invalid vector length: %v", err)
		return nil
	}
	out := make([]uint32, 0, capN)
	for i := uint32(0); i < n; i++ {
		typeIdx, err := readU32(r)
		if err != nil {
			diags.Addf("function[%d]: invalid type index: %v", i, err)
			break
		}
		out = append(out, typeIdx)
	}
	return out
}

func decodeTableSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Table {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("table section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("table section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.Table, 0, capN)
	for i := uint32(0); i < n; i++ {
		first, err := readByte(r)
		if err != nil {
			diags.Addf("table[%d]: missing table type: %v", i, err)
			break
		}
		if first == tableFlagHasInit {
			reserved, err := readByte(r)
			if err != nil {
				diags.Addf("table[%d]: missing init table reserved byte: %v", i, err)
				break
			}
			if reserved != 0x00 {
				diags.Addf("table[%d]: unsupported init table reserved byte 0x%x", i, reserved)
				break
			}
			refType, err := decodeRefTypeFromReader(r)
			if err != nil {
				diags.Addf("table[%d]: invalid ref type: %v", i, err)
				break
			}
			addrType, min, hasMax, max, err := decodeTableLimits(r)
			if err != nil {
				diags.Addf("table[%d]: invalid limits: %v", i, err)
				break
			}
			init, err := decodeConstExprInstrs(r)
			if err != nil {
				diags.Addf("table[%d]: invalid init expr: %v", i, err)
				break
			}
			table := wasmir.Table{AddressType: addrType, Min: min, RefType: refType, Init: init}
			if hasMax {
				table.Max = &max
			}
			out = append(out, table)
			continue
		}
		refType, err := decodeValueTypeFromLeadingByte(r, first)
		if err != nil || !refType.IsRef() {
			if err == nil {
				err = fmt.Errorf("expected reference type, got %s", refType)
			}
			diags.Addf("table[%d]: invalid ref type: %v", i, err)
			break
		}
		addrType, min, hasMax, max, err := decodeTableLimits(r)
		if err != nil {
			diags.Addf("table[%d]: invalid limits: %v", i, err)
			break
		}
		table := wasmir.Table{AddressType: addrType, Min: min, RefType: refType}
		if hasMax {
			table.Max = &max
		}
		out = append(out, table)
	}
	return out
}

func decodeMemorySection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Memory {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("memory section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("memory section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.Memory, 0, capN)
	for i := uint32(0); i < n; i++ {
		addrType, min, hasMax, max, err := decodeMemoryLimits(r)
		if err != nil {
			diags.Addf("memory[%d]: invalid limits: %v", i, err)
			break
		}
		mem := wasmir.Memory{AddressType: addrType, Min: min}
		if hasMax {
			mem.Max = &max
		}
		out = append(out, mem)
	}
	return out
}

func decodeGlobalSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Global {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("global section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("global section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.Global, 0, capN)
	for i := uint32(0); i < n; i++ {
		ty, err := decodeValueTypeFromReader(r)
		if err != nil {
			diags.Addf("global[%d]: invalid value type: %v", i, err)
			break
		}
		mut, err := readByte(r)
		if err != nil {
			diags.Addf("global[%d]: missing mutability: %v", i, err)
			break
		}
		if mut != globalMutabilityConstCode && mut != globalMutabilityVarCode {
			diags.Addf("global[%d]: unsupported mutability 0x%x", i, mut)
			break
		}
		init, err := decodeConstExprInstrs(r)
		if err != nil {
			diags.Addf("global[%d]: invalid initializer: %v", i, err)
			break
		}
		out = append(out, wasmir.Global{
			Type:    ty,
			Mutable: mut == globalMutabilityVarCode,
			Init:    init,
		})
	}
	return out
}

func decodeElementSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.ElementSegment {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("element section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("element section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.ElementSegment, 0, capN)
	for i := uint32(0); i < n; i++ {
		flags, err := readByte(r)
		if err != nil {
			diags.Addf("element[%d]: missing flags: %v", i, err)
			break
		}
		switch flags {
		case elemSegmentFlagActiveTable0FuncIndices:
			offsetExpr, err := decodeConstExprInstrs(r)
			if err != nil {
				diags.Addf("element[%d]: invalid offset expr: %v", i, err)
				break
			}
			funcIndices := decodeElemFuncIndices(r, i, diags)
			out = append(out, wasmir.ElementSegment{
				Mode:        wasmir.ElemSegmentModeActive,
				TableIndex:  0,
				OffsetExpr:  offsetExpr,
				FuncIndices: funcIndices,
			})
		case elemSegmentFlagPassiveFuncIndices:
			elemKind, err := readByte(r)
			if err != nil {
				diags.Addf("element[%d]: missing elemkind: %v", i, err)
				break
			}
			if elemKind != elemKindFuncRef {
				diags.Addf("element[%d]: unsupported elemkind 0x%x", i, elemKind)
				break
			}
			funcIndices := decodeElemFuncIndices(r, i, diags)
			out = append(out, wasmir.ElementSegment{
				Mode:        wasmir.ElemSegmentModePassive,
				FuncIndices: funcIndices,
			})
		case elemSegmentFlagActiveExplicitTableFuncIndices:
			tableIndex, err := readU32(r)
			if err != nil {
				diags.Addf("element[%d]: invalid table index: %v", i, err)
				break
			}
			offsetExpr, err := decodeConstExprInstrs(r)
			if err != nil {
				diags.Addf("element[%d]: invalid offset expr: %v", i, err)
				break
			}
			elemKind, err := readByte(r)
			if err != nil {
				diags.Addf("element[%d]: missing elemkind: %v", i, err)
				break
			}
			if elemKind != elemKindFuncRef {
				diags.Addf("element[%d]: unsupported elemkind 0x%x", i, elemKind)
				break
			}
			funcIndices := decodeElemFuncIndices(r, i, diags)
			out = append(out, wasmir.ElementSegment{
				Mode:        wasmir.ElemSegmentModeActive,
				TableIndex:  tableIndex,
				OffsetExpr:  offsetExpr,
				FuncIndices: funcIndices,
			})
		case elemSegmentFlagDeclarativeFuncIndices:
			elemKind, err := readByte(r)
			if err != nil {
				diags.Addf("element[%d]: missing elemkind: %v", i, err)
				break
			}
			if elemKind != elemKindFuncRef {
				diags.Addf("element[%d]: unsupported elemkind 0x%x", i, elemKind)
				break
			}
			funcIndices := decodeElemFuncIndices(r, i, diags)
			out = append(out, wasmir.ElementSegment{
				Mode:        wasmir.ElemSegmentModeDeclarative,
				FuncIndices: funcIndices,
			})
		case elemSegmentFlagActiveExplicitTableExprs:
			tableIndex, err := readU32(r)
			if err != nil {
				diags.Addf("element[%d]: invalid table index: %v", i, err)
				break
			}
			offsetExpr, err := decodeConstExprInstrs(r)
			if err != nil {
				diags.Addf("element[%d]: invalid offset expr: %v", i, err)
				break
			}
			refType, err := decodeRefTypeFromReader(r)
			if err != nil {
				diags.Addf("element[%d]: invalid ref type: %v", i, err)
				break
			}
			exprCount, err := readU32(r)
			if err != nil {
				diags.Addf("element[%d]: invalid expr vector length: %v", i, err)
				break
			}
			exprCap, err := boundedVectorCapacity(r, exprCount)
			if err != nil {
				diags.Addf("element[%d]: invalid expr vector length: %v", i, err)
				break
			}
			exprs := make([][]wasmir.Instruction, 0, exprCap)
			for j := uint32(0); j < exprCount; j++ {
				expr, err := decodeConstExprInstrs(r)
				if err != nil {
					diags.Addf("element[%d] expr[%d]: invalid const expr: %v", i, j, err)
					break
				}
				exprs = append(exprs, expr)
			}
			out = append(out, wasmir.ElementSegment{
				Mode:       wasmir.ElemSegmentModeActive,
				TableIndex: tableIndex,
				OffsetExpr: offsetExpr,
				Exprs:      exprs,
				RefType:    refType,
			})
		case elemSegmentFlagPassiveExprs, elemSegmentFlagDeclarativeExprs:
			refType, err := decodeRefTypeFromReader(r)
			if err != nil {
				diags.Addf("element[%d]: invalid ref type: %v", i, err)
				break
			}
			exprCount, err := readU32(r)
			if err != nil {
				diags.Addf("element[%d]: invalid expr vector length: %v", i, err)
				break
			}
			exprCap, err := boundedVectorCapacity(r, exprCount)
			if err != nil {
				diags.Addf("element[%d]: invalid expr vector length: %v", i, err)
				break
			}
			exprs := make([][]wasmir.Instruction, 0, exprCap)
			for j := uint32(0); j < exprCount; j++ {
				expr, err := decodeConstExprInstrs(r)
				if err != nil {
					diags.Addf("element[%d] expr[%d]: invalid const expr: %v", i, j, err)
					break
				}
				exprs = append(exprs, expr)
			}
			mode := wasmir.ElemSegmentModePassive
			if flags == elemSegmentFlagDeclarativeExprs {
				mode = wasmir.ElemSegmentModeDeclarative
			}
			out = append(out, wasmir.ElementSegment{
				Mode:    mode,
				Exprs:   exprs,
				RefType: refType,
			})
		default:
			diags.Addf("element[%d]: unsupported flags 0x%x", i, flags)
			break
		}
	}
	return out
}

func decodeDataSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.DataSegment {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("data section: invalid vector length: %v", err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("data section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.DataSegment, 0, capN)
	for i := uint32(0); i < n; i++ {
		flags, err := readByte(r)
		if err != nil {
			diags.Addf("data[%d]: missing flags: %v", i, err)
			break
		}
		seg := wasmir.DataSegment{}
		segOK := true
		switch flags {
		case dataSegmentFlagPassive:
			seg.Mode = wasmir.DataSegmentModePassive
		case dataSegmentFlagActiveMem0, dataSegmentFlagActiveExplicitMemory:
			seg.Mode = wasmir.DataSegmentModeActive
			if flags == dataSegmentFlagActiveExplicitMemory {
				memoryIndex, err := readU32(r)
				if err != nil {
					diags.Addf("data[%d]: invalid memory index: %v", i, err)
					segOK = false
					break
				}
				seg.MemoryIndex = memoryIndex
			}
			offsetExpr, err := decodeConstExprInstrs(r)
			if err != nil {
				diags.Addf("data[%d]: invalid offset expr: %v", i, err)
				segOK = false
				break
			}
			seg.OffsetExpr = offsetExpr
		default:
			diags.Addf("data[%d]: unsupported flags 0x%x", i, flags)
			segOK = false
		}
		if !segOK {
			break
		}
		size, err := readU32(r)
		if err != nil {
			diags.Addf("data[%d]: invalid payload size: %v", i, err)
			break
		}
		init, err := readN(r, int(size))
		if err != nil {
			diags.Addf("data[%d]: invalid payload bytes: %v", i, err)
			break
		}
		seg.Init = init
		out = append(out, seg)
	}
	return out
}

func decodeElemFuncIndices(r *bytes.Reader, elemIdx uint32, diags *diag.ErrorList) []uint32 {
	funcCount, err := readU32(r)
	if err != nil {
		diags.Addf("element[%d]: invalid function index vector length: %v", elemIdx, err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, funcCount)
	if err != nil {
		diags.Addf("element[%d]: invalid function index vector length: %v", elemIdx, err)
		return nil
	}
	funcIndices := make([]uint32, 0, capN)
	for j := uint32(0); j < funcCount; j++ {
		idx, err := readU32(r)
		if err != nil {
			diags.Addf("element[%d] func[%d]: invalid function index: %v", elemIdx, j, err)
			break
		}
		funcIndices = append(funcIndices, idx)
	}
	return funcIndices
}

func decodeMemInstr(r *bytes.Reader, kind wasmir.InstrKind) (wasmir.Instruction, error) {
	alignField, err := readU32(r)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	memoryIndex := uint32(0)
	align := alignField
	if alignField >= 1<<6 {
		if alignField >= 1<<7 {
			return wasmir.Instruction{}, fmt.Errorf("alignment field %d exceeds supported memarg range", alignField)
		}
		memoryIndex, err = readU32(r)
		if err != nil {
			return wasmir.Instruction{}, err
		}
		align = alignField - (1 << 6)
	}
	offset, err := readU64(r)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	return wasmir.Instruction{
		Kind:         kind,
		MemoryIndex:  memoryIndex,
		MemoryAlign:  align,
		MemoryOffset: offset,
	}, nil
}

func decodeMemoryLimits(r *bytes.Reader) (wasmir.ValueType, uint64, bool, uint64, error) {
	flags, err := readByte(r)
	if err != nil {
		return wasmir.ValueType{}, 0, false, 0, err
	}
	switch flags {
	case limitsFlagMinOnly:
		min, err := readU64(r)
		return wasmir.ValueTypeI32, min, false, 0, err
	case limitsFlagMinMax:
		min, err := readU64(r)
		if err != nil {
			return wasmir.ValueType{}, 0, false, 0, err
		}
		max, err := readU64(r)
		return wasmir.ValueTypeI32, min, true, max, err
	case limitsFlagMinOnly64:
		min, err := readU64(r)
		return wasmir.ValueTypeI64, min, false, 0, err
	case limitsFlagMinMax64:
		min, err := readU64(r)
		if err != nil {
			return wasmir.ValueType{}, 0, false, 0, err
		}
		max, err := readU64(r)
		return wasmir.ValueTypeI64, min, true, max, err
	default:
		return wasmir.ValueType{}, 0, false, 0, fmt.Errorf("unsupported memory limits flags 0x%x", flags)
	}
}

func decodeLimits(r *bytes.Reader) (uint32, bool, uint32, error) {
	flags, err := readByte(r)
	if err != nil {
		return 0, false, 0, err
	}
	switch flags {
	case limitsFlagMinOnly:
		min, err := readU32(r)
		return min, false, 0, err
	case limitsFlagMinMax:
		min, err := readU32(r)
		if err != nil {
			return 0, false, 0, err
		}
		max, err := readU32(r)
		return min, true, max, err
	default:
		return 0, false, 0, fmt.Errorf("unsupported limits flags 0x%x", flags)
	}
}

func decodeTableLimits(r *bytes.Reader) (wasmir.ValueType, uint64, bool, uint64, error) {
	return decodeMemoryLimits(r)
}

func decodeExportSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Export {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("export section: invalid vector length: %v", err)
		return nil
	}

	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("export section: invalid vector length: %v", err)
		return nil
	}
	out := make([]wasmir.Export, 0, capN)
	for i := uint32(0); i < n; i++ {
		name, err := readName(r)
		if err != nil {
			diags.Addf("export[%d]: invalid name: %v", i, err)
			break
		}
		kindByte, err := readByte(r)
		if err != nil {
			diags.Addf("export[%d]: missing kind: %v", i, err)
			break
		}
		index, err := readU32(r)
		if err != nil {
			diags.Addf("export[%d]: invalid index: %v", i, err)
			break
		}

		kind, ok := decodeExportKind(kindByte)
		if !ok {
			diags.Addf("export[%d]: unsupported kind 0x%x", i, kindByte)
			continue
		}
		out = append(out, wasmir.Export{Name: name, Kind: kind, Index: index})
	}
	return out
}

func decodeCodeSection(r *bytes.Reader, diags *diag.ErrorList) []decodedFuncBody {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("code section: invalid vector length: %v", err)
		return nil
	}

	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("code section: invalid vector length: %v", err)
		return nil
	}
	out := make([]decodedFuncBody, 0, capN)
	for i := uint32(0); i < n; i++ {
		bodySize, err := readU32(r)
		if err != nil {
			diags.Addf("code[%d]: invalid body size: %v", i, err)
			break
		}
		bodyBytes, err := readN(r, int(bodySize))
		if err != nil {
			diags.Addf("code[%d]: body out of bounds: %v", i, err)
			break
		}

		br := bytes.NewReader(bodyBytes)
		locals := decodeLocals(br, i, diags)
		instrs := decodeInstructionExpr(br, i, diags)
		if !atEOF(br) {
			diags.Addf("code[%d]: trailing bytes after instruction expression", i)
		}

		out = append(out, decodedFuncBody{locals: locals, body: instrs})
	}
	return out
}

func decodeLocals(r *bytes.Reader, funcIdx uint32, diags *diag.ErrorList) []wasmir.ValueType {
	declCount, err := readU32(r)
	if err != nil {
		diags.Addf("code[%d]: invalid local decl count: %v", funcIdx, err)
		return nil
	}
	if _, err := boundedVectorCapacity(r, declCount); err != nil {
		diags.Addf("code[%d]: invalid local decl count: %v", funcIdx, err)
		return nil
	}

	var locals []wasmir.ValueType
	var totalLocals uint64
	for i := uint32(0); i < declCount; i++ {
		n, err := readU32(r)
		if err != nil {
			diags.Addf("code[%d] localdecl[%d]: invalid count: %v", funcIdx, i, err)
			break
		}
		tyCode, err := readByte(r)
		if err != nil {
			diags.Addf("code[%d] localdecl[%d]: missing value type: %v", funcIdx, i, err)
			break
		}
		ty, err := decodeValueTypeFromLeadingByte(r, tyCode)
		if err != nil {
			diags.Addf("code[%d] localdecl[%d]: invalid value type: %v", funcIdx, i, err)
			break
		}
		totalLocals += uint64(n)
		if totalLocals > maxDecodedLocals {
			diags.Addf("code[%d]: too many locals", funcIdx)
			break
		}
		for j := uint32(0); j < n; j++ {
			locals = append(locals, ty)
		}
	}
	return locals
}

// decodeInstructionExpr decodes one function body instruction sequence using
// the shared instruction catalog for opcode dispatch.
func decodeInstructionExpr(r *bytes.Reader, funcIdx uint32, diags *diag.ErrorList) []wasmir.Instruction {
	var out []wasmir.Instruction
	depth := 0

	for !atEOF(r) {
		op, err := readByte(r)
		if err != nil {
			diags.Addf("code[%d]: failed reading opcode: %v", funcIdx, err)
			return out
		}
		ins, err := decodeInstructionFromOpcode(r, op)
		if err != nil {
			diags.Addf("code[%d]: %v", funcIdx, err)
			return out
		}

		switch ins.Kind {
		case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf, wasmir.InstrTryTable:
			out = append(out, ins)
			depth++
		case wasmir.InstrElse:
			if depth == 0 {
				diags.Addf("code[%d]: unexpected else", funcIdx)
				return out
			}
			out = append(out, ins)
		case wasmir.InstrEnd:
			out = append(out, ins)
			if depth == 0 {
				return out
			}
			depth--
		default:
			out = append(out, ins)
		}
	}

	diags.Addf("code[%d]: unterminated instruction expression (missing end)", funcIdx)
	return out
}

func decodeInstructionFromOpcode(r *bytes.Reader, op byte) (wasmir.Instruction, error) {
	switch op {
	case 0xfb, 0xfc, 0xfd:
		subop, err := readU32(r)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("0x%x prefixed op missing/invalid subopcode: %w", op, err)
		}
		switch op {
		case 0xfb:
			switch subop {
			case 0x14, 0x15:
				refType, err := decodeHeapTypeImmediateFromReader(r, subop == 0x15)
				if err != nil {
					return wasmir.Instruction{}, fmt.Errorf("ref.test missing/invalid type immediate: %w", err)
				}
				return wasmir.Instruction{Kind: wasmir.InstrRefTest, RefType: refType}, nil
			case 0x16, 0x17:
				refType, err := decodeHeapTypeImmediateFromReader(r, subop == 0x17)
				if err != nil {
					return wasmir.Instruction{}, fmt.Errorf("ref.cast missing/invalid type immediate: %w", err)
				}
				return wasmir.Instruction{Kind: wasmir.InstrRefCast, RefType: refType}, nil
			}
		}
		def, ok := instrdef.LookupInstructionByBinary(op, subop)
		if !ok {
			return wasmir.Instruction{}, fmt.Errorf("unsupported 0x%x subopcode 0x%x", op, subop)
		}
		return decodeInstructionFromDef(r, def)
	default:
		if op == 0x1c {
			n, err := readU32Immediate(r, "select", "result arity")
			if err != nil {
				return wasmir.Instruction{}, err
			}
			if n != 1 {
				return wasmir.Instruction{}, fmt.Errorf("select invalid result arity %d", n)
			}
			vt, err := decodeValueTypeFromReader(r)
			if err != nil {
				return wasmir.Instruction{}, fmt.Errorf("select invalid result type: %w", err)
			}
			return wasmir.Instruction{Kind: wasmir.InstrSelect, SelectType: &vt}, nil
		}
		def, ok := instrdef.LookupInstructionByBinary(0, uint32(op))
		if !ok {
			return wasmir.Instruction{}, fmt.Errorf("unsupported opcode 0x%x", op)
		}
		return decodeInstructionFromDef(r, def)
	}
}

func decodeInstructionFromDef(r *bytes.Reader, def instrdef.InstructionDef) (wasmir.Instruction, error) {
	if def.Binary.Encoding == instrdef.BinaryEncodingSimple {
		return wasmir.Instruction{Kind: def.Kind}, nil
	}

	switch def.Kind {
	case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf:
		ins, err := readControlBlockType(r, def.Kind)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid block type: %w", def.TextName, err)
		}
		return ins, nil
	case wasmir.InstrTryTable:
		ins, err := readTryTableImmediate(r)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid immediate: %w", def.TextName, err)
		}
		return ins, nil
	case wasmir.InstrBr, wasmir.InstrBrIf, wasmir.InstrBrOnNull, wasmir.InstrBrOnNonNull:
		depthImm, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, BranchDepth: depthImm}, nil
	case wasmir.InstrBrTable:
		n, err := readU32Immediate(r, def.TextName, "vector length")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		capN, err := boundedVectorCapacity(r, n)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s invalid vector length: %w", def.TextName, err)
		}
		table := make([]uint32, 0, capN)
		for i := uint32(0); i < n; i++ {
			depth, err := readU32(r)
			if err != nil {
				return wasmir.Instruction{}, fmt.Errorf("%s invalid depth[%d]: %w", def.TextName, i, err)
			}
			table = append(table, depth)
		}
		fallback, err := readU32Immediate(r, def.TextName, "default depth")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{
			Kind:          def.Kind,
			BranchTable:   table,
			BranchDefault: fallback,
		}, nil
	case wasmir.InstrI32Const:
		value, err := readS32Immediate(r, def.TextName)
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, I32Const: value}, nil
	case wasmir.InstrI64Const:
		value, err := readS64Immediate(r, def.TextName)
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, I64Const: value}, nil
	case wasmir.InstrF32Const:
		value, err := readF32Immediate(r, def.TextName)
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, F32Const: value}, nil
	case wasmir.InstrF64Const:
		value, err := readF64Immediate(r, def.TextName)
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, F64Const: value}, nil
	case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
		localIndex, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, LocalIndex: localIndex}, nil
	case wasmir.InstrGlobalGet, wasmir.InstrGlobalSet:
		globalIndex, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, GlobalIndex: globalIndex}, nil
	case wasmir.InstrTableGet, wasmir.InstrTableSet, wasmir.InstrTableGrow, wasmir.InstrTableSize, wasmir.InstrTableFill:
		tableIndex, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TableIndex: tableIndex}, nil
	case wasmir.InstrCall:
		funcIndex, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, FuncIndex: funcIndex}, nil
	case wasmir.InstrReturnCall:
		funcIndex, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, FuncIndex: funcIndex}, nil
	case wasmir.InstrCallIndirect:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		tableIndex, err := readU32Immediate(r, def.TextName, "table")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, CallTypeIndex: typeIndex, TableIndex: tableIndex}, nil
	case wasmir.InstrReturnCallIndirect:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		tableIndex, err := readU32Immediate(r, def.TextName, "table")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, CallTypeIndex: typeIndex, TableIndex: tableIndex}, nil
	case wasmir.InstrCallRef:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, CallTypeIndex: typeIndex}, nil
	case wasmir.InstrReturnCallRef:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, CallTypeIndex: typeIndex}, nil
	case wasmir.InstrThrow:
		tagIndex, err := readU32Immediate(r, def.TextName, "tag")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TagIndex: tagIndex}, nil
	case wasmir.InstrRefNull:
		refType, err := decodeRefNullImmediate(r)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid type immediate: %w", def.TextName, err)
		}
		return wasmir.Instruction{Kind: def.Kind, RefType: refType}, nil
	case wasmir.InstrRefFunc:
		funcIndex, err := readU32Immediate(r, def.TextName, "immediate")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, FuncIndex: funcIndex}, nil
	case wasmir.InstrMemoryInit:
		dataIndex, err := readU32Immediate(r, def.TextName, "data")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		memIndex, err := readU32Immediate(r, def.TextName, "memory")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, DataIndex: dataIndex, MemoryIndex: memIndex}, nil
	case wasmir.InstrDataDrop:
		dataIndex, err := readU32Immediate(r, def.TextName, "data")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, DataIndex: dataIndex}, nil
	case wasmir.InstrMemoryCopy:
		dstMemIndex, err := readU32Immediate(r, def.TextName, "destination memory")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		srcMemIndex, err := readU32Immediate(r, def.TextName, "source memory")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{
			Kind:              def.Kind,
			MemoryIndex:       dstMemIndex,
			SourceMemoryIndex: srcMemIndex,
		}, nil
	case wasmir.InstrMemoryFill, wasmir.InstrMemorySize, wasmir.InstrMemoryGrow:
		memIndex, err := readU32Immediate(r, def.TextName, "memory")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, MemoryIndex: memIndex}, nil
	case wasmir.InstrTableInit:
		elemIndex, err := readU32Immediate(r, def.TextName, "element")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		tableIndex, err := readU32Immediate(r, def.TextName, "table")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TableIndex: tableIndex, ElemIndex: elemIndex}, nil
	case wasmir.InstrElemDrop:
		elemIndex, err := readU32Immediate(r, def.TextName, "element")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, ElemIndex: elemIndex}, nil
	case wasmir.InstrTableCopy:
		dstTableIndex, err := readU32Immediate(r, def.TextName, "destination table")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		srcTableIndex, err := readU32Immediate(r, def.TextName, "source table")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{
			Kind:             def.Kind,
			TableIndex:       dstTableIndex,
			SourceTableIndex: srcTableIndex,
		}, nil
	case wasmir.InstrStructNew, wasmir.InstrStructNewDefault, wasmir.InstrArrayNew, wasmir.InstrArrayNewDefault:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TypeIndex: typeIndex}, nil
	case wasmir.InstrStructGet, wasmir.InstrStructGetS, wasmir.InstrStructGetU, wasmir.InstrStructSet:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		fieldIndex, err := readU32Immediate(r, def.TextName, "field")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TypeIndex: typeIndex, FieldIndex: fieldIndex}, nil
	case wasmir.InstrArrayNewData, wasmir.InstrArrayInitData:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		dataIndex, err := readU32Immediate(r, def.TextName, "data")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TypeIndex: typeIndex, DataIndex: dataIndex}, nil
	case wasmir.InstrArrayNewElem, wasmir.InstrArrayInitElem:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		elemIndex, err := readU32Immediate(r, def.TextName, "element")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TypeIndex: typeIndex, ElemIndex: elemIndex}, nil
	case wasmir.InstrArrayNewFixed:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		fixedCount, err := readU32Immediate(r, def.TextName, "length")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TypeIndex: typeIndex, FixedCount: fixedCount}, nil
	case wasmir.InstrArrayGet, wasmir.InstrArrayGetS, wasmir.InstrArrayGetU, wasmir.InstrArraySet, wasmir.InstrArrayFill:
		typeIndex, err := readU32Immediate(r, def.TextName, "type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, TypeIndex: typeIndex}, nil
	case wasmir.InstrArrayCopy:
		dstTypeIndex, err := readU32Immediate(r, def.TextName, "destination type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		srcTypeIndex, err := readU32Immediate(r, def.TextName, "source type")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{
			Kind:            def.Kind,
			TypeIndex:       dstTypeIndex,
			SourceTypeIndex: srcTypeIndex,
		}, nil
	case wasmir.InstrBrOnCast, wasmir.InstrBrOnCastFail:
		flags, err := readByteImmediate(r, def.TextName, "cast flags")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		depthImm, err := readU32Immediate(r, def.TextName, "label")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		srcType, err := decodeHeapTypeImmediateFromReader(r, flags&0x01 != 0)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid source type: %w", def.TextName, err)
		}
		dstType, err := decodeHeapTypeImmediateFromReader(r, flags&0x02 != 0)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid destination type: %w", def.TextName, err)
		}
		return wasmir.Instruction{
			Kind:          def.Kind,
			BranchDepth:   depthImm,
			SourceRefType: srcType,
			RefType:       dstType,
		}, nil
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
		return decodeMemInstruction(r, def.Kind, def.TextName)
	case wasmir.InstrV128Const:
		bytes, err := readN(r, 16)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid immediate: %w", def.TextName, err)
		}
		var value [16]byte
		copy(value[:], bytes)
		return wasmir.Instruction{Kind: def.Kind, V128Const: value}, nil
	case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU, wasmir.InstrI8x16ReplaceLane,
		wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU, wasmir.InstrI16x8ReplaceLane,
		wasmir.InstrI32x4ExtractLane, wasmir.InstrI32x4ReplaceLane,
		wasmir.InstrI64x2ExtractLane, wasmir.InstrI64x2ReplaceLane,
		wasmir.InstrF32x4ExtractLane, wasmir.InstrF32x4ReplaceLane,
		wasmir.InstrF64x2ExtractLane, wasmir.InstrF64x2ReplaceLane:
		lane, err := readByteImmediate(r, def.TextName, "lane")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		return wasmir.Instruction{Kind: def.Kind, LaneIndex: uint32(lane)}, nil
	case wasmir.InstrI8x16Shuffle:
		bytes, err := readN(r, 16)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("%s missing/invalid immediate: %w", def.TextName, err)
		}
		var lanes [16]byte
		copy(lanes[:], bytes)
		return wasmir.Instruction{Kind: def.Kind, ShuffleLanes: lanes}, nil
	default:
		return wasmir.Instruction{}, fmt.Errorf("%s does not have a generic binary decoder", def.TextName)
	}
}

func decodeMemInstruction(r *bytes.Reader, kind wasmir.InstrKind, name string) (wasmir.Instruction, error) {
	ins, err := decodeMemInstr(r, kind)
	if err != nil {
		return wasmir.Instruction{}, fmt.Errorf("%s invalid memarg: %w", name, err)
	}
	switch kind {
	case wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane,
		wasmir.InstrV128Store8Lane, wasmir.InstrV128Store16Lane, wasmir.InstrV128Store32Lane, wasmir.InstrV128Store64Lane:
		lane, err := readByteImmediate(r, name, "lane")
		if err != nil {
			return wasmir.Instruction{}, err
		}
		ins.LaneIndex = uint32(lane)
	}
	return ins, nil
}

func readU32Immediate(r *bytes.Reader, instrName string, what string) (uint32, error) {
	value, err := readU32(r)
	if err != nil {
		return 0, fmt.Errorf("%s missing/invalid %s immediate: %w", instrName, what, err)
	}
	return value, nil
}

func readByteImmediate(r *bytes.Reader, instrName string, what string) (byte, error) {
	value, err := readByte(r)
	if err != nil {
		return 0, fmt.Errorf("%s missing/invalid %s immediate: %w", instrName, what, err)
	}
	return value, nil
}

func readS32Immediate(r *bytes.Reader, instrName string) (int32, error) {
	value, err := readS32(r)
	if err != nil {
		return 0, fmt.Errorf("read i32 immediate: %w", err)
	}
	return value, nil
}

func readS64Immediate(r *bytes.Reader, instrName string) (int64, error) {
	value, err := readS64(r)
	if err != nil {
		return 0, fmt.Errorf("read i64 immediate: %w", err)
	}
	return value, nil
}

func readF32Immediate(r *bytes.Reader, instrName string) (uint32, error) {
	value, err := readU32LE(r)
	if err != nil {
		return 0, fmt.Errorf("read f32 immediate: %w", err)
	}
	return value, nil
}

func readF64Immediate(r *bytes.Reader, instrName string) (uint64, error) {
	value, err := readU64LE(r)
	if err != nil {
		return 0, fmt.Errorf("read f64 immediate: %w", err)
	}
	return value, nil
}

func readControlBlockType(r *bytes.Reader, kind wasmir.InstrKind) (wasmir.Instruction, error) {
	b, err := readByte(r)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	ins := wasmir.Instruction{Kind: kind}
	if b == blockTypeEmptyCode {
		return ins, nil
	}
	if isValueTypeLeadByte(b) {
		vt, err := decodeValueTypeFromLeadingByte(r, b)
		if err != nil {
			return wasmir.Instruction{}, err
		}
		ins.BlockType = &vt
		return ins, nil
	}
	if err := r.UnreadByte(); err != nil {
		return wasmir.Instruction{}, err
	}
	v, err := readS64(r)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	if v < 0 {
		return wasmir.Instruction{}, fmt.Errorf("unsupported signed block type %d", v)
	}
	if v > (1<<32 - 1) {
		return wasmir.Instruction{}, fmt.Errorf("block type index out of range: %d", v)
	}
	ins.BlockTypeUsesIndex = true
	ins.BlockTypeIndex = uint32(v)
	return ins, nil
}

func readTryTableImmediate(r *bytes.Reader) (wasmir.Instruction, error) {
	ins, err := readControlBlockType(r, wasmir.InstrTryTable)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	n, err := readU32Immediate(r, "try_table", "catch vector length")
	if err != nil {
		return wasmir.Instruction{}, err
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		return wasmir.Instruction{}, fmt.Errorf("try_table invalid catch vector length: %w", err)
	}
	ins.TryTableCatches = make([]wasmir.TryTableCatch, 0, capN)
	for i := uint32(0); i < n; i++ {
		catch, err := readTryTableCatch(r)
		if err != nil {
			return wasmir.Instruction{}, fmt.Errorf("try_table invalid catch[%d]: %w", i, err)
		}
		ins.TryTableCatches = append(ins.TryTableCatches, catch)
	}
	return ins, nil
}

func readTryTableCatch(r *bytes.Reader) (wasmir.TryTableCatch, error) {
	kindByte, err := readByte(r)
	if err != nil {
		return wasmir.TryTableCatch{}, err
	}
	catch := wasmir.TryTableCatch{Kind: wasmir.TryTableCatchKind(kindByte)}
	switch catch.Kind {
	case wasmir.TryTableCatchKindTag, wasmir.TryTableCatchKindTagRef:
		tagIndex, err := readU32Immediate(r, "try_table catch", "tag")
		if err != nil {
			return wasmir.TryTableCatch{}, err
		}
		labelDepth, err := readU32Immediate(r, "try_table catch", "label")
		if err != nil {
			return wasmir.TryTableCatch{}, err
		}
		catch.TagIndex = tagIndex
		catch.LabelDepth = labelDepth
		return catch, nil
	case wasmir.TryTableCatchKindAll, wasmir.TryTableCatchKindAllRef:
		labelDepth, err := readU32Immediate(r, "try_table catch", "label")
		if err != nil {
			return wasmir.TryTableCatch{}, err
		}
		catch.LabelDepth = labelDepth
		return catch, nil
	default:
		return wasmir.TryTableCatch{}, fmt.Errorf("unknown catch kind %d", kindByte)
	}
}

func decodeConstExpr(r *bytes.Reader) (wasmir.Instruction, error) {
	instrs, err := decodeConstExprInstrs(r)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	if len(instrs) != 1 {
		return wasmir.Instruction{}, fmt.Errorf("const expr must contain exactly one instruction")
	}
	return instrs[0], nil
}

func decodeConstExprInstrs(r *bytes.Reader) ([]wasmir.Instruction, error) {
	var out []wasmir.Instruction
	for {
		op, err := readByte(r)
		if err != nil {
			return nil, err
		}
		if def, ok := instrdef.LookupInstructionByBinary(0, uint32(op)); ok && def.Kind == wasmir.InstrEnd {
			return out, nil
		}
		ins, err := decodeConstExprInstr(r, op)
		if err != nil {
			return nil, err
		}
		out = append(out, ins)
	}
}

func decodeConstExprInstr(r *bytes.Reader, op byte) (wasmir.Instruction, error) {
	ins, err := decodeInstructionFromOpcode(r, op)
	if err != nil {
		return wasmir.Instruction{}, err
	}
	switch ins.Kind {
	case wasmir.InstrI32Const,
		wasmir.InstrI64Const,
		wasmir.InstrF32Const,
		wasmir.InstrF64Const,
		wasmir.InstrI32Add,
		wasmir.InstrI32Sub,
		wasmir.InstrI32Mul,
		wasmir.InstrI64Add,
		wasmir.InstrI64Sub,
		wasmir.InstrI64Mul,
		wasmir.InstrRefNull,
		wasmir.InstrRefFunc,
		wasmir.InstrGlobalGet,
		wasmir.InstrV128Const,
		wasmir.InstrStructNew,
		wasmir.InstrStructNewDefault,
		wasmir.InstrArrayNew,
		wasmir.InstrArrayNewDefault,
		wasmir.InstrArrayNewFixed,
		wasmir.InstrExternConvertAny,
		wasmir.InstrAnyConvertExtern,
		wasmir.InstrRefI31:
		return ins, nil
	default:
		return wasmir.Instruction{}, fmt.Errorf("unsupported const expr instruction %s", instructionName(ins.Kind))
	}
}

func instructionName(kind wasmir.InstrKind) string {
	if def, ok := instrdef.LookupInstructionByKind(kind); ok {
		return def.TextName
	}
	return fmt.Sprintf("instruction %d", kind)
}

func decodeValueTypeVec(r *bytes.Reader, where string, diags *diag.ErrorList) []wasmir.ValueType {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("%s: invalid vector length: %v", where, err)
		return nil
	}
	capN, err := boundedVectorCapacity(r, n)
	if err != nil {
		diags.Addf("%s: invalid vector length: %v", where, err)
		return nil
	}
	out := make([]wasmir.ValueType, 0, capN)
	for i := uint32(0); i < n; i++ {
		vt, err := decodeValueTypeFromReader(r)
		if err != nil {
			diags.Addf("%s[%d]: invalid value type: %v", where, i, err)
			break
		}
		out = append(out, vt)
	}
	return out
}

// boundedVectorCapacity returns a safe slice capacity for a decoded vector of
// n entries. It uses the remaining bytes in r as a cheap allocation guard
// against malformed binaries that claim absurd vector lengths; semantic vector
// validation still happens in the specific decoder that consumes the entries.
func boundedVectorCapacity(r *bytes.Reader, n uint32) (int, error) {
	if uint64(n) > uint64(r.Len()) {
		return 0, fmt.Errorf("length out of bounds")
	}
	return int(n), nil
}

func decodeValueTypeFromReader(r *bytes.Reader) (wasmir.ValueType, error) {
	b, err := readByte(r)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	return decodeValueTypeFromLeadingByte(r, b)
}

func decodeValueType(code byte) (wasmir.ValueType, bool) {
	switch code {
	case valueTypeI32Code:
		return wasmir.ValueTypeI32, true
	case valueTypeI64Code:
		return wasmir.ValueTypeI64, true
	case valueTypeF32Code:
		return wasmir.ValueTypeF32, true
	case valueTypeF64Code:
		return wasmir.ValueTypeF64, true
	case valueTypeV128Code:
		return wasmir.ValueTypeV128, true
	case refTypeArrayCode:
		return wasmir.RefTypeArray(true), true
	case refTypeStructCode:
		return wasmir.RefTypeStruct(true), true
	case refTypeI31Code:
		return wasmir.RefTypeI31(true), true
	case refTypeEqCode:
		return wasmir.RefTypeEq(true), true
	case refTypeAnyCode:
		return wasmir.RefTypeAny(true), true
	case refTypeNoExternCode:
		return wasmir.RefTypeNoExtern(true), true
	case refTypeNoFuncCode:
		return wasmir.RefTypeNoFunc(true), true
	case refTypeNoExnCode:
		return wasmir.RefTypeNoExn(true), true
	case refTypeNoneCode:
		return wasmir.RefTypeNone(true), true
	case refTypeFuncRefCode:
		return wasmir.RefTypeFunc(true), true
	case valueTypeExternRefCode:
		return wasmir.RefTypeExtern(true), true
	case refTypeExnCode:
		return wasmir.RefTypeExn(true), true
	default:
		return wasmir.ValueType{}, false
	}
}

func decodeExportKind(code byte) (wasmir.ExternalKind, bool) {
	switch code {
	case exportKindFunctionCode:
		return wasmir.ExternalKindFunction, true
	case exportKindTableCode:
		return wasmir.ExternalKindTable, true
	case exportKindMemoryCode:
		return wasmir.ExternalKindMemory, true
	case exportKindGlobalCode:
		return wasmir.ExternalKindGlobal, true
	case exportKindTagCode:
		return wasmir.ExternalKindTag, true
	default:
		return 0, false
	}
}

func decodeRefNullImmediate(r *bytes.Reader) (wasmir.ValueType, error) {
	b, err := readByte(r)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	if refType, ok := decodeValueType(b); ok && refType.IsRef() {
		return refType, nil
	}
	if err := r.UnreadByte(); err != nil {
		return wasmir.ValueType{}, err
	}
	typeIndex, err := readS33(r)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	return decodeHeapTypeImmediate(typeIndex, true)
}

func decodeValueTypeFromLeadingByte(r *bytes.Reader, b byte) (wasmir.ValueType, error) {
	switch b {
	case refNullPrefixCode, refPrefixCode:
		typeIndex, err := readS33(r)
		if err != nil {
			return wasmir.ValueType{}, err
		}
		return decodeHeapTypeImmediate(typeIndex, b == refNullPrefixCode)
	default:
		vt, ok := decodeValueType(b)
		if !ok {
			return wasmir.ValueType{}, fmt.Errorf("unsupported value type 0x%x", b)
		}
		return vt, nil
	}
}

func decodeRefTypeFromReader(r *bytes.Reader) (wasmir.ValueType, error) {
	vt, err := decodeValueTypeFromReader(r)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	if !vt.IsRef() {
		return wasmir.ValueType{}, fmt.Errorf("expected reference type, got %s", vt)
	}
	return vt, nil
}

func decodeHeapTypeImmediateFromReader(r *bytes.Reader, nullable bool) (wasmir.ValueType, error) {
	b, err := readByte(r)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	if refType, ok := decodeValueType(b); ok && refType.IsRef() {
		refType.Nullable = nullable
		return refType, nil
	}
	if err := r.UnreadByte(); err != nil {
		return wasmir.ValueType{}, err
	}
	typeIndex, err := readS33(r)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	return decodeHeapTypeImmediate(typeIndex, nullable)
}

func isValueTypeLeadByte(b byte) bool {
	if _, ok := decodeValueType(b); ok {
		return true
	}
	return b == refNullPrefixCode || b == refPrefixCode
}

func decodeHeapTypeImmediate(typeIndex int64, nullable bool) (wasmir.ValueType, error) {
	if typeIndex < 0 {
		switch typeIndex {
		case -22:
			return wasmir.RefTypeArray(nullable), nil
		case -21:
			return wasmir.RefTypeStruct(nullable), nil
		case -20:
			return wasmir.RefTypeI31(nullable), nil
		case -19:
			return wasmir.RefTypeEq(nullable), nil
		case -18:
			return wasmir.RefTypeAny(nullable), nil
		case -14:
			return wasmir.RefTypeNoExtern(nullable), nil
		case -12:
			return wasmir.RefTypeNoExn(nullable), nil
		case -13:
			return wasmir.RefTypeNoFunc(nullable), nil
		case -15:
			return wasmir.RefTypeNone(nullable), nil
		case -16:
			return wasmir.RefTypeFunc(nullable), nil
		case -17:
			return wasmir.RefTypeExtern(nullable), nil
		case -23:
			return wasmir.RefTypeExn(nullable), nil
		default:
			return wasmir.ValueType{}, fmt.Errorf("unsupported negative heap type %d", typeIndex)
		}
	}
	return wasmir.RefTypeIndexed(uint32(typeIndex), nullable), nil
}

// atEOF reports whether r has no unread bytes left.
func atEOF(r *bytes.Reader) bool {
	return r.Len() == 0
}

// readByte reads one byte from r.
// It returns an "unexpected EOF" error when no bytes remain.
func readByte(r *bytes.Reader) (byte, error) {
	b, err := r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return b, nil
}

// readN reads exactly n bytes from r.
// It returns an "unexpected EOF" error when fewer than n bytes are available.
func readN(r *bytes.Reader, n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("negative length %d", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

// The Wasm binary format uses width-constrained LEB128 encodings for immediates
// such as u32, s33, and s64. These helpers intentionally do more than generic
// varint decoding: they reject encodings that exceed the permitted byte width
// for the target integer size, and they reject terminal bytes whose unused
// high bits are malformed for that width.
//
// This matches the spec's malformed-binary rules and lets the decoder report
// bad integer encodings directly instead of silently accepting oversized or
// ill-formed representations.

// readU32 reads a Wasm u32 immediate from r.
func readU32(r *bytes.Reader) (uint32, error) {
	v, err := readULEB128(r, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// readU64 reads a Wasm u64 immediate from r.
func readU64(r *bytes.Reader) (uint64, error) {
	return readULEB128(r, 64)
}

// readS32 reads a Wasm s32 immediate from r.
func readS32(r *bytes.Reader) (int32, error) {
	v, err := readSLEB128(r, 32)
	if err != nil {
		return 0, err
	}
	return int32(v), nil
}

// readS33 reads a Wasm s33 immediate from r.
func readS33(r *bytes.Reader) (int64, error) {
	return readSLEB128(r, 33)
}

// readS64 reads a Wasm s64 immediate from r.
func readS64(r *bytes.Reader) (int64, error) {
	return readSLEB128(r, 64)
}

// readULEB128 decodes a Wasm unsigned LEB128 immediate with the given bit
// width and validates the encoding against that width.
func readULEB128(r *bytes.Reader, bits int) (uint64, error) {
	var result uint64
	var shift uint
	maxBytes := (bits + 6) / 7
	count := 0

	for {
		if count == maxBytes {
			return 0, fmt.Errorf("integer representation too long")
		}
		b, err := readByte(r)
		if err != nil {
			return 0, err
		}
		count++

		payload := uint64(b & 0x7f)
		if shift >= 64 || payload<<shift>>shift != payload {
			return 0, fmt.Errorf("overflows a 64-bit integer")
		}
		result |= payload << shift

		if (b & 0x80) == 0 {
			if bits < 64 && result >= (uint64(1)<<bits) {
				return 0, fmt.Errorf("u%d overflow: %d", bits, result)
			}
			return result, nil
		}
		shift += 7
	}
}

// readSLEB128 decodes a Wasm signed LEB128 immediate with the given bit width
// and validates both its terminal-byte padding and its final signed range.
func readSLEB128(r *bytes.Reader, bits int) (int64, error) {
	var result int64
	var shift uint
	var b byte
	maxBytes := (bits + 6) / 7
	count := 0

	for {
		if count == maxBytes {
			return 0, fmt.Errorf("integer representation too long")
		}
		var err error
		b, err = readByte(r)
		if err != nil {
			return 0, err
		}
		count++

		result |= int64(b&0x7f) << shift
		shift += 7
		if (b & 0x80) == 0 {
			break
		}
	}

	if shift < 64 && (b&0x40) != 0 {
		result |= ^int64(0) << shift
	}
	if count == maxBytes {
		usedBits := bits - 7*(count-1)
		if usedBits < 7 {
			payload := uint64(b & 0x7f)
			topBits := payload >> usedBits
			expectedTopBits := uint64(0)
			if result < 0 {
				expectedTopBits = (uint64(1) << (7 - usedBits)) - 1
			}
			if topBits != expectedTopBits {
				return 0, fmt.Errorf("overflows a %d-bit integer", bits)
			}
		}
	}

	min := -(int64(1) << (bits - 1))
	max := (int64(1) << (bits - 1)) - 1
	if bits == 64 {
		min = math.MinInt64
		max = math.MaxInt64
	}
	if result < min || result > max {
		return 0, fmt.Errorf("overflows a %d-bit integer", bits)
	}
	return result, nil
}

// readU32LE reads a 4-byte little-endian uint32 from r.
func readU32LE(r *bytes.Reader) (uint32, error) {
	b, err := readN(r, 4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}

// readU64LE reads an 8-byte little-endian uint64 from r.
func readU64LE(r *bytes.Reader) (uint64, error) {
	b, err := readN(r, 8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}

// readName reads a WASM name from r as: u32 byte length followed by UTF-8
// bytes.
func readName(r *bytes.Reader) (string, error) {
	n, err := readU32(r)
	if err != nil {
		return "", err
	}
	b, err := readN(r, int(n))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("invalid UTF-8 name")
	}
	return string(b), nil
}
