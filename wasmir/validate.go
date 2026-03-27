package wasmir

import (
	"fmt"

	"github.com/eliben/watgo/diag"
)

const (
	maxMemoryPages32 uint64 = 65536
	maxMemoryPages64 uint64 = 1 << 48
	maxTableElems32  uint64 = 1<<32 - 1
)

type validatedValue struct {
	Type ValueType
}

func isRefValueType(vt ValueType) bool {
	return vt.IsRef()
}

func validatedValueFromType(vt ValueType) validatedValue {
	return validatedValue{Type: vt}
}

func sameValidatedValue(got, want validatedValue) bool {
	return got.Type == want.Type
}

func equalValueTypeSlices(a, b []ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func matchesExpectedValue(got, want validatedValue) bool {
	if got.Type == want.Type {
		return true
	}
	if !got.Type.IsRef() || !want.Type.IsRef() {
		return false
	}
	if got.Type.Nullable && !want.Type.Nullable {
		return false
	}
	switch want.Type.HeapType.Kind {
	case HeapKindAny:
		switch got.Type.HeapType.Kind {
		case HeapKindNone, HeapKindAny, HeapKindEq, HeapKindI31, HeapKindArray, HeapKindStruct:
			return true
		case HeapKindTypeIndex:
			return true
		default:
			return false
		}
	case HeapKindNone:
		return got.Type.HeapType.Kind == HeapKindNone
	}
	if want.Type.UsesTypeIndex() {
		if !got.Type.UsesTypeIndex() {
			return got.Type.HeapType.Kind == HeapKindNoFunc && want.Type.Nullable
		}
		if got.Type.HeapType.TypeIndex != want.Type.HeapType.TypeIndex {
			return false
		}
	} else {
		switch want.Type.HeapType.Kind {
		case HeapKindFunc:
			if got.Type.HeapType.Kind != HeapKindFunc &&
				got.Type.HeapType.Kind != HeapKindTypeIndex &&
				got.Type.HeapType.Kind != HeapKindNoFunc {
				return false
			}
		case HeapKindExtern:
			if got.Type.HeapType.Kind != HeapKindExtern && got.Type.HeapType.Kind != HeapKindNoExtern {
				return false
			}
		case HeapKindNoFunc:
			if got.Type.HeapType.Kind != HeapKindNoFunc {
				return false
			}
		case HeapKindNoExtern:
			if got.Type.HeapType.Kind != HeapKindNoExtern {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validatedValueName(v validatedValue) string {
	return v.Type.String()
}

func refinedNonNullValue(v validatedValue) validatedValue {
	if isRefValueType(v.Type) {
		v.Type.Nullable = false
	}
	return v
}

func memoryAddressType(m *Module, memoryIndex uint32) ValueType {
	if m != nil && int(memoryIndex) < len(m.Memories) {
		if m.Memories[memoryIndex].AddressType == ValueTypeI64 {
			return ValueTypeI64
		}
	}
	return ValueTypeI32
}

func tableAddressType(m *Module, tableIndex uint32) ValueType {
	if m != nil && int(tableIndex) < len(m.Tables) {
		if m.Tables[tableIndex].AddressType == ValueTypeI64 {
			return ValueTypeI64
		}
	}
	return ValueTypeI32
}

func validModuleValueType(m *Module, vt ValueType) bool {
	if !vt.IsRef() {
		return vt.Kind != ValueKindInvalid
	}
	if vt.HeapType.Kind != HeapKindTypeIndex {
		return vt.HeapType.Kind != HeapKindInvalid
	}
	return int(vt.HeapType.TypeIndex) < len(m.Types)
}

func isDefaultableFieldType(ft FieldType) bool {
	if ft.Packed != PackedTypeNone {
		return true
	}
	return isDefaultableValueType(ft.Type)
}

func elementRefType(m *Module, elemIndex uint32) (ValueType, bool) {
	if m == nil || int(elemIndex) >= len(m.Elements) {
		return ValueType{}, false
	}
	elem := m.Elements[elemIndex]
	if elem.RefType.Kind != ValueKindInvalid {
		return elem.RefType, true
	}
	if len(elem.FuncIndices) > 0 {
		return RefTypeFunc(true), true
	}
	return ValueType{}, false
}

func typeDefAtIndex(m *Module, typeIndex uint32) (FuncType, bool) {
	if m == nil || int(typeIndex) >= len(m.Types) {
		return FuncType{}, false
	}
	return m.Types[typeIndex], true
}

func isDefaultableValueType(vt ValueType) bool {
	if vt.IsRef() {
		return vt.Nullable
	}
	switch vt.Kind {
	case ValueKindI32, ValueKindI64, ValueKindF32, ValueKindF64, ValueKindV128:
		return true
	default:
		return false
	}
}

func fieldValueType(ft FieldType) ValueType {
	switch ft.Packed {
	case PackedTypeI8, PackedTypeI16:
		return ValueTypeI32
	default:
		return ft.Type
	}
}

func sameFieldStorage(a, b FieldType) bool {
	if a.Packed != b.Packed {
		return false
	}
	if a.Packed != PackedTypeNone {
		return true
	}
	return a.Type == b.Type
}

func fieldByteWidth(ft FieldType) (uint32, bool) {
	switch ft.Packed {
	case PackedTypeI8:
		return 1, true
	case PackedTypeI16:
		return 2, true
	}
	switch ft.Type {
	case ValueTypeI32, ValueTypeF32:
		return 4, true
	case ValueTypeI64, ValueTypeF64:
		return 8, true
	case ValueTypeV128:
		return 16, true
	default:
		return 0, false
	}
}

func elementSegmentType(_ *Module, seg ElementSegment) ValueType {
	if seg.RefType.IsRef() {
		return seg.RefType
	}
	if len(seg.FuncIndices) > 0 {
		return RefTypeFunc(true)
	}
	return ValueType{}
}

func typeIndexHasKind(m *Module, typeIndex uint32, kind TypeDefKind) bool {
	if m == nil || int(typeIndex) >= len(m.Types) {
		return false
	}
	return m.Types[typeIndex].Kind == kind
}

func isTypeIndexSubtype(m *Module, got, want uint32) bool {
	if got == want {
		return true
	}
	if typeIndicesEquivalent(m, got, want) {
		return true
	}
	if m == nil || int(got) >= len(m.Types) || int(want) >= len(m.Types) {
		return false
	}
	seen := map[uint32]bool{}
	stack := []uint32{got}
	for len(stack) > 0 {
		idx := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[idx] {
			continue
		}
		seen[idx] = true
		for _, super := range m.Types[idx].SuperTypes {
			if super == want || typeIndicesEquivalent(m, super, want) {
				return true
			}
			stack = append(stack, super)
		}
	}
	return false
}

type typePair struct {
	a uint32
	b uint32
}

func typeIndicesEquivalent(m *Module, a, b uint32) bool {
	groupVisiting := make(map[typePair]bool)
	groupMemo := make(map[typePair]bool)
	typeVisiting := make(map[typePair]bool)
	typeMemo := make(map[typePair]bool)
	return typeIndicesEquivalentRec(m, a, b, groupVisiting, groupMemo, typeVisiting, typeMemo)
}

func typeIndicesEquivalentRec(m *Module, a, b uint32, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	startA, sizeA, posA := recGroupInfo(m, a)
	startB, sizeB, posB := recGroupInfo(m, b)
	if posA != posB || sizeA != sizeB {
		return false
	}
	if sizeA > 1 {
		groupKey := typePair{a: startA, b: startB}
		if eq, ok := groupMemo[groupKey]; ok {
			return eq
		}
		if groupVisiting[groupKey] {
			return true
		}
		groupVisiting[groupKey] = true
		defer delete(groupVisiting, groupKey)
		for i := uint32(0); i < sizeA; i++ {
			if !typeIndicesEquivalentInGroupRec(m, startA, startB, sizeA, startA+i, startB+i, groupVisiting, groupMemo, typeVisiting, typeMemo) {
				groupMemo[groupKey] = false
				return false
			}
		}
		groupMemo[groupKey] = true
		return true
	}
	return typeIndicesEquivalentBodyRec(m, a, b, groupVisiting, groupMemo, typeVisiting, typeMemo)
}

func typeIndicesEquivalentInGroupRec(m *Module, groupA, groupB, groupSize, a, b uint32, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	if a == b && groupA == groupB {
		return true
	}
	if m == nil || int(a) >= len(m.Types) || int(b) >= len(m.Types) {
		return false
	}
	key := typePair{a: a, b: b}
	if eq, ok := typeMemo[key]; ok {
		return eq
	}
	if typeVisiting[key] {
		return true
	}
	typeVisiting[key] = true
	defer delete(typeVisiting, key)

	ta := m.Types[a]
	tb := m.Types[b]
	if ta.SubType != tb.SubType || ta.Final != tb.Final || ta.Kind != tb.Kind || len(ta.SuperTypes) != len(tb.SuperTypes) {
		typeMemo[key] = false
		return false
	}
	for i := range ta.SuperTypes {
		if !typeIndexRefsEquivalentInGroup(m, groupA, groupB, groupSize, ta.SuperTypes[i], tb.SuperTypes[i], groupVisiting, groupMemo, typeVisiting, typeMemo) {
			typeMemo[key] = false
			return false
		}
	}

	var eq bool
	switch ta.Kind {
	case TypeDefKindFunc:
		eq = len(ta.Params) == len(tb.Params) && len(ta.Results) == len(tb.Results)
		if eq {
			for i := range ta.Params {
				if !valueTypesEquivalentInRecGroup(m, ta.Params[i], tb.Params[i], groupA, groupB, groupSize, groupVisiting, groupMemo, typeVisiting, typeMemo) {
					eq = false
					break
				}
			}
		}
		if eq {
			for i := range ta.Results {
				if !valueTypesEquivalentInRecGroup(m, ta.Results[i], tb.Results[i], groupA, groupB, groupSize, groupVisiting, groupMemo, typeVisiting, typeMemo) {
					eq = false
					break
				}
			}
		}
	case TypeDefKindStruct:
		eq = len(ta.Fields) == len(tb.Fields)
		if eq {
			for i := range ta.Fields {
				if !fieldTypesEquivalentInRecGroup(m, ta.Fields[i], tb.Fields[i], groupA, groupB, groupSize, groupVisiting, groupMemo, typeVisiting, typeMemo) {
					eq = false
					break
				}
			}
		}
	case TypeDefKindArray:
		eq = fieldTypesEquivalentInRecGroup(m, ta.ElemField, tb.ElemField, groupA, groupB, groupSize, groupVisiting, groupMemo, typeVisiting, typeMemo)
	default:
		eq = false
	}
	typeMemo[key] = eq
	return eq
}

func typeIndexRefsEquivalentInGroup(m *Module, groupA, groupB, groupSize, a, b uint32, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	inA := a >= groupA && a < groupA+groupSize
	inB := b >= groupB && b < groupB+groupSize
	if inA || inB {
		if !inA || !inB {
			return false
		}
		if a-groupA != b-groupB {
			return false
		}
		return typeIndicesEquivalentInGroupRec(m, groupA, groupB, groupSize, a, b, groupVisiting, groupMemo, typeVisiting, typeMemo)
	}
	return typeIndicesEquivalentRec(m, a, b, groupVisiting, groupMemo, typeVisiting, typeMemo)
}

func valueTypesEquivalentInRecGroup(m *Module, a, b ValueType, groupA, groupB, groupSize uint32, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind != ValueKindRef {
		return a == b
	}
	if a.Nullable != b.Nullable {
		return false
	}
	if a.UsesTypeIndex() && b.UsesTypeIndex() {
		return typeIndexRefsEquivalentInGroup(m, groupA, groupB, groupSize, a.HeapType.TypeIndex, b.HeapType.TypeIndex, groupVisiting, groupMemo, typeVisiting, typeMemo)
	}
	return a.HeapType.Kind == b.HeapType.Kind
}

func fieldTypesEquivalentInRecGroup(m *Module, a, b FieldType, groupA, groupB, groupSize uint32, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	if a.Mutable != b.Mutable || a.Packed != b.Packed {
		return false
	}
	if a.Packed != PackedTypeNone {
		return true
	}
	return valueTypesEquivalentInRecGroup(m, a.Type, b.Type, groupA, groupB, groupSize, groupVisiting, groupMemo, typeVisiting, typeMemo)
}

func recGroupInfo(m *Module, idx uint32) (start uint32, size uint32, pos uint32) {
	if m == nil || int(idx) >= len(m.Types) {
		return idx, 1, 0
	}
	for s := idx; ; {
		if groupSize := m.Types[s].RecGroupSize; groupSize > 0 && idx < s+groupSize {
			return s, groupSize, idx - s
		}
		if s == 0 {
			break
		}
		s--
	}
	return idx, 1, 0
}

func typeIndicesEquivalentBodyRec(m *Module, a, b uint32, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	if a == b {
		return true
	}
	if m == nil || int(a) >= len(m.Types) || int(b) >= len(m.Types) {
		return false
	}
	key := typePair{a: a, b: b}
	if eq, ok := typeMemo[key]; ok {
		return eq
	}
	if typeVisiting[key] {
		return true
	}
	typeVisiting[key] = true
	defer delete(typeVisiting, key)

	ta := m.Types[a]
	tb := m.Types[b]
	if ta.SubType != tb.SubType || ta.Final != tb.Final || ta.Kind != tb.Kind || len(ta.SuperTypes) != len(tb.SuperTypes) {
		typeMemo[key] = false
		return false
	}
	for i := range ta.SuperTypes {
		if !typeIndicesEquivalentRec(m, ta.SuperTypes[i], tb.SuperTypes[i], groupVisiting, groupMemo, typeVisiting, typeMemo) {
			typeMemo[key] = false
			return false
		}
	}

	var eq bool
	switch ta.Kind {
	case TypeDefKindFunc:
		eq = len(ta.Params) == len(tb.Params) && len(ta.Results) == len(tb.Results)
		if eq {
			for i := range ta.Params {
				if !valueTypesEquivalentInModule(m, ta.Params[i], tb.Params[i], groupVisiting, groupMemo, typeVisiting, typeMemo) {
					eq = false
					break
				}
			}
		}
		if eq {
			for i := range ta.Results {
				if !valueTypesEquivalentInModule(m, ta.Results[i], tb.Results[i], groupVisiting, groupMemo, typeVisiting, typeMemo) {
					eq = false
					break
				}
			}
		}
	case TypeDefKindStruct:
		eq = len(ta.Fields) == len(tb.Fields)
		if eq {
			for i := range ta.Fields {
				if !fieldTypesEquivalentInModule(m, ta.Fields[i], tb.Fields[i], groupVisiting, groupMemo, typeVisiting, typeMemo) {
					eq = false
					break
				}
			}
		}
	case TypeDefKindArray:
		eq = fieldTypesEquivalentInModule(m, ta.ElemField, tb.ElemField, groupVisiting, groupMemo, typeVisiting, typeMemo)
	default:
		eq = false
	}
	typeMemo[key] = eq
	return eq
}

func valueTypesEquivalentInModule(m *Module, a, b ValueType, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind != ValueKindRef {
		return a == b
	}
	if a.Nullable != b.Nullable {
		return false
	}
	if a.UsesTypeIndex() && b.UsesTypeIndex() {
		return typeIndicesEquivalentRec(m, a.HeapType.TypeIndex, b.HeapType.TypeIndex, groupVisiting, groupMemo, typeVisiting, typeMemo)
	}
	return a.HeapType.Kind == b.HeapType.Kind
}

func fieldTypesEquivalentInModule(m *Module, a, b FieldType, groupVisiting map[typePair]bool, groupMemo map[typePair]bool, typeVisiting map[typePair]bool, typeMemo map[typePair]bool) bool {
	if a.Mutable != b.Mutable || a.Packed != b.Packed {
		return false
	}
	if a.Packed != PackedTypeNone {
		return true
	}
	return valueTypesEquivalentInModule(m, a.Type, b.Type, groupVisiting, groupMemo, typeVisiting, typeMemo)
}

func isModuleValueSubtype(m *Module, got, want ValueType) bool {
	if got == want {
		return true
	}
	if got.IsRef() && want.IsRef() {
		return matchesRefTypeInModule(m, got, want)
	}
	return false
}

func isTypeFinal(td FuncType) bool {
	return !td.SubType || td.Final
}

func isValidDeclaredSubtype(m *Module, sub, super FuncType) bool {
	if sub.Kind != super.Kind {
		return false
	}
	switch sub.Kind {
	case TypeDefKindFunc:
		if len(sub.Params) != len(super.Params) || len(sub.Results) != len(super.Results) {
			return false
		}
		for i := range sub.Params {
			if !isModuleValueSubtype(m, super.Params[i], sub.Params[i]) {
				return false
			}
		}
		for i := range sub.Results {
			if !isModuleValueSubtype(m, sub.Results[i], super.Results[i]) {
				return false
			}
		}
		return true
	case TypeDefKindStruct:
		if len(sub.Fields) < len(super.Fields) {
			return false
		}
		for i := range super.Fields {
			sf := sub.Fields[i]
			gf := super.Fields[i]
			if sf.Mutable != gf.Mutable || sf.Packed != gf.Packed {
				return false
			}
			if sf.Packed != PackedTypeNone {
				continue
			}
			if sf.Mutable {
				if !fieldTypesEquivalentInModule(m, sf, gf, map[typePair]bool{}, map[typePair]bool{}, map[typePair]bool{}, map[typePair]bool{}) {
					return false
				}
			} else if !isModuleValueSubtype(m, sf.Type, gf.Type) {
				return false
			}
		}
		return true
	case TypeDefKindArray:
		if sub.ElemField.Mutable != super.ElemField.Mutable || sub.ElemField.Packed != super.ElemField.Packed {
			return false
		}
		if sub.ElemField.Packed != PackedTypeNone {
			return true
		}
		if sub.ElemField.Mutable {
			return fieldTypesEquivalentInModule(m, sub.ElemField, super.ElemField, map[typePair]bool{}, map[typePair]bool{}, map[typePair]bool{}, map[typePair]bool{})
		}
		return isModuleValueSubtype(m, sub.ElemField.Type, super.ElemField.Type)
	default:
		return false
	}
}

func matchesRefTypeInModule(m *Module, got, want ValueType) bool {
	if got == want {
		return true
	}
	if !got.IsRef() || !want.IsRef() {
		return false
	}
	if got.Nullable && !want.Nullable {
		return false
	}
	switch want.HeapType.Kind {
	case HeapKindAny:
		switch got.HeapType.Kind {
		case HeapKindNone, HeapKindAny, HeapKindEq, HeapKindI31, HeapKindArray, HeapKindStruct:
			return true
		case HeapKindTypeIndex:
			return true
		default:
			return false
		}
	case HeapKindNone:
		return got.HeapType.Kind == HeapKindNone
	case HeapKindEq:
		switch got.HeapType.Kind {
		case HeapKindNone, HeapKindEq, HeapKindI31, HeapKindArray, HeapKindStruct:
			return true
		case HeapKindTypeIndex:
			return typeIndexHasKind(m, got.HeapType.TypeIndex, TypeDefKindStruct) ||
				typeIndexHasKind(m, got.HeapType.TypeIndex, TypeDefKindArray)
		default:
			return false
		}
	case HeapKindI31:
		return got.HeapType.Kind == HeapKindI31
	case HeapKindArray:
		return got.HeapType.Kind == HeapKindArray ||
			(got.HeapType.Kind == HeapKindTypeIndex && typeIndexHasKind(m, got.HeapType.TypeIndex, TypeDefKindArray))
	case HeapKindStruct:
		return got.HeapType.Kind == HeapKindStruct ||
			(got.HeapType.Kind == HeapKindTypeIndex && typeIndexHasKind(m, got.HeapType.TypeIndex, TypeDefKindStruct))
	case HeapKindFunc:
		return got.HeapType.Kind == HeapKindFunc ||
			got.HeapType.Kind == HeapKindNoFunc ||
			(got.HeapType.Kind == HeapKindTypeIndex && typeIndexHasKind(m, got.HeapType.TypeIndex, TypeDefKindFunc))
	case HeapKindExtern:
		return got.HeapType.Kind == HeapKindExtern || got.HeapType.Kind == HeapKindNoExtern
	case HeapKindNoExtern:
		return got.HeapType.Kind == HeapKindNoExtern
	case HeapKindNoFunc:
		return got.HeapType.Kind == HeapKindNoFunc
	case HeapKindTypeIndex:
		if got.HeapType.Kind == HeapKindTypeIndex {
			return isTypeIndexSubtype(m, got.HeapType.TypeIndex, want.HeapType.TypeIndex)
		}
		return (got.HeapType.Kind == HeapKindNone && want.Nullable &&
			(typeIndexHasKind(m, want.HeapType.TypeIndex, TypeDefKindStruct) ||
				typeIndexHasKind(m, want.HeapType.TypeIndex, TypeDefKindArray))) ||
			(got.HeapType.Kind == HeapKindNoFunc && want.Nullable &&
				typeIndexHasKind(m, want.HeapType.TypeIndex, TypeDefKindFunc))
	default:
		return false
	}
}

func diffRefType(src, target ValueType) ValueType {
	if !src.IsRef() || !target.IsRef() {
		return ValueType{}
	}
	if src == target && !src.Nullable {
		return ValueType{}
	}
	if src.Nullable && target.Nullable {
		out := src
		out.Nullable = false
		return out
	}
	return src
}

func matchesGCExpectedValue(m *Module, got, want ValueType) bool {
	if got.IsRef() || want.IsRef() {
		return matchesRefTypeInModule(m, got, want)
	}
	return got == want
}

func matchesExpectedValueInModule(m *Module, got, want validatedValue) bool {
	return matchesGCExpectedValue(m, got.Type, want.Type)
}

func naturalMemoryAlignExponent(kind InstrKind) (uint32, bool) {
	switch kind {
	case InstrI32Load8S, InstrI32Load8U, InstrI64Load8S, InstrI64Load8U, InstrI32Store8, InstrI64Store8:
		return 0, true
	case InstrV128Load8Splat:
		return 0, true
	case InstrI32Load16S, InstrI32Load16U, InstrI64Load16S, InstrI64Load16U, InstrI32Store16, InstrI64Store16:
		return 1, true
	case InstrV128Load16Splat:
		return 1, true
	case InstrI32Load, InstrF32Load, InstrI64Load32S, InstrI64Load32U, InstrI32Store, InstrI64Store32, InstrF32Store:
		return 2, true
	case InstrV128Load32Splat:
		return 2, true
	case InstrI64Load, InstrF64Load, InstrI64Store, InstrF64Store:
		return 3, true
	case InstrV128Load8x8S, InstrV128Load8x8U, InstrV128Load16x4S, InstrV128Load16x4U, InstrV128Load32x2S, InstrV128Load32x2U, InstrV128Load64Splat:
		return 3, true
	case InstrV128Load, InstrV128Store:
		return 4, true
	default:
		return 0, false
	}
}

// ValidateModule validates m.
// Validation includes module-level checks (type/export indices) and function
// body type checks for the currently supported instruction subset.
// It returns nil on success. On any failure, it returns diag.ErrorList.
func ValidateModule(m *Module) error {
	if m == nil {
		return diag.Fromf("module is nil")
	}

	var diags diag.ErrorList
	funcImportTypeIdx := importedFunctionTypeIndices(m)
	funcImportCount := uint32(len(funcImportTypeIdx))
	totalFuncCount := funcImportCount + uint32(len(m.Funcs))
	for i, td := range m.Types {
		for j, super := range td.SuperTypes {
			if int(super) >= len(m.Types) {
				diags.Addf("type[%d] super[%d]: unknown type", i, j)
				continue
			}
			if m.Types[super].Kind != td.Kind {
				diags.Addf("type[%d] super[%d]: type mismatch", i, j)
				continue
			}
			if isTypeFinal(m.Types[super]) || !isValidDeclaredSubtype(m, td, m.Types[super]) {
				diags.Addf("type[%d] super[%d]: sub type", i, j)
			}
		}
		switch td.Kind {
		case TypeDefKindFunc:
			for j, param := range td.Params {
				if !validModuleValueType(m, param) {
					diags.Addf("type[%d] param[%d]: unknown type", i, j)
				}
			}
			for j, result := range td.Results {
				if !validModuleValueType(m, result) {
					diags.Addf("type[%d] result[%d]: unknown type", i, j)
				}
			}
		case TypeDefKindStruct:
			for j, field := range td.Fields {
				if field.Packed == PackedTypeNone && !validModuleValueType(m, field.Type) {
					diags.Addf("type[%d] field[%d]: unknown type", i, j)
				}
			}
		case TypeDefKindArray:
			if td.ElemField.Packed == PackedTypeNone && !validModuleValueType(m, td.ElemField.Type) {
				diags.Addf("type[%d] element: unknown type", i)
			}
		}
	}
	for i, f := range m.Funcs {
		fnCtx := functionContext(m, funcImportCount+uint32(i), funcImportCount)
		if int(f.TypeIdx) >= len(m.Types) {
			diags.Addf("%s has invalid type index %d", fnCtx, f.TypeIdx)
			continue
		}
		if m.Types[f.TypeIdx].Kind != TypeDefKindFunc {
			diags.Addf("%s has non-function type index %d", fnCtx, f.TypeIdx)
			continue
		}
		funcErrs := validateFunctionBody(m, m.Types[f.TypeIdx], f, funcImportTypeIdx)
		for _, err := range funcErrs {
			diags.Addf("%s: %v", fnCtx, err)
		}
	}

	for i, g := range m.Globals {
		if g.ImportModule != "" {
			continue
		}
		initType, ok := globalInitType(m, g.Init)
		if !ok {
			diags.Addf("global[%d]: unsupported initializer", i)
			continue
		}
		if !matchesExpectedValueInModule(m, validatedValue{Type: initType}, validatedValue{Type: g.Type}) {
			diags.Addf("global[%d]: initializer type mismatch: got %s want %s", i, valueTypeName(initType), valueTypeName(g.Type))
		}
	}

	for i, table := range m.Tables {
		addrType := tableAddressType(m, uint32(i))
		if addrType == ValueTypeI32 && table.Min > maxTableElems32 {
			diags.Addf("table[%d]: table size", i)
		}
		if table.HasMax && table.Min > table.Max {
			diags.Addf("table[%d]: size minimum must not be greater than maximum", i)
		}
		if addrType == ValueTypeI32 && table.HasMax && table.Max > maxTableElems32 {
			diags.Addf("table[%d]: table size", i)
		}
		if len(table.Init) > 0 {
			initType, ok := globalInitType(m, table.Init)
			if !ok {
				diags.Addf("table[%d]: invalid initializer", i)
				continue
			}
			if !matchesExpectedValueInModule(m, validatedValue{Type: initType}, validatedValue{Type: table.RefType}) {
				diags.Addf("table[%d]: type mismatch", i)
			}
		}
	}

	for i, mem := range m.Memories {
		addrType := memoryAddressType(m, uint32(i))
		maxPages := maxMemoryPages32
		if addrType == ValueTypeI64 {
			maxPages = maxMemoryPages64
		}
		if mem.Min > maxPages {
			diags.Addf("memory[%d]: memory size", i)
		}
		if mem.HasMax {
			if mem.Max > maxPages {
				diags.Addf("memory[%d]: memory size", i)
			}
			if mem.Min > mem.Max {
				diags.Addf("memory[%d]: size minimum must not be greater than maximum", i)
			}
		}
	}

	for i, data := range m.Data {
		if data.Mode != DataSegmentModeActive {
			continue
		}
		if int(data.MemoryIndex) >= len(m.Memories) {
			diags.Addf("data[%d]: unknown memory", i)
			continue
		}
		if data.OffsetType != memoryAddressType(m, data.MemoryIndex) {
			diags.Addf("data[%d]: offset type mismatch", i)
		}
	}

	for i, f := range m.Funcs {
		for j, ins := range f.Body {
			natural, ok := naturalMemoryAlignExponent(ins.Kind)
			if ok && ins.MemoryAlign > natural {
				diags.Addf("func[%d] instruction[%d]: alignment must not be larger than natural", i, j)
			}
		}
	}

	for i, elem := range m.Elements {
		tableTy := RefTypeFunc(true)
		if elem.Mode == ElemSegmentModeActive {
			if int(elem.TableIndex) >= len(m.Tables) {
				diags.Addf("element[%d] has invalid table index %d", i, elem.TableIndex)
				continue
			}
			tableTy = m.Tables[elem.TableIndex].RefType
			if elem.OffsetType != tableAddressType(m, elem.TableIndex) {
				diags.Addf("element[%d]: offset type mismatch", i)
			}
			if len(elem.FuncIndices) > 0 && tableTy.HeapType.Kind != HeapKindFunc && tableTy.HeapType.Kind != HeapKindTypeIndex {
				diags.Addf("element[%d]: type mismatch", i)
			}
		}
		for j, funcIdx := range elem.FuncIndices {
			if funcIdx >= totalFuncCount {
				diags.Addf("element[%d] func[%d] index %d out of range", i, j, funcIdx)
			}
		}
		if len(elem.Exprs) > 0 {
			if elem.Mode == ElemSegmentModeActive &&
				!matchesExpectedValueInModule(m, validatedValue{Type: elem.RefType}, validatedValue{Type: tableTy}) {
				diags.Addf("element[%d]: type mismatch", i)
			}
			for j, expr := range elem.Exprs {
				ty, ok := globalInitType(m, expr)
				if !ok {
					diags.Addf("element[%d] expr[%d]: constant expression required", i, j)
					continue
				}
				if !matchesExpectedValueInModule(m, validatedValue{Type: ty}, validatedValue{Type: elem.RefType}) {
					diags.Addf("element[%d] expr[%d]: type mismatch", i, j)
				}
			}
		}
	}

	exportNameFirstSeen := map[string]int{}
	for i, exp := range m.Exports {
		if prev, exists := exportNameFirstSeen[exp.Name]; exists {
			diags.Addf("export[%d]: duplicate export name %q (first seen at export[%d])", i, exp.Name, prev)
		} else {
			exportNameFirstSeen[exp.Name] = i
		}
		switch exp.Kind {
		case ExternalKindFunction:
			if exp.Index >= totalFuncCount {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case ExternalKindTable:
			if int(exp.Index) >= len(m.Tables) {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case ExternalKindMemory:
			if int(exp.Index) >= len(m.Memories) {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case ExternalKindGlobal:
			if int(exp.Index) >= len(m.Globals) {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		default:
			diags.Addf("export[%d] has unsupported kind %d", i, exp.Kind)
		}
	}

	if diags.HasAny() {
		return diags
	}
	return nil
}

// validateFunctionBody validates f against function type ft.
// It returns all diagnostics found while checking instruction ordering,
// local-index bounds and stack/result typing.
func validateFunctionBody(m *Module, ft FuncType, f Function, funcImportTypeIdx []uint32) diag.ErrorList {
	var diags diag.ErrorList
	funcLocCtx := functionLocationContext(f)
	funcImportCount := uint32(len(funcImportTypeIdx))
	totalFuncCount := funcImportCount + uint32(len(m.Funcs))

	if len(f.Body) == 0 {
		diags.Addf("%sempty function body", funcLocCtx)
		return diags
	}
	if f.Body[len(f.Body)-1].Kind != InstrEnd {
		diags.Addf("%sfunction body must terminate with end", funcLocCtx)
		return diags
	}

	valueAt := func(types []ValueType, i int) validatedValue {
		return validatedValue{Type: types[i]}
	}
	valuesOf := func(types []ValueType) []validatedValue {
		out := make([]validatedValue, len(types))
		for i := range types {
			out[i] = valueAt(types, i)
		}
		return out
	}

	locals := make([]ValueType, 0, len(ft.Params)+len(f.Locals))
	locals = append(locals, ft.Params...)
	locals = append(locals, f.Locals...)
	localInitialized := make([]bool, len(locals))
	for i := range ft.Params {
		localInitialized[i] = true
	}
	for i, vt := range f.Locals {
		localInitialized[len(ft.Params)+i] = isDefaultableValueType(vt)
	}

	funcResultValues := valuesOf(ft.Results)

	stack := make([]ValueType, 0)
	stackValue := func(i int) validatedValue {
		return validatedValue{Type: stack[i]}
	}
	appendStackValue := func(v validatedValue) {
		stack = append(stack, v.Type)
	}
	appendStackType := func(vt ValueType) {
		appendStackValue(validatedValueFromType(vt))
	}
	appendStackValues := func(values []validatedValue) {
		for _, v := range values {
			appendStackValue(v)
		}
	}
	truncateStack := func(n int) {
		stack = stack[:n]
	}
	setStackValue := func(i int, v validatedValue) {
		stack[i] = v.Type
	}
	type controlKind uint8
	const (
		controlKindBlock controlKind = iota
		controlKindLoop
		controlKindIf
	)
	type controlFrame struct {
		kind        controlKind
		entryHeight int
		paramTypes  []ValueType
		resultTypes []ValueType
		sawElse     bool
		unreachable bool
	}
	var controlStack []controlFrame
	// The function body is typed under an implicit outer block whose label
	// carries the function result types. This makes top-level br/br_if/br_table
	// depth 0 target the function return arity/types.
	controlStack = append(controlStack, controlFrame{
		kind:        controlKindBlock,
		entryHeight: 0,
		resultTypes: append([]ValueType(nil), ft.Results...),
	})

	validateFrameResult := func(insCtx string, frame controlFrame, context string) {
		wantHeight := frame.entryHeight + len(frame.resultTypes)
		if len(stack) != wantHeight {
			diags.Addf("%s: %s stack height mismatch: got %d want %d", insCtx, context, len(stack), wantHeight)
			return
		}
		for i := range frame.resultTypes {
			got := stackValue(frame.entryHeight + i)
			want := valueAt(frame.resultTypes, i)
			if !matchesExpectedValueInModule(m, got, want) {
				diags.Addf("%s: %s result type mismatch at %d: got %s want %s", insCtx, context, i, validatedValueName(got), validatedValueName(want))
				return
			}
		}
	}

	validateBranchTarget := func(insCtx string, depth uint32, opName string) (controlFrame, []validatedValue, int, bool) {
		if int(depth) >= len(controlStack) {
			diags.Addf("%s: %s depth %d out of range", insCtx, opName, depth)
			return controlFrame{}, nil, 0, false
		}
		target := controlStack[len(controlStack)-1-int(depth)]
		targetTypes := target.resultTypes
		if target.kind == controlKindLoop {
			targetTypes = target.paramTypes
		}
		targetValues := valuesOf(targetTypes)
		minHeight := target.entryHeight + len(targetValues)
		if len(stack) < minHeight {
			diags.Addf("%s: %s depth %d has insufficient stack height", insCtx, opName, depth)
			return controlFrame{}, nil, 0, false
		}
		base := len(stack) - len(targetValues)
		// Branch operands must come from the current frame's operand portion.
		// Values below the current frame entry height are not available as
		// branch arguments from inside nested blocks.
		currentEntry := 0
		if len(controlStack) > 0 {
			currentEntry = controlStack[len(controlStack)-1].entryHeight
		}
		if base < currentEntry {
			diags.Addf("%s: %s depth %d has insufficient stack height", insCtx, opName, depth)
			return controlFrame{}, nil, 0, false
		}
		for i, want := range targetValues {
			got := stackValue(base + i)
			if !matchesExpectedValueInModule(m, got, want) {
				diags.Addf("%s: %s depth %d target type mismatch at %d: got %s want %s", insCtx, opName, depth, i, validatedValueName(got), validatedValueName(want))
				return controlFrame{}, nil, 0, false
			}
		}
		return target, targetValues, base, true
	}

	branchTargetTypes := func(target controlFrame) []validatedValue {
		if target.kind == controlKindLoop {
			return valuesOf(target.paramTypes)
		}
		return valuesOf(target.resultTypes)
	}

	markCurrentFrameUnreachable := func() {
		if len(controlStack) == 0 {
			return
		}
		frame := controlStack[len(controlStack)-1]
		truncateStack(frame.entryHeight)
		frame.unreachable = true
		controlStack[len(controlStack)-1] = frame
	}

	controlSignature := func(ins Instruction, insCtx, opname string) ([]ValueType, []ValueType, bool) {
		if ins.BlockTypeUsesIndex {
			if int(ins.BlockTypeIndex) >= len(m.Types) {
				diags.Addf("%s: %s has invalid block type index %d", insCtx, opname, ins.BlockTypeIndex)
				return nil, nil, false
			}
			ft := m.Types[ins.BlockTypeIndex]
			return ft.Params, ft.Results, true
		}
		if ins.BlockHasResult {
			return nil, []ValueType{ins.BlockType}, true
		}
		return nil, nil, true
	}

	validateBrOnCastStatic := func(ins Instruction, insCtx, opname string, branchOnCast bool) bool {
		if !ins.SourceRefType.IsRef() || !ins.RefType.IsRef() {
			diags.Addf("%s: type mismatch", insCtx)
			return false
		}
		if !matchesRefTypeInModule(m, ins.RefType, ins.SourceRefType) {
			diags.Addf("%s: type mismatch", insCtx)
			return false
		}
		if int(ins.BranchDepth) >= len(controlStack) {
			diags.Addf("%s: %s depth %d out of range", insCtx, opname, ins.BranchDepth)
			return false
		}
		target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
		targetValues := branchTargetTypes(target)
		if len(targetValues) == 0 {
			diags.Addf("%s: %s depth %d has insufficient stack height", insCtx, opname, ins.BranchDepth)
			return false
		}
		wantRef := targetValues[len(targetValues)-1]
		if !wantRef.Type.IsRef() {
			diags.Addf("%s: type mismatch", insCtx)
			return false
		}
		gotRef := validatedValueFromType(ins.RefType)
		if !branchOnCast {
			gotRef = validatedValueFromType(diffRefType(ins.SourceRefType, ins.RefType))
		}
		if !matchesExpectedValueInModule(m, gotRef, wantRef) {
			diags.Addf("%s: type mismatch", insCtx)
			return false
		}
		return true
	}

	returned := false
instrLoop:
	for i, ins := range f.Body {
		insCtx := fmt.Sprintf("instruction %d", i)
		if ins.SourceLoc != "" {
			insCtx = fmt.Sprintf("%s at %s", insCtx, ins.SourceLoc)
		}

		if len(controlStack) > 0 && controlStack[len(controlStack)-1].unreachable {
			switch ins.Kind {
			case InstrBrOnCast:
				validateBrOnCastStatic(ins, insCtx, "br_on_cast", true)
			case InstrBrOnCastFail:
				validateBrOnCastStatic(ins, insCtx, "br_on_cast_fail", false)
			}
			switch ins.Kind {
			case InstrBlock, InstrLoop, InstrIf:
				opname := instrName(ins.Kind)
				params, results, ok := controlSignature(ins, insCtx, opname)
				if !ok {
					continue
				}
				kind := controlKindBlock
				switch ins.Kind {
				case InstrLoop:
					kind = controlKindLoop
				case InstrIf:
					kind = controlKindIf
				}
				controlStack = append(controlStack, controlFrame{
					kind:        kind,
					entryHeight: len(stack),
					paramTypes:  params,
					resultTypes: results,
					unreachable: true,
				})
				continue
			case InstrElse:
				frame := controlStack[len(controlStack)-1]
				if frame.kind != controlKindIf {
					diags.Addf("%s: else without matching if", insCtx)
					continue
				}
				if frame.sawElse {
					diags.Addf("%s: duplicate else for if", insCtx)
					continue
				}
				truncateStack(frame.entryHeight + len(frame.paramTypes))
				frame.sawElse = true
				// Else branch is reachable even if then branch was unreachable.
				frame.unreachable = false
				controlStack[len(controlStack)-1] = frame
				continue
			case InstrEnd:
				frame := controlStack[len(controlStack)-1]
				controlStack = controlStack[:len(controlStack)-1]
				truncateStack(frame.entryHeight)
				appendStackValues(valuesOf(frame.resultTypes))
				continue
			default:
				// Unreachable code is stack-polymorphic; ignore non-structural ops.
				continue
			}
		}
		switch ins.Kind {
		case InstrNop:
			// No stack effect.
		case InstrBlock:
			params, results, ok := controlSignature(ins, insCtx, "block")
			if !ok {
				continue
			}
			if len(stack) < len(params) {
				diags.Addf("%s: block needs %d parameter operands", insCtx, len(params))
				continue
			}
			base := len(stack) - len(params)
			matched := true
			for j := range params {
				want := valueAt(params, j)
				got := stackValue(base + j)
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: block parameter %d expects %s", insCtx, j, validatedValueName(want))
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			for j := range params {
				setStackValue(base+j, valueAt(params, j))
			}
			controlStack = append(controlStack, controlFrame{
				kind:        controlKindBlock,
				entryHeight: len(stack) - len(params),
				paramTypes:  params,
				resultTypes: results,
			})
		case InstrLoop:
			params, results, ok := controlSignature(ins, insCtx, "loop")
			if !ok {
				continue
			}
			if len(stack) < len(params) {
				diags.Addf("%s: loop needs %d parameter operands", insCtx, len(params))
				continue
			}
			base := len(stack) - len(params)
			matched := true
			for j := range params {
				want := valueAt(params, j)
				got := stackValue(base + j)
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: loop parameter %d expects %s", insCtx, j, validatedValueName(want))
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			for j := range params {
				setStackValue(base+j, valueAt(params, j))
			}
			controlStack = append(controlStack, controlFrame{
				kind:        controlKindLoop,
				entryHeight: len(stack) - len(params),
				paramTypes:  params,
				resultTypes: results,
			})
		case InstrIf:
			params, results, ok := controlSignature(ins, insCtx, "if")
			if !ok {
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: if needs 1 i32 condition operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: if expects i32 condition operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 1) // pop condition
			if len(stack) < len(params) {
				diags.Addf("%s: if needs %d parameter operands", insCtx, len(params))
				continue
			}
			base := len(stack) - len(params)
			matched := true
			for j := range params {
				want := valueAt(params, j)
				got := stackValue(base + j)
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: if parameter %d expects %s", insCtx, j, validatedValueName(want))
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			for j := range params {
				setStackValue(base+j, valueAt(params, j))
			}
			controlStack = append(controlStack, controlFrame{
				kind:        controlKindIf,
				entryHeight: len(stack) - len(params),
				paramTypes:  params,
				resultTypes: results,
			})
		case InstrElse:
			if len(controlStack) == 0 {
				diags.Addf("%s: else without matching if", insCtx)
				continue
			}
			frame := controlStack[len(controlStack)-1]
			if frame.kind != controlKindIf {
				diags.Addf("%s: else without matching if", insCtx)
				continue
			}
			if frame.sawElse {
				diags.Addf("%s: duplicate else for if", insCtx)
				continue
			}
			if !frame.unreachable {
				validateFrameResult(insCtx, frame, "then-branch")
			}
			truncateStack(frame.entryHeight + len(frame.paramTypes))
			frame.sawElse = true
			frame.unreachable = false
			controlStack[len(controlStack)-1] = frame
		case InstrLocalGet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if !localInitialized[ins.LocalIndex] {
				diags.Addf("%s: uninitialized local", insCtx)
				continue
			}
			appendStackValue(valueAt(locals, int(ins.LocalIndex)))
		case InstrLocalSet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: local.set needs 1 operand", insCtx)
				continue
			}
			want := valueAt(locals, int(ins.LocalIndex))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: local.set expects %s operand", insCtx, validatedValueName(want))
				continue
			}
			localInitialized[ins.LocalIndex] = true
			truncateStack(len(stack) - 1)
		case InstrLocalTee:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: local.tee needs 1 operand", insCtx)
				continue
			}
			want := valueAt(locals, int(ins.LocalIndex))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: local.tee expects %s operand", insCtx, validatedValueName(want))
				continue
			}
			localInitialized[ins.LocalIndex] = true
			// local.tee writes local and preserves operand on stack.
		case InstrGlobalGet:
			if int(ins.GlobalIndex) >= len(m.Globals) {
				diags.Addf("%s: global index %d out of range", insCtx, ins.GlobalIndex)
				continue
			}
			appendStackValue(validatedValueFromType(m.Globals[ins.GlobalIndex].Type))
		case InstrGlobalSet:
			if int(ins.GlobalIndex) >= len(m.Globals) {
				diags.Addf("%s: global index %d out of range", insCtx, ins.GlobalIndex)
				continue
			}
			g := m.Globals[ins.GlobalIndex]
			if !g.Mutable {
				diags.Addf("%s: global.set on immutable global %d", insCtx, ins.GlobalIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: global.set needs 1 operand", insCtx)
				continue
			}
			want := validatedValueFromType(g.Type)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: global.set expects %s operand", insCtx, validatedValueName(want))
				continue
			}
			truncateStack(len(stack) - 1)
		case InstrTableGet:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: table.get needs 1 operand", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: table.get expects %s index operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(m.Tables[ins.TableIndex].RefType))
		case InstrTableSet:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: table.set needs 2 operands", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if stack[len(stack)-2] != addrType {
				diags.Addf("%s: table.set expects %s index operand", insCtx, addrType)
				continue
			}
			want := validatedValueFromType(m.Tables[ins.TableIndex].RefType)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: table.set expects %s value operand", insCtx, validatedValueName(want))
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrTableCopy:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.TableIndex)
				continue
			}
			if int(ins.SourceTableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.SourceTableIndex)
				continue
			}
			if len(stack) < 3 {
				diags.Addf("%s: table.copy needs 3 operands", insCtx)
				continue
			}
			dstTable := m.Tables[ins.TableIndex]
			srcTable := m.Tables[ins.SourceTableIndex]
			if !matchesExpectedValueInModule(m, validatedValueFromType(srcTable.RefType), validatedValueFromType(dstTable.RefType)) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			dstAddrType := tableAddressType(m, ins.TableIndex)
			srcAddrType := tableAddressType(m, ins.SourceTableIndex)
			if stack[len(stack)-3] != dstAddrType {
				diags.Addf("%s: table.copy expects %s destination index operand", insCtx, dstAddrType)
				continue
			}
			if stack[len(stack)-2] != srcAddrType {
				diags.Addf("%s: table.copy expects %s source index operand", insCtx, srcAddrType)
				continue
			}
			lenType := ValueTypeI32
			if dstAddrType == ValueTypeI64 && srcAddrType == ValueTypeI64 {
				lenType = ValueTypeI64
			}
			if stack[len(stack)-1] != lenType {
				diags.Addf("%s: table.copy expects %s length operand", insCtx, lenType)
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrTableFill:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.TableIndex)
				continue
			}
			if len(stack) < 3 {
				diags.Addf("%s: table.fill needs 3 operands", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if stack[len(stack)-3] != addrType {
				diags.Addf("%s: table.fill expects %s index operand", insCtx, addrType)
				continue
			}
			want := validatedValueFromType(m.Tables[ins.TableIndex].RefType)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), want) {
				diags.Addf("%s: table.fill expects %s value operand", insCtx, validatedValueName(want))
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: table.fill expects %s length operand", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrTableInit:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.TableIndex)
				continue
			}
			if int(ins.ElemIndex) >= len(m.Elements) {
				diags.Addf("%s: unknown elem segment %d", insCtx, ins.ElemIndex)
				continue
			}
			if len(stack) < 3 {
				diags.Addf("%s: table.init needs 3 operands", insCtx)
				continue
			}
			elemType, ok := elementRefType(m, ins.ElemIndex)
			if !ok || !matchesExpectedValueInModule(m, validatedValueFromType(elemType), validatedValueFromType(m.Tables[ins.TableIndex].RefType)) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if stack[len(stack)-3] != addrType {
				diags.Addf("%s: table.init expects %s destination index operand", insCtx, addrType)
				continue
			}
			if stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: table.init expects i32 source index operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: table.init expects i32 length operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrElemDrop:
			if int(ins.ElemIndex) >= len(m.Elements) {
				diags.Addf("%s: unknown elem segment %d", insCtx, ins.ElemIndex)
				continue
			}
		case InstrTableGrow:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: table.grow needs 2 operands", insCtx)
				continue
			}
			want := validatedValueFromType(m.Tables[ins.TableIndex].RefType)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), want) {
				diags.Addf("%s: table.grow expects %s value operand", insCtx, validatedValueName(want))
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: table.grow expects %s delta operand", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(addrType)
		case InstrTableSize:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			appendStackType(tableAddressType(m, ins.TableIndex))
		case InstrCall:
			if ins.FuncIndex >= totalFuncCount {
				diags.Addf("%s: call function index %d out of range", insCtx, ins.FuncIndex)
				continue
			}
			calleeType, calleeDef, ok := functionTypeAtIndex(m, funcImportTypeIdx, ins.FuncIndex)
			calleeCtx := functionContext(m, ins.FuncIndex, funcImportCount)
			if !ok {
				diags.Addf("%s: call target %s has invalid type index", insCtx, calleeCtx)
				continue
			}
			if len(stack) < len(calleeType.Params) {
				diags.Addf("%s: call to %s needs %d operands", insCtx, calleeCtx, len(calleeType.Params))
				continue
			}
			base := len(stack) - len(calleeType.Params)
			operandsOK := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: call to %s expects operand %s to be %s", insCtx, calleeCtx, operandLabelFromDef(calleeDef, j), validatedValueName(want))
					operandsOK = false
					break
				}
			}
			if !operandsOK {
				continue
			}
			truncateStack(base)
			appendStackValues(valuesOf(calleeType.Results))
		case InstrCallIndirect:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: call_indirect table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if int(ins.CallTypeIndex) >= len(m.Types) {
				diags.Addf("%s: call_indirect type index %d out of range", insCtx, ins.CallTypeIndex)
				continue
			}
			calleeType := m.Types[ins.CallTypeIndex]
			if calleeType.Kind != TypeDefKindFunc {
				diags.Addf("%s: call_indirect type index %d is not a function type", insCtx, ins.CallTypeIndex)
				continue
			}
			need := len(calleeType.Params) + 1 // +1 for table element index
			if len(stack) < need {
				diags.Addf("%s: call_indirect needs %d operands", insCtx, need)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: call_indirect expects %s table index operand", insCtx, addrType)
				continue
			}
			base := len(stack) - 1 - len(calleeType.Params)
			ok := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: call_indirect expects operand %d to be %s", insCtx, j, validatedValueName(want))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			truncateStack(base)
			appendStackValues(valuesOf(calleeType.Results))
		case InstrCallRef:
			if int(ins.CallTypeIndex) >= len(m.Types) {
				diags.Addf("%s: call_ref type index %d out of range", insCtx, ins.CallTypeIndex)
				continue
			}
			calleeType := m.Types[ins.CallTypeIndex]
			if calleeType.Kind != TypeDefKindFunc {
				diags.Addf("%s: call_ref type index %d is not a function type", insCtx, ins.CallTypeIndex)
				continue
			}
			need := len(calleeType.Params) + 1
			if len(stack) < need {
				diags.Addf("%s: call_ref needs %d operands", insCtx, need)
				continue
			}
			calleeRefWant := validatedValue{Type: RefTypeIndexed(ins.CallTypeIndex, false)}
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), calleeRefWant) {
				diags.Addf("%s: call_ref expects operand of type %s", insCtx, validatedValueName(calleeRefWant))
				continue
			}
			base := len(stack) - 1 - len(calleeType.Params)
			ok := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: call_ref expects operand %d to be %s", insCtx, j, validatedValueName(want))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			truncateStack(base)
			appendStackValues(valuesOf(calleeType.Results))
		case InstrStructNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.new type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindStruct {
				diags.Addf("%s: struct.new type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if len(stack) < len(td.Fields) {
				diags.Addf("%s: struct.new needs %d operands", insCtx, len(td.Fields))
				continue
			}
			base := len(stack) - len(td.Fields)
			operandsOK := true
			for j, field := range td.Fields {
				want := validatedValueFromType(fieldValueType(field))
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: struct.new field %d expects %s", insCtx, j, validatedValueName(want))
					operandsOK = false
					break
				}
			}
			if !operandsOK {
				continue
			}
			truncateStack(base)
			appendStackValue(validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrStructNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.new_default type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindStruct {
				diags.Addf("%s: struct.new_default type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			defaultable := true
			for _, field := range td.Fields {
				if !isDefaultableFieldType(field) {
					defaultable = false
					break
				}
			}
			if !defaultable {
				diags.Addf("%s: struct.new_default requires defaultable field types", insCtx)
				continue
			}
			appendStackValue(validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrStructGet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.get type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindStruct {
				diags.Addf("%s: struct.get type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.get field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: struct.get needs 1 operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(td.Fields[ins.FieldIndex].Type))
		case InstrStructGetS:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.get_s type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindStruct {
				diags.Addf("%s: struct.get_s type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.get_s field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			field := td.Fields[ins.FieldIndex]
			if field.Packed == PackedTypeNone {
				diags.Addf("%s: struct.get_s requires packed field type", insCtx)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: struct.get_s needs 1 operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get_s expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrStructGetU:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.get_u type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindStruct {
				diags.Addf("%s: struct.get_u type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.get_u field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			field := td.Fields[ins.FieldIndex]
			if field.Packed == PackedTypeNone {
				diags.Addf("%s: struct.get_u requires packed field type", insCtx)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: struct.get_u needs 1 operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get_u expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrStructSet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.set type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindStruct {
				diags.Addf("%s: struct.set type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.set field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			field := td.Fields[ins.FieldIndex]
			if !field.Mutable {
				diags.Addf("%s: immutable field", insCtx)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: struct.set needs 2 operands", insCtx)
				continue
			}
			wantValue := validatedValueFromType(fieldValueType(field))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantValue) {
				diags.Addf("%s: struct.set expects value operand of type %s", insCtx, validatedValueName(wantValue))
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: struct.set expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrArrayLen:
			if len(stack) < 1 {
				diags.Addf("%s: array.len needs 1 operand", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, RefTypeArray(true)) {
				diags.Addf("%s: array.len expects array reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrArrayNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_default type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.new_default type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !isDefaultableValueType(fieldValueType(td.ElemField)) {
				diags.Addf("%s: array.new_default requires defaultable element type", insCtx)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: array.new_default needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.new_default expects i32 length operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrArrayNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.new type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: array.new needs 2 operands", insCtx)
				continue
			}
			elemType := fieldValueType(td.ElemField)
			if !matchesGCExpectedValue(m, stackValue(len(stack)-2).Type, elemType) {
				diags.Addf("%s: array.new expects element operand of type %s", insCtx, elemType)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.new expects i32 length operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrArrayNewFixed:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_fixed type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.new_fixed type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if len(stack) < int(ins.FixedCount) {
				diags.Addf("%s: array.new_fixed needs %d operands", insCtx, ins.FixedCount)
				continue
			}
			base := len(stack) - int(ins.FixedCount)
			elemType := fieldValueType(td.ElemField)
			operandsOK := true
			for j := 0; j < int(ins.FixedCount); j++ {
				if !matchesGCExpectedValue(m, stackValue(base+j).Type, elemType) {
					diags.Addf("%s: array.new_fixed element %d expects %s", insCtx, j, elemType)
					operandsOK = false
					break
				}
			}
			if !operandsOK {
				continue
			}
			truncateStack(base)
			appendStackValue(validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrArrayNewData:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_data type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.new_data type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.DataIndex) >= len(m.Data) {
				diags.Addf("%s: array.new_data data index %d out of range", insCtx, ins.DataIndex)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: array.new_data needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.new_data expects i32 offset and i32 length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrArrayNewElem:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_elem type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.new_elem type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.ElemIndex) >= len(m.Elements) {
				diags.Addf("%s: array.new_elem element index %d out of range", insCtx, ins.ElemIndex)
				continue
			}
			elemType := elementSegmentType(m, m.Elements[ins.ElemIndex])
			if !matchesExpectedValueInModule(m, validatedValueFromType(elemType), validatedValueFromType(fieldValueType(td.ElemField))) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: array.new_elem needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.new_elem expects i32 offset and i32 length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(RefTypeIndexed(ins.TypeIndex, false)))
		case InstrArrayInitData:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.init_data type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.init_data type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !td.ElemField.Mutable {
				diags.Addf("%s: immutable array", insCtx)
				continue
			}
			if _, ok := fieldByteWidth(td.ElemField); !ok {
				diags.Addf("%s: array type is not numeric or vector", insCtx)
				continue
			}
			if int(ins.DataIndex) >= len(m.Data) {
				diags.Addf("%s: array.init_data data index %d out of range", insCtx, ins.DataIndex)
				continue
			}
			if len(stack) < 4 {
				diags.Addf("%s: array.init_data needs 4 operands", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-4), wantRef) {
				diags.Addf("%s: array.init_data expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			if stack[len(stack)-3] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.init_data expects i32 destination, source, and length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 4)
		case InstrArrayInitElem:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.init_elem type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.init_elem type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !td.ElemField.Mutable {
				diags.Addf("%s: immutable array", insCtx)
				continue
			}
			if int(ins.ElemIndex) >= len(m.Elements) {
				diags.Addf("%s: array.init_elem element index %d out of range", insCtx, ins.ElemIndex)
				continue
			}
			elemType := elementSegmentType(m, m.Elements[ins.ElemIndex])
			if !matchesExpectedValueInModule(m, validatedValueFromType(elemType), validatedValueFromType(fieldValueType(td.ElemField))) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			if len(stack) < 4 {
				diags.Addf("%s: array.init_elem needs 4 operands", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-4), wantRef) {
				diags.Addf("%s: array.init_elem expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			if stack[len(stack)-3] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.init_elem expects i32 destination, source, and length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 4)
		case InstrArrayGet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.get type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.get type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: array.get needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.get expects i32 index operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: array.get expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(fieldValueType(td.ElemField)))
		case InstrArrayGetS, InstrArrayGetU:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: %s type index %d out of range", insCtx, instrName(ins.Kind), ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: %s type index %d is not an array type", insCtx, instrName(ins.Kind), ins.TypeIndex)
				continue
			}
			if td.ElemField.Packed == PackedTypeNone {
				diags.Addf("%s: %s requires packed array element type", insCtx, instrName(ins.Kind))
				continue
			}
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 index operand", insCtx, instrName(ins.Kind))
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: %s expects operand of type %s", insCtx, instrName(ins.Kind), validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(ValueTypeI32))
		case InstrArraySet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.set type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.set type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !td.ElemField.Mutable {
				diags.Addf("%s: array.set requires mutable array", insCtx)
				continue
			}
			if len(stack) < 3 {
				diags.Addf("%s: array.set needs 3 operands", insCtx)
				continue
			}
			wantValue := validatedValueFromType(fieldValueType(td.ElemField))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantValue) {
				diags.Addf("%s: array.set expects value operand of type %s", insCtx, validatedValueName(wantValue))
				continue
			}
			if stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: array.set expects i32 index operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-3), wantRef) {
				diags.Addf("%s: array.set expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrArrayFill:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.fill type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != TypeDefKindArray {
				diags.Addf("%s: array.fill type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !td.ElemField.Mutable {
				diags.Addf("%s: immutable array", insCtx)
				continue
			}
			if len(stack) < 4 {
				diags.Addf("%s: array.fill needs 4 operands", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-4), wantRef) {
				diags.Addf("%s: array.fill expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			if stack[len(stack)-3] != ValueTypeI32 {
				diags.Addf("%s: array.fill expects i32 index operand", insCtx)
				continue
			}
			wantValue := validatedValueFromType(fieldValueType(td.ElemField))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantValue) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.fill expects i32 length operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 4)
		case InstrArrayCopy:
			dstType, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.copy destination type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if dstType.Kind != TypeDefKindArray {
				diags.Addf("%s: array.copy destination type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			srcType, ok := typeDefAtIndex(m, ins.SourceTypeIndex)
			if !ok {
				diags.Addf("%s: array.copy source type index %d out of range", insCtx, ins.SourceTypeIndex)
				continue
			}
			if srcType.Kind != TypeDefKindArray {
				diags.Addf("%s: array.copy source type index %d is not an array type", insCtx, ins.SourceTypeIndex)
				continue
			}
			if !dstType.ElemField.Mutable {
				diags.Addf("%s: immutable array", insCtx)
				continue
			}
			if !sameFieldStorage(dstType.ElemField, srcType.ElemField) {
				diags.Addf("%s: array types do not match", insCtx)
				continue
			}
			if len(stack) < 5 {
				diags.Addf("%s: array.copy needs 5 operands", insCtx)
				continue
			}
			dstWant := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-5), dstWant) {
				diags.Addf("%s: array.copy expects destination operand of type %s", insCtx, validatedValueName(dstWant))
				continue
			}
			if stack[len(stack)-4] != ValueTypeI32 {
				diags.Addf("%s: array.copy expects i32 destination index operand", insCtx)
				continue
			}
			srcWant := validatedValueFromType(RefTypeIndexed(ins.SourceTypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-3), srcWant) {
				diags.Addf("%s: array.copy expects source operand of type %s", insCtx, validatedValueName(srcWant))
				continue
			}
			if stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: array.copy expects i32 source index and length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 5)
		case InstrRefEq:
			if len(stack) < 2 {
				diags.Addf("%s: ref.eq needs 2 operands", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-2).Type, RefTypeEq(true)) ||
				!matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, RefTypeEq(true)) {
				diags.Addf("%s: ref.eq expects eqref operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(ValueTypeI32))
		case InstrRefTest:
			if len(stack) < 1 {
				diags.Addf("%s: ref.test needs 1 operand", insCtx)
				continue
			}
			if !stackValue(len(stack) - 1).Type.IsRef() {
				diags.Addf("%s: ref.test expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrRefCast:
			if len(stack) < 1 {
				diags.Addf("%s: ref.cast needs 1 operand", insCtx)
				continue
			}
			if !stackValue(len(stack) - 1).Type.IsRef() {
				diags.Addf("%s: ref.cast expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ins.RefType))
		case InstrRefI31:
			if len(stack) < 1 {
				diags.Addf("%s: ref.i31 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: ref.i31 expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(RefTypeI31(false)))
		case InstrExternConvertAny:
			if len(stack) < 1 {
				diags.Addf("%s: extern.convert_any needs 1 operand", insCtx)
				continue
			}
			got := stackValue(len(stack) - 1)
			if !matchesGCExpectedValue(m, got.Type, RefTypeAny(true)) {
				diags.Addf("%s: extern.convert_any expects any reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(RefTypeExtern(got.Type.Nullable)))
		case InstrAnyConvertExtern:
			if len(stack) < 1 {
				diags.Addf("%s: any.convert_extern needs 1 operand", insCtx)
				continue
			}
			got := stackValue(len(stack) - 1)
			if !matchesGCExpectedValue(m, got.Type, RefTypeExtern(true)) {
				diags.Addf("%s: any.convert_extern expects extern reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(RefTypeAny(got.Type.Nullable)))
		case InstrI31GetS:
			if len(stack) < 1 {
				diags.Addf("%s: i31.get_s needs 1 operand", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, RefTypeI31(true)) {
				diags.Addf("%s: i31.get_s expects i31 reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrI31GetU:
			if len(stack) < 1 {
				diags.Addf("%s: i31.get_u needs 1 operand", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, RefTypeI31(true)) {
				diags.Addf("%s: i31.get_u expects i31 reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))

		case InstrI32Const:
			appendStackType(ValueTypeI32)

		case InstrI64Const:
			appendStackType(ValueTypeI64)

		case InstrF32Const:
			appendStackType(ValueTypeF32)

		case InstrF64Const:
			appendStackType(ValueTypeF64)

		case InstrV128Const:
			appendStackType(ValueTypeV128)

		case InstrDrop:
			if len(stack) < 1 {
				diags.Addf("%s: drop needs 1 operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 1)
		case InstrSelect:
			if len(stack) < 3 {
				diags.Addf("%s: select needs 3 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: select expects i32 condition operand", insCtx)
				continue
			}
			v2 := stackValue(len(stack) - 2)
			v1 := stackValue(len(stack) - 3)
			if !sameValidatedValue(v1, v2) {
				diags.Addf("%s: select expects same-typed value operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 3)
			appendStackValue(v1)
		case InstrI32Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i32.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i32.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: i32.load needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: i32.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrI64Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i64.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i64.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: i64.load needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: i64.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI64))
		case InstrF32Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f32.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f32.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: f32.load needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: f32.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeF32))
		case InstrF64Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f64.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f64.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: f64.load needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: f64.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeF64))
		case InstrV128Load, InstrV128Load8x8S, InstrV128Load8x8U, InstrV128Load16x4S, InstrV128Load16x4U, InstrV128Load32x2S, InstrV128Load32x2U, InstrV128Load8Splat, InstrV128Load16Splat, InstrV128Load32Splat, InstrV128Load64Splat:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, instrName(ins.Kind))
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: %s expects %s address operand", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeV128))
		case InstrI32Load8S, InstrI32Load8U, InstrI32Load16S, InstrI32Load16U:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, instrName(ins.Kind))
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: %s expects %s address operand", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrI64Load8S, InstrI64Load8U, InstrI64Load16S, InstrI64Load16U, InstrI64Load32S, InstrI64Load32U:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, instrName(ins.Kind))
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: %s expects %s address operand", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI64))
		case InstrI32Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i32.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i32.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: i32.store needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: i32.store expects i32 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrI64Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i64.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i64.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: i64.store needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: i64.store expects i64 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrI32Store8, InstrI32Store16:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: %s expects i32 value and %s address operands", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrI64Store8, InstrI64Store16, InstrI64Store32:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: %s expects i64 value and %s address operands", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrF32Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f32.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f32.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: f32.store needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: f32.store expects f32 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrF64Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f64.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f64.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: f64.store needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF64 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: f64.store expects f64 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrV128Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: v128.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: v128.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 2 {
				diags.Addf("%s: v128.store needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 || stack[len(stack)-2] != addrType {
				diags.Addf("%s: v128.store expects v128 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case InstrMemorySize:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.size requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.size memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			appendStackType(memoryAddressType(m, ins.MemoryIndex))
		case InstrMemoryGrow:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.grow requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.grow memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 1 {
				diags.Addf("%s: memory.grow needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != addrType {
				diags.Addf("%s: memory.grow expects %s operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(addrType))
		case InstrMemoryCopy:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.copy requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.copy destination memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			if int(ins.SourceMemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.copy source memory index %d out of range", insCtx, ins.SourceMemoryIndex)
				continue
			}
			if len(stack) < 3 {
				diags.Addf("%s: memory.copy needs 3 operands", insCtx)
				continue
			}
			dstAddrType := memoryAddressType(m, ins.MemoryIndex)
			srcAddrType := memoryAddressType(m, ins.SourceMemoryIndex)
			if stack[len(stack)-3] != dstAddrType || stack[len(stack)-2] != srcAddrType || stack[len(stack)-1] != dstAddrType {
				diags.Addf("%s: memory.copy expects %s destination, %s source, and %s length operands", insCtx, dstAddrType, srcAddrType, dstAddrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrMemoryInit:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.init requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.init memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			if int(ins.DataIndex) >= len(m.Data) {
				diags.Addf("%s: memory.init data index %d out of range", insCtx, ins.DataIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 3 {
				diags.Addf("%s: memory.init needs 3 operands", insCtx)
				continue
			}
			if stack[len(stack)-3] != addrType || stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: memory.init expects %s destination, i32 source, and i32 length operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrMemoryFill:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.fill requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.fill memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if len(stack) < 3 {
				diags.Addf("%s: memory.fill needs 3 operands", insCtx)
				continue
			}
			if stack[len(stack)-3] != addrType || stack[len(stack)-2] != ValueTypeI32 || stack[len(stack)-1] != addrType {
				diags.Addf("%s: memory.fill expects %s destination, i32 value, and %s length operands", insCtx, addrType, addrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case InstrDataDrop:
			if int(ins.DataIndex) >= len(m.Data) {
				diags.Addf("%s: data.drop data index %d out of range", insCtx, ins.DataIndex)
				continue
			}
		case InstrBr:
			target, _, _, ok := validateBranchTarget(insCtx, ins.BranchDepth, "br")
			if !ok {
				continue
			}
			_ = target
			markCurrentFrameUnreachable()
		case InstrBrIf:
			if len(stack) < 1 {
				diags.Addf("%s: br_if needs 1 i32 condition operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: br_if expects i32 condition operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 1)
			_, targetValues, base, ok := validateBranchTarget(insCtx, ins.BranchDepth, "br_if")
			if ok {
				for i, v := range targetValues {
					setStackValue(base+i, v)
				}
			}
		case InstrBrOnNull:
			if len(stack) < 1 {
				diags.Addf("%s: br_on_null needs 1 reference operand", insCtx)
				continue
			}
			refVal := stackValue(len(stack) - 1)
			if !isRefValueType(refVal.Type) {
				diags.Addf("%s: br_on_null expects reference operand", insCtx)
				continue
			}
			if int(ins.BranchDepth) >= len(controlStack) {
				diags.Addf("%s: br_on_null depth %d out of range", insCtx, ins.BranchDepth)
				continue
			}
			target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
			targetValues := branchTargetTypes(target)
			if len(stack) < len(targetValues)+1 {
				diags.Addf("%s: br_on_null depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			base := len(stack) - 1 - len(targetValues)
			currentEntry := 0
			if len(controlStack) > 0 {
				currentEntry = controlStack[len(controlStack)-1].entryHeight
			}
			if base < currentEntry {
				diags.Addf("%s: br_on_null depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			matches := true
			for i, want := range targetValues {
				got := stackValue(base + i)
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: br_on_null depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, i, validatedValueName(got), validatedValueName(want))
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			for i, v := range targetValues {
				setStackValue(base+i, v)
			}
			truncateStack(len(stack) - 1)
			appendStackValue(refinedNonNullValue(refVal))
		case InstrBrOnNonNull:
			if len(stack) < 1 {
				diags.Addf("%s: br_on_non_null needs 1 reference operand", insCtx)
				continue
			}
			refVal := stackValue(len(stack) - 1)
			if !isRefValueType(refVal.Type) {
				diags.Addf("%s: br_on_non_null expects reference operand", insCtx)
				continue
			}
			if int(ins.BranchDepth) >= len(controlStack) {
				diags.Addf("%s: br_on_non_null depth %d out of range", insCtx, ins.BranchDepth)
				continue
			}
			target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
			targetValues := branchTargetTypes(target)
			if len(targetValues) == 0 || len(stack) < len(targetValues) {
				diags.Addf("%s: br_on_non_null depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			base := len(stack) - len(targetValues)
			currentEntry := 0
			if len(controlStack) > 0 {
				currentEntry = controlStack[len(controlStack)-1].entryHeight
			}
			if base < currentEntry {
				diags.Addf("%s: br_on_non_null depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			matches := true
			for i := 0; i < len(targetValues)-1; i++ {
				got := stackValue(base + i)
				want := targetValues[i]
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: br_on_non_null depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, i, validatedValueName(got), validatedValueName(want))
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			wantRef := targetValues[len(targetValues)-1]
			gotRef := refinedNonNullValue(refVal)
			if !matchesExpectedValueInModule(m, gotRef, wantRef) {
				diags.Addf("%s: br_on_non_null depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, len(targetValues)-1, validatedValueName(gotRef), validatedValueName(wantRef))
				continue
			}
			for i := 0; i < len(targetValues)-1; i++ {
				setStackValue(base+i, targetValues[i])
			}
			truncateStack(len(stack) - 1)
		case InstrBrOnCast:
			if len(stack) < 1 {
				diags.Addf("%s: br_on_cast needs 1 reference operand", insCtx)
				continue
			}
			refVal := stackValue(len(stack) - 1)
			if !matchesExpectedValueInModule(m, refVal, validatedValueFromType(ins.SourceRefType)) {
				diags.Addf("%s: br_on_cast expects operand of type %s", insCtx, validatedValueName(validatedValueFromType(ins.SourceRefType)))
				continue
			}
			if !matchesRefTypeInModule(m, ins.RefType, ins.SourceRefType) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			if int(ins.BranchDepth) >= len(controlStack) {
				diags.Addf("%s: br_on_cast depth %d out of range", insCtx, ins.BranchDepth)
				continue
			}
			target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
			targetValues := branchTargetTypes(target)
			if len(targetValues) == 0 || len(stack) < len(targetValues) {
				diags.Addf("%s: br_on_cast depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			base := len(stack) - len(targetValues)
			currentEntry := 0
			if len(controlStack) > 0 {
				currentEntry = controlStack[len(controlStack)-1].entryHeight
			}
			if base < currentEntry {
				diags.Addf("%s: br_on_cast depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			matches := true
			for i := 0; i < len(targetValues)-1; i++ {
				got := stackValue(base + i)
				want := targetValues[i]
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: br_on_cast depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, i, validatedValueName(got), validatedValueName(want))
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			wantRef := targetValues[len(targetValues)-1]
			if !matchesExpectedValueInModule(m, validatedValueFromType(ins.RefType), wantRef) {
				diags.Addf("%s: br_on_cast depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, len(targetValues)-1, validatedValueName(validatedValueFromType(ins.RefType)), validatedValueName(wantRef))
				continue
			}
			for i := 0; i < len(targetValues)-1; i++ {
				setStackValue(base+i, targetValues[i])
			}
			setStackValue(len(stack)-1, validatedValueFromType(diffRefType(ins.SourceRefType, ins.RefType)))
		case InstrBrOnCastFail:
			if len(stack) < 1 {
				diags.Addf("%s: br_on_cast_fail needs 1 reference operand", insCtx)
				continue
			}
			refVal := stackValue(len(stack) - 1)
			if !matchesExpectedValueInModule(m, refVal, validatedValueFromType(ins.SourceRefType)) {
				diags.Addf("%s: br_on_cast_fail expects operand of type %s", insCtx, validatedValueName(validatedValueFromType(ins.SourceRefType)))
				continue
			}
			if !matchesRefTypeInModule(m, ins.RefType, ins.SourceRefType) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			if int(ins.BranchDepth) >= len(controlStack) {
				diags.Addf("%s: br_on_cast_fail depth %d out of range", insCtx, ins.BranchDepth)
				continue
			}
			target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
			targetValues := branchTargetTypes(target)
			if len(targetValues) == 0 || len(stack) < len(targetValues) {
				diags.Addf("%s: br_on_cast_fail depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			base := len(stack) - len(targetValues)
			currentEntry := 0
			if len(controlStack) > 0 {
				currentEntry = controlStack[len(controlStack)-1].entryHeight
			}
			if base < currentEntry {
				diags.Addf("%s: br_on_cast_fail depth %d has insufficient stack height", insCtx, ins.BranchDepth)
				continue
			}
			matches := true
			for i := 0; i < len(targetValues)-1; i++ {
				got := stackValue(base + i)
				want := targetValues[i]
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: br_on_cast_fail depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, i, validatedValueName(got), validatedValueName(want))
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			wantRef := targetValues[len(targetValues)-1]
			diffType := diffRefType(ins.SourceRefType, ins.RefType)
			if !matchesExpectedValueInModule(m, validatedValueFromType(diffType), wantRef) {
				diags.Addf("%s: br_on_cast_fail depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, len(targetValues)-1, validatedValueName(validatedValueFromType(diffType)), validatedValueName(wantRef))
				continue
			}
			for i := 0; i < len(targetValues)-1; i++ {
				setStackValue(base+i, targetValues[i])
			}
			setStackValue(len(stack)-1, validatedValueFromType(ins.RefType))
		case InstrBrTable:
			if len(stack) < 1 {
				diags.Addf("%s: br_table needs 1 i32 selector operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: br_table expects i32 selector operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 1)

			targetDepths := make([]uint32, 0, len(ins.BranchTable)+1)
			targetDepths = append(targetDepths, ins.BranchTable...)
			targetDepths = append(targetDepths, ins.BranchDefault)
			if len(targetDepths) == 0 {
				diags.Addf("%s: br_table requires at least default target", insCtx)
				continue
			}
			target, targetValues, base, ok := validateBranchTarget(insCtx, targetDepths[0], "br_table")
			if ok {
				for _, depth := range targetDepths[1:] {
					if int(depth) >= len(controlStack) {
						diags.Addf("%s: br_table depth %d out of range", insCtx, depth)
						continue
					}
					otherTarget := controlStack[len(controlStack)-1-int(depth)]
					otherValues := branchTargetTypes(otherTarget)
					if len(otherValues) != len(targetValues) {
						diags.Addf("%s: br_table target arity mismatch", insCtx)
						continue
					}
					for k, want := range otherValues {
						got := stackValue(base + k)
						if !matchesExpectedValueInModule(m, got, want) {
							diags.Addf("%s: br_table depth %d target type mismatch at %d: got %s want %s", insCtx, depth, k, validatedValueName(got), validatedValueName(want))
							break
						}
					}
				}
				_ = target
				markCurrentFrameUnreachable()
			}
		case InstrUnreachable:
			if len(controlStack) == 0 {
				// Top-level unreachable makes the rest of the function unreachable.
				truncateStack(0)
				appendStackValues(funcResultValues)
				returned = true
				break instrLoop
			}
			markCurrentFrameUnreachable()
		case InstrReturn:
			if len(stack) < len(ft.Results) {
				diags.Addf("%s: return needs %d operands", insCtx, len(ft.Results))
				continue
			}
			base := len(stack) - len(ft.Results)
			ok := true
			for j := range ft.Results {
				want := valueAt(ft.Results, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: return expects result %d to be %s", insCtx, j, validatedValueName(want))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			truncateStack(0)
			appendStackValues(funcResultValues)
			returned = true
			break instrLoop

		case InstrI32Add, InstrI32Sub, InstrI32Mul, InstrI32DivS, InstrI32DivU,
			InstrI32RemS, InstrI32RemU, InstrI32Shl, InstrI32ShrS, InstrI32ShrU,
			InstrI32And, InstrI32Or, InstrI32Xor, InstrI32Rotl, InstrI32Rotr:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeI32)

		case InstrI32Eq, InstrI32Ne, InstrI32LtS, InstrI32LtU, InstrI32LeS, InstrI32LeU,
			InstrI32GtS, InstrI32GtU, InstrI32GeS, InstrI32GeU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeI32)
		case InstrI32Eqz:
			if len(stack) < 1 {
				diags.Addf("%s: i32.eqz needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: i32.eqz expects i32 operand", insCtx)
				continue
			}
			// i32.eqz replaces i32 with i32 at top-of-stack.
		case InstrI32Clz, InstrI32Ctz, InstrI32Popcnt, InstrI32Extend8S, InstrI32Extend16S:
			name := instrName(ins.Kind)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 operand", insCtx, name)
				continue
			}
			// i32 unary operators preserve i32 on stack.

		case InstrI64Add, InstrI64Sub, InstrI64Mul, InstrI64DivS, InstrI64DivU,
			InstrI64RemS, InstrI64RemU, InstrI64Shl, InstrI64ShrS, InstrI64ShrU,
			InstrI64And, InstrI64Or, InstrI64Xor, InstrI64Rotl, InstrI64Rotr:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != ValueTypeI64 {
				diags.Addf("%s: %s expects i64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeI64)

		case InstrI64Eq, InstrI64Ne, InstrI64LtS, InstrI64LtU, InstrI64GtS, InstrI64GtU,
			InstrI64LeS, InstrI64LeU, InstrI64GeS, InstrI64GeU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != ValueTypeI64 {
				diags.Addf("%s: %s expects i64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeI32)
		case InstrI64Eqz:
			if len(stack) < 1 {
				diags.Addf("%s: i64.eqz needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: i64.eqz expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrI64Clz, InstrI64Ctz, InstrI64Popcnt, InstrI64Extend8S, InstrI64Extend16S, InstrI64Extend32S:
			name := instrName(ins.Kind)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: %s expects i64 operand", insCtx, name)
				continue
			}
			// i64 unary operators preserve i64 on stack.

		case InstrI32WrapI64:
			if len(stack) < 1 {
				diags.Addf("%s: i32.wrap_i64 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: i32.wrap_i64 expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))

		case InstrI64ExtendI32S, InstrI64ExtendI32U:
			name := instrName(ins.Kind)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 operand", insCtx, name)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI64))

		case InstrF32ConvertI32S:
			if len(stack) < 1 {
				diags.Addf("%s: f32.convert_i32_s needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: f32.convert_i32_s expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeF32))

		case InstrF64ConvertI64S:
			if len(stack) < 1 {
				diags.Addf("%s: f64.convert_i64_s needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: f64.convert_i64_s expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeF64))

		case InstrF32Add, InstrF32Sub, InstrF32Mul, InstrF32Div, InstrF32Min, InstrF32Max:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 || stack[len(stack)-2] != ValueTypeF32 {
				diags.Addf("%s: %s expects f32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeF32)

		case InstrF32Sqrt, InstrF32Ceil, InstrF32Floor, InstrF32Trunc, InstrF32Nearest, InstrF32Neg:
			name := instrName(ins.Kind)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 {
				diags.Addf("%s: %s expects f32 operand", insCtx, name)
				continue
			}
			// Unary f32 operators preserve top-of-stack type.
		case InstrF32Eq, InstrF32Lt, InstrF32Gt, InstrF32Ne:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 || stack[len(stack)-2] != ValueTypeF32 {
				diags.Addf("%s: %s expects f32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeI32)

		case InstrF64Add, InstrF64Sub, InstrF64Mul, InstrF64Div, InstrF64Min, InstrF64Max:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF64 || stack[len(stack)-2] != ValueTypeF64 {
				diags.Addf("%s: %s expects f64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeF64)

		case InstrF64Sqrt, InstrF64Ceil, InstrF64Floor, InstrF64Trunc, InstrF64Nearest, InstrF64Neg:
			name := instrName(ins.Kind)
			if len(stack) < 1 {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF64 {
				diags.Addf("%s: %s expects f64 operand", insCtx, name)
				continue
			}
			// Unary f64 operators preserve top-of-stack type.
		case InstrF64Eq, InstrF64Le:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF64 || stack[len(stack)-2] != ValueTypeF64 {
				diags.Addf("%s: %s expects f64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeI32)
		case InstrI8x16Swizzle:
			if len(stack) < 2 {
				diags.Addf("%s: i8x16.swizzle needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 || stack[len(stack)-2] != ValueTypeV128 {
				diags.Addf("%s: i8x16.swizzle expects v128 operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeV128)
		case InstrV128Not:
			if len(stack) < 1 {
				diags.Addf("%s: v128.not needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 {
				diags.Addf("%s: v128.not expects v128 operand", insCtx)
				continue
			}
		case InstrV128And, InstrV128AndNot, InstrV128Or, InstrV128Xor:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 || stack[len(stack)-2] != ValueTypeV128 {
				diags.Addf("%s: %s expects v128 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeV128)
		case InstrI8x16Shl, InstrI8x16ShrS, InstrI8x16ShrU,
			InstrI16x8Shl, InstrI16x8ShrS, InstrI16x8ShrU,
			InstrI32x4Shl, InstrI32x4ShrS, InstrI32x4ShrU,
			InstrI64x2Shl, InstrI64x2ShrS, InstrI64x2ShrU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeV128 {
				diags.Addf("%s: %s expects v128 and i32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeV128)
		case InstrI32x4Splat:
			if len(stack) < 1 {
				diags.Addf("%s: i32x4.splat needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: i32x4.splat expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeV128))
		case InstrI32x4ExtractLane:
			if ins.LaneIndex >= 4 {
				diags.Addf("%s: i32x4.extract_lane lane %d out of range", insCtx, ins.LaneIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: i32x4.extract_lane needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 {
				diags.Addf("%s: i32x4.extract_lane expects v128 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrI32x4Eq, InstrI32x4LtS, InstrI32x4Add, InstrI32x4MinS, InstrF32x4Add:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 || stack[len(stack)-2] != ValueTypeV128 {
				diags.Addf("%s: %s expects v128 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(ValueTypeV128)
		case InstrI32x4Neg:
			if len(stack) < 1 {
				diags.Addf("%s: i32x4.neg needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 {
				diags.Addf("%s: i32x4.neg expects v128 operand", insCtx)
				continue
			}
		case InstrV128Bitselect:
			if len(stack) < 3 {
				diags.Addf("%s: v128.bitselect needs 3 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeV128 || stack[len(stack)-2] != ValueTypeV128 || stack[len(stack)-3] != ValueTypeV128 {
				diags.Addf("%s: v128.bitselect expects v128 operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 3)
			appendStackType(ValueTypeV128)
		case InstrI32ReinterpretF32:
			if len(stack) < 1 {
				diags.Addf("%s: i32.reinterpret_f32 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF32 {
				diags.Addf("%s: i32.reinterpret_f32 expects f32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrI64ReinterpretF64:
			if len(stack) < 1 {
				diags.Addf("%s: i64.reinterpret_f64 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeF64 {
				diags.Addf("%s: i64.reinterpret_f64 expects f64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI64))
		case InstrF32ReinterpretI32:
			if len(stack) < 1 {
				diags.Addf("%s: f32.reinterpret_i32 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: f32.reinterpret_i32 expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeF32))
		case InstrF64ReinterpretI64:
			if len(stack) < 1 {
				diags.Addf("%s: f64.reinterpret_i64 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: f64.reinterpret_i64 expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeF64))
		case InstrRefNull:
			appendStackValue(validatedValue{Type: ins.RefType})
		case InstrRefFunc:
			if ins.FuncIndex >= totalFuncCount {
				diags.Addf("%s: ref.func function index %d out of range", insCtx, ins.FuncIndex)
				continue
			}
			typeIdx, ok := functionTypeIndexAtIndex(m, funcImportTypeIdx, ins.FuncIndex)
			if !ok {
				diags.Addf("%s: ref.func function index %d has invalid type", insCtx, ins.FuncIndex)
				continue
			}
			appendStackValue(validatedValue{Type: RefTypeIndexed(typeIdx, false)})
		case InstrRefIsNull:
			if len(stack) < 1 {
				diags.Addf("%s: ref.is_null needs 1 operand", insCtx)
				continue
			}
			top := stackValue(len(stack) - 1)
			if !isRefValueType(top.Type) {
				diags.Addf("%s: ref.is_null expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ValueTypeI32))
		case InstrRefAsNonNull:
			if len(stack) < 1 {
				diags.Addf("%s: ref.as_non_null needs 1 operand", insCtx)
				continue
			}
			top := stackValue(len(stack) - 1)
			if !isRefValueType(top.Type) {
				diags.Addf("%s: ref.as_non_null expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, refinedNonNullValue(top))

		case InstrEnd:
			if len(controlStack) > 0 {
				frame := controlStack[len(controlStack)-1]
				controlStack = controlStack[:len(controlStack)-1]
				if !frame.unreachable {
					switch frame.kind {
					case controlKindIf:
						validateFrameResult(insCtx, frame, "if-branch")
						if len(frame.resultTypes) > 0 && !frame.sawElse &&
							!equalValueTypeSlices(frame.paramTypes, frame.resultTypes) {
							diags.Addf("%s: if with result requires else branch", insCtx)
						}
					case controlKindBlock:
						validateFrameResult(insCtx, frame, "block")
					case controlKindLoop:
						validateFrameResult(insCtx, frame, "loop")
					}
				}

				truncateStack(frame.entryHeight)
				appendStackValues(valuesOf(frame.resultTypes))
				continue
			}
			if i != len(f.Body)-1 {
				diags.Addf("%s: end must be last", insCtx)
			}

		default:
			diags.Addf("%s: unsupported instruction kind %d", insCtx, ins.Kind)
		}
	}
	if returned {
		return diags
	}
	if len(controlStack) > 0 {
		diags.Addf("%sunterminated control construct: missing end", funcLocCtx)
	}

	if len(stack) != len(ft.Results) {
		diags.Addf("%sresult arity mismatch: got %d stack values, want %d", funcLocCtx, len(stack), len(ft.Results))
		return diags
	}
	for i := range stack {
		got := stackValue(i)
		want := valueAt(ft.Results, i)
		if !matchesExpectedValueInModule(m, got, want) {
			diags.Addf("%sresult type mismatch at %d: got %s want %s", funcLocCtx, i, validatedValueName(got), validatedValueName(want))
		}
	}

	return diags
}

func functionContext(m *Module, funcIdx uint32, funcImportCount uint32) string {
	ctx := fmt.Sprintf("func[%d]", funcIdx)
	if funcIdx >= moduleFunctionCount(m) {
		return ctx
	}
	if funcIdx >= funcImportCount {
		defIdx := funcIdx - funcImportCount
		if int(defIdx) < len(m.Funcs) {
			if name := m.Funcs[defIdx].Name; name != "" {
				return ctx + " " + name
			}
		}
	}
	if exportName, ok := functionExportName(m, funcIdx); ok {
		return fmt.Sprintf(`%s export %q`, ctx, exportName)
	}
	return ctx
}

func importedFunctionTypeIndices(m *Module) []uint32 {
	out := make([]uint32, 0, len(m.Imports))
	for _, imp := range m.Imports {
		if imp.Kind == ExternalKindFunction {
			out = append(out, imp.TypeIdx)
		}
	}
	return out
}

func moduleFunctionCount(m *Module) uint32 {
	var count uint32 = uint32(len(m.Funcs))
	for _, imp := range m.Imports {
		if imp.Kind == ExternalKindFunction {
			count++
		}
	}
	return count
}

func functionTypeAtIndex(m *Module, funcImportTypeIdx []uint32, funcIdx uint32) (FuncType, *Function, bool) {
	importCount := uint32(len(funcImportTypeIdx))
	if funcIdx < importCount {
		typeIdx := funcImportTypeIdx[funcIdx]
		if int(typeIdx) >= len(m.Types) {
			return FuncType{}, nil, false
		}
		if m.Types[typeIdx].Kind != TypeDefKindFunc {
			return FuncType{}, nil, false
		}
		return m.Types[typeIdx], nil, true
	}
	defIdx := funcIdx - importCount
	if int(defIdx) >= len(m.Funcs) {
		return FuncType{}, nil, false
	}
	def := &m.Funcs[defIdx]
	if int(def.TypeIdx) >= len(m.Types) {
		return FuncType{}, nil, false
	}
	if m.Types[def.TypeIdx].Kind != TypeDefKindFunc {
		return FuncType{}, nil, false
	}
	return m.Types[def.TypeIdx], def, true
}

func functionTypeIndexAtIndex(m *Module, funcImportTypeIdx []uint32, funcIdx uint32) (uint32, bool) {
	importCount := uint32(len(funcImportTypeIdx))
	if funcIdx < importCount {
		typeIdx := funcImportTypeIdx[funcIdx]
		if int(typeIdx) >= len(m.Types) {
			return 0, false
		}
		if m.Types[typeIdx].Kind != TypeDefKindFunc {
			return 0, false
		}
		return typeIdx, true
	}
	defIdx := funcIdx - importCount
	if int(defIdx) >= len(m.Funcs) {
		return 0, false
	}
	typeIdx := m.Funcs[defIdx].TypeIdx
	if int(typeIdx) >= len(m.Types) {
		return 0, false
	}
	if m.Types[typeIdx].Kind != TypeDefKindFunc {
		return 0, false
	}
	return typeIdx, true
}

func functionExportName(m *Module, funcIdx uint32) (string, bool) {
	for _, exp := range m.Exports {
		if exp.Kind == ExternalKindFunction && exp.Index == funcIdx {
			return exp.Name, true
		}
	}
	return "", false
}

func functionLocationContext(f Function) string {
	if f.SourceLoc == "" {
		return ""
	}
	return "at " + f.SourceLoc + ": "
}

func operandLabel(callee Function, operandIndex int) string {
	if operandIndex < len(callee.ParamNames) && callee.ParamNames[operandIndex] != "" {
		return fmt.Sprintf("%d (%s)", operandIndex, callee.ParamNames[operandIndex])
	}
	return fmt.Sprintf("%d", operandIndex)
}

func operandLabelFromDef(callee *Function, operandIndex int) string {
	if callee == nil {
		return fmt.Sprintf("%d", operandIndex)
	}
	return operandLabel(*callee, operandIndex)
}

func valueTypeName(vt ValueType) string {
	return vt.String()
}

func globalInitType(m *Module, init []Instruction) (ValueType, bool) {
	stack := make([]ValueType, 0, len(init))
	push := func(vt ValueType) {
		stack = append(stack, vt)
	}
	pop := func() (ValueType, bool) {
		if len(stack) == 0 {
			return ValueType{}, false
		}
		vt := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return vt, true
	}

	for _, ins := range init {
		switch ins.Kind {
		case InstrI32Const:
			push(ValueTypeI32)
		case InstrI64Const:
			push(ValueTypeI64)
		case InstrF32Const:
			push(ValueTypeF32)
		case InstrF64Const:
			push(ValueTypeF64)
		case InstrRefNull:
			push(ins.RefType)
		case InstrRefFunc:
			if ins.FuncIndex >= moduleFunctionCount(m) {
				return ValueType{}, false
			}
			typeIdx, ok := functionTypeIndexAtIndex(m, importedFunctionTypeIndices(m), ins.FuncIndex)
			if !ok {
				return ValueType{}, false
			}
			push(RefTypeIndexed(typeIdx, false))
		case InstrGlobalGet:
			if int(ins.GlobalIndex) >= len(m.Globals) {
				return ValueType{}, false
			}
			g := m.Globals[ins.GlobalIndex]
			if g.Mutable {
				return ValueType{}, false
			}
			push(g.Type)
		case InstrArrayNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != TypeDefKindArray {
				return ValueType{}, false
			}
			lenType, ok := pop()
			if !ok || lenType != ValueTypeI32 {
				return ValueType{}, false
			}
			elemType, ok := pop()
			if !ok || !matchesGCExpectedValue(m, elemType, fieldValueType(td.ElemField)) {
				return ValueType{}, false
			}
			push(RefTypeIndexed(ins.TypeIndex, false))
		case InstrStructNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != TypeDefKindStruct {
				return ValueType{}, false
			}
			if len(stack) < len(td.Fields) {
				return ValueType{}, false
			}
			base := len(stack) - len(td.Fields)
			for j, field := range td.Fields {
				if !matchesExpectedValueInModule(m, validatedValueFromType(stack[base+j]), validatedValueFromType(fieldValueType(field))) {
					return ValueType{}, false
				}
			}
			stack = stack[:base]
			push(RefTypeIndexed(ins.TypeIndex, false))
		case InstrStructNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != TypeDefKindStruct {
				return ValueType{}, false
			}
			for _, field := range td.Fields {
				if !isDefaultableFieldType(field) {
					return ValueType{}, false
				}
			}
			push(RefTypeIndexed(ins.TypeIndex, false))
		case InstrArrayNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != TypeDefKindArray {
				return ValueType{}, false
			}
			if !isDefaultableValueType(fieldValueType(td.ElemField)) {
				return ValueType{}, false
			}
			lenType, ok := pop()
			if !ok || lenType != ValueTypeI32 {
				return ValueType{}, false
			}
			push(RefTypeIndexed(ins.TypeIndex, false))
		case InstrArrayNewFixed:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != TypeDefKindArray {
				return ValueType{}, false
			}
			if len(stack) < int(ins.FixedCount) {
				return ValueType{}, false
			}
			base := len(stack) - int(ins.FixedCount)
			elemType := fieldValueType(td.ElemField)
			for j := 0; j < int(ins.FixedCount); j++ {
				if !matchesGCExpectedValue(m, stack[base+j], elemType) {
					return ValueType{}, false
				}
			}
			stack = stack[:base]
			push(RefTypeIndexed(ins.TypeIndex, false))
		case InstrRefI31:
			valueType, ok := pop()
			if !ok || valueType != ValueTypeI32 {
				return ValueType{}, false
			}
			push(RefTypeI31(false))
		case InstrExternConvertAny:
			valueType, ok := pop()
			if !ok || !matchesGCExpectedValue(m, valueType, RefTypeAny(true)) {
				return ValueType{}, false
			}
			push(RefTypeExtern(valueType.Nullable))
		case InstrAnyConvertExtern:
			valueType, ok := pop()
			if !ok || !matchesGCExpectedValue(m, valueType, RefTypeExtern(true)) {
				return ValueType{}, false
			}
			push(RefTypeAny(valueType.Nullable))
		default:
			return ValueType{}, false
		}
	}

	if len(stack) != 1 {
		return ValueType{}, false
	}
	return stack[0], true
}

func instrName(kind InstrKind) string {
	switch kind {
	case InstrNop:
		return "nop"
	case InstrLocalGet:
		return "local.get"
	case InstrLocalSet:
		return "local.set"
	case InstrLocalTee:
		return "local.tee"
	case InstrCall:
		return "call"
	case InstrCallIndirect:
		return "call_indirect"
	case InstrCallRef:
		return "call_ref"
	case InstrStructNew:
		return "struct.new"
	case InstrStructNewDefault:
		return "struct.new_default"
	case InstrStructGet:
		return "struct.get"
	case InstrStructGetS:
		return "struct.get_s"
	case InstrStructGetU:
		return "struct.get_u"
	case InstrStructSet:
		return "struct.set"
	case InstrArrayLen:
		return "array.len"
	case InstrArrayNew:
		return "array.new"
	case InstrArrayNewDefault:
		return "array.new_default"
	case InstrArrayNewData:
		return "array.new_data"
	case InstrArrayNewElem:
		return "array.new_elem"
	case InstrArrayNewFixed:
		return "array.new_fixed"
	case InstrArrayInitData:
		return "array.init_data"
	case InstrArrayInitElem:
		return "array.init_elem"
	case InstrArrayGet:
		return "array.get"
	case InstrArrayGetS:
		return "array.get_s"
	case InstrArrayGetU:
		return "array.get_u"
	case InstrArraySet:
		return "array.set"
	case InstrArrayFill:
		return "array.fill"
	case InstrArrayCopy:
		return "array.copy"
	case InstrRefEq:
		return "ref.eq"
	case InstrRefTest:
		return "ref.test"
	case InstrRefCast:
		return "ref.cast"
	case InstrExternConvertAny:
		return "extern.convert_any"
	case InstrAnyConvertExtern:
		return "any.convert_extern"
	case InstrRefI31:
		return "ref.i31"
	case InstrI31GetS:
		return "i31.get_s"
	case InstrI31GetU:
		return "i31.get_u"
	case InstrV128Not:
		return "v128.not"
	case InstrV128And:
		return "v128.and"
	case InstrV128AndNot:
		return "v128.andnot"
	case InstrV128Or:
		return "v128.or"
	case InstrV128Xor:
		return "v128.xor"
	case InstrI8x16Swizzle:
		return "i8x16.swizzle"
	case InstrI8x16Shl:
		return "i8x16.shl"
	case InstrI8x16ShrS:
		return "i8x16.shr_s"
	case InstrI8x16ShrU:
		return "i8x16.shr_u"
	case InstrI16x8Shl:
		return "i16x8.shl"
	case InstrI16x8ShrS:
		return "i16x8.shr_s"
	case InstrI16x8ShrU:
		return "i16x8.shr_u"
	case InstrI32x4Splat:
		return "i32x4.splat"
	case InstrI32x4ExtractLane:
		return "i32x4.extract_lane"
	case InstrI32x4Eq:
		return "i32x4.eq"
	case InstrI32x4LtS:
		return "i32x4.lt_s"
	case InstrI32x4Shl:
		return "i32x4.shl"
	case InstrI32x4ShrS:
		return "i32x4.shr_s"
	case InstrI32x4ShrU:
		return "i32x4.shr_u"
	case InstrI32x4Add:
		return "i32x4.add"
	case InstrI32x4Neg:
		return "i32x4.neg"
	case InstrI32x4MinS:
		return "i32x4.min_s"
	case InstrI64x2Shl:
		return "i64x2.shl"
	case InstrI64x2ShrS:
		return "i64x2.shr_s"
	case InstrI64x2ShrU:
		return "i64x2.shr_u"
	case InstrF32x4Add:
		return "f32x4.add"
	case InstrV128Bitselect:
		return "v128.bitselect"
	case InstrBlock:
		return "block"
	case InstrLoop:
		return "loop"
	case InstrIf:
		return "if"
	case InstrElse:
		return "else"
	case InstrBr:
		return "br"
	case InstrBrIf:
		return "br_if"
	case InstrBrOnNull:
		return "br_on_null"
	case InstrBrOnNonNull:
		return "br_on_non_null"
	case InstrBrOnCast:
		return "br_on_cast"
	case InstrBrOnCastFail:
		return "br_on_cast_fail"
	case InstrBrTable:
		return "br_table"
	case InstrUnreachable:
		return "unreachable"
	case InstrReturn:
		return "return"
	case InstrI32Const:
		return "i32.const"
	case InstrI64Const:
		return "i64.const"
	case InstrF32Const:
		return "f32.const"
	case InstrF64Const:
		return "f64.const"
	case InstrV128Const:
		return "v128.const"
	case InstrDrop:
		return "drop"
	case InstrSelect:
		return "select"
	case InstrGlobalGet:
		return "global.get"
	case InstrGlobalSet:
		return "global.set"
	case InstrTableGet:
		return "table.get"
	case InstrTableSet:
		return "table.set"
	case InstrTableCopy:
		return "table.copy"
	case InstrTableFill:
		return "table.fill"
	case InstrTableInit:
		return "table.init"
	case InstrElemDrop:
		return "elem.drop"
	case InstrTableGrow:
		return "table.grow"
	case InstrTableSize:
		return "table.size"
	case InstrI32Load:
		return "i32.load"
	case InstrI64Load:
		return "i64.load"
	case InstrF32Load:
		return "f32.load"
	case InstrF64Load:
		return "f64.load"
	case InstrV128Load:
		return "v128.load"
	case InstrV128Load8x8S:
		return "v128.load8x8_s"
	case InstrV128Load8x8U:
		return "v128.load8x8_u"
	case InstrV128Load16x4S:
		return "v128.load16x4_s"
	case InstrV128Load16x4U:
		return "v128.load16x4_u"
	case InstrV128Load32x2S:
		return "v128.load32x2_s"
	case InstrV128Load32x2U:
		return "v128.load32x2_u"
	case InstrV128Load8Splat:
		return "v128.load8_splat"
	case InstrV128Load16Splat:
		return "v128.load16_splat"
	case InstrV128Load32Splat:
		return "v128.load32_splat"
	case InstrV128Load64Splat:
		return "v128.load64_splat"
	case InstrI32Load8S:
		return "i32.load8_s"
	case InstrI32Load8U:
		return "i32.load8_u"
	case InstrI32Load16S:
		return "i32.load16_s"
	case InstrI32Load16U:
		return "i32.load16_u"
	case InstrI64Load8S:
		return "i64.load8_s"
	case InstrI64Load8U:
		return "i64.load8_u"
	case InstrI64Load16S:
		return "i64.load16_s"
	case InstrI64Load16U:
		return "i64.load16_u"
	case InstrI64Load32S:
		return "i64.load32_s"
	case InstrI64Load32U:
		return "i64.load32_u"
	case InstrI32Store:
		return "i32.store"
	case InstrI64Store:
		return "i64.store"
	case InstrI32Store8:
		return "i32.store8"
	case InstrI32Store16:
		return "i32.store16"
	case InstrI64Store8:
		return "i64.store8"
	case InstrI64Store16:
		return "i64.store16"
	case InstrI64Store32:
		return "i64.store32"
	case InstrF32Store:
		return "f32.store"
	case InstrF64Store:
		return "f64.store"
	case InstrV128Store:
		return "v128.store"
	case InstrMemorySize:
		return "memory.size"
	case InstrMemoryGrow:
		return "memory.grow"
	case InstrMemoryCopy:
		return "memory.copy"
	case InstrMemoryInit:
		return "memory.init"
	case InstrMemoryFill:
		return "memory.fill"
	case InstrDataDrop:
		return "data.drop"
	case InstrI32Eq:
		return "i32.eq"
	case InstrI32Ne:
		return "i32.ne"
	case InstrI32Clz:
		return "i32.clz"
	case InstrI32Ctz:
		return "i32.ctz"
	case InstrI32Popcnt:
		return "i32.popcnt"
	case InstrI32Add:
		return "i32.add"
	case InstrI32Sub:
		return "i32.sub"
	case InstrI32Mul:
		return "i32.mul"
	case InstrI32Or:
		return "i32.or"
	case InstrI32Xor:
		return "i32.xor"
	case InstrI32DivS:
		return "i32.div_s"
	case InstrI32DivU:
		return "i32.div_u"
	case InstrI32RemS:
		return "i32.rem_s"
	case InstrI32RemU:
		return "i32.rem_u"
	case InstrI32Shl:
		return "i32.shl"
	case InstrI32ShrS:
		return "i32.shr_s"
	case InstrI32ShrU:
		return "i32.shr_u"
	case InstrI32Rotl:
		return "i32.rotl"
	case InstrI32Rotr:
		return "i32.rotr"
	case InstrI32Eqz:
		return "i32.eqz"
	case InstrI32LtS:
		return "i32.lt_s"
	case InstrI32LtU:
		return "i32.lt_u"
	case InstrI32LeS:
		return "i32.le_s"
	case InstrI32LeU:
		return "i32.le_u"
	case InstrI32GtS:
		return "i32.gt_s"
	case InstrI32GtU:
		return "i32.gt_u"
	case InstrI32GeS:
		return "i32.ge_s"
	case InstrI32GeU:
		return "i32.ge_u"
	case InstrI32And:
		return "i32.and"
	case InstrI32Extend8S:
		return "i32.extend8_s"
	case InstrI32Extend16S:
		return "i32.extend16_s"
	case InstrI64Add:
		return "i64.add"
	case InstrI64And:
		return "i64.and"
	case InstrI64Or:
		return "i64.or"
	case InstrI64Xor:
		return "i64.xor"
	case InstrI64Eq:
		return "i64.eq"
	case InstrI64Ne:
		return "i64.ne"
	case InstrI64Eqz:
		return "i64.eqz"
	case InstrI64GtS:
		return "i64.gt_s"
	case InstrI64GtU:
		return "i64.gt_u"
	case InstrI64GeS:
		return "i64.ge_s"
	case InstrI64GeU:
		return "i64.ge_u"
	case InstrI64LeS:
		return "i64.le_s"
	case InstrI64LeU:
		return "i64.le_u"
	case InstrI64Sub:
		return "i64.sub"
	case InstrI64Mul:
		return "i64.mul"
	case InstrI64DivS:
		return "i64.div_s"
	case InstrI64DivU:
		return "i64.div_u"
	case InstrI64RemS:
		return "i64.rem_s"
	case InstrI64RemU:
		return "i64.rem_u"
	case InstrI64Shl:
		return "i64.shl"
	case InstrI64ShrS:
		return "i64.shr_s"
	case InstrI64ShrU:
		return "i64.shr_u"
	case InstrI64Rotl:
		return "i64.rotl"
	case InstrI64Rotr:
		return "i64.rotr"
	case InstrI64LtS:
		return "i64.lt_s"
	case InstrI64LtU:
		return "i64.lt_u"
	case InstrI64Clz:
		return "i64.clz"
	case InstrI64Ctz:
		return "i64.ctz"
	case InstrI64Popcnt:
		return "i64.popcnt"
	case InstrI64Extend8S:
		return "i64.extend8_s"
	case InstrI64Extend16S:
		return "i64.extend16_s"
	case InstrI64Extend32S:
		return "i64.extend32_s"
	case InstrI32WrapI64:
		return "i32.wrap_i64"
	case InstrI64ExtendI32S:
		return "i64.extend_i32_s"
	case InstrI64ExtendI32U:
		return "i64.extend_i32_u"
	case InstrF32ConvertI32S:
		return "f32.convert_i32_s"
	case InstrF64ConvertI64S:
		return "f64.convert_i64_s"
	case InstrF32Add:
		return "f32.add"
	case InstrF32Sub:
		return "f32.sub"
	case InstrF32Mul:
		return "f32.mul"
	case InstrF32Div:
		return "f32.div"
	case InstrF32Sqrt:
		return "f32.sqrt"
	case InstrF32Neg:
		return "f32.neg"
	case InstrF32Eq:
		return "f32.eq"
	case InstrF32Lt:
		return "f32.lt"
	case InstrF32Gt:
		return "f32.gt"
	case InstrF32Ne:
		return "f32.ne"
	case InstrF32Min:
		return "f32.min"
	case InstrF32Max:
		return "f32.max"
	case InstrF32Ceil:
		return "f32.ceil"
	case InstrF32Floor:
		return "f32.floor"
	case InstrF32Trunc:
		return "f32.trunc"
	case InstrF32Nearest:
		return "f32.nearest"
	case InstrF64Add:
		return "f64.add"
	case InstrF64Sub:
		return "f64.sub"
	case InstrF64Mul:
		return "f64.mul"
	case InstrF64Div:
		return "f64.div"
	case InstrF64Sqrt:
		return "f64.sqrt"
	case InstrF64Neg:
		return "f64.neg"
	case InstrF64Min:
		return "f64.min"
	case InstrF64Max:
		return "f64.max"
	case InstrF64Ceil:
		return "f64.ceil"
	case InstrF64Floor:
		return "f64.floor"
	case InstrF64Trunc:
		return "f64.trunc"
	case InstrF64Nearest:
		return "f64.nearest"
	case InstrF64Eq:
		return "f64.eq"
	case InstrF64Le:
		return "f64.le"
	case InstrI32ReinterpretF32:
		return "i32.reinterpret_f32"
	case InstrI64ReinterpretF64:
		return "i64.reinterpret_f64"
	case InstrF32ReinterpretI32:
		return "f32.reinterpret_i32"
	case InstrF64ReinterpretI64:
		return "f64.reinterpret_i64"
	case InstrRefNull:
		return "ref.null"
	case InstrRefIsNull:
		return "ref.is_null"
	case InstrRefAsNonNull:
		return "ref.as_non_null"
	case InstrRefFunc:
		return "ref.func"
	case InstrEnd:
		return "end"
	default:
		return "unknown"
	}
}
