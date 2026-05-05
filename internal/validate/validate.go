// Package validate performs semantic validation over wasmir.Module.
//
// The validator runs after WAT text or wasm binary has already been lowered
// into watgo's shared IR. It is not a byte-for-byte implementation of the
// WebAssembly spec appendix's binary-stream validation algorithm; it applies
// the same validation model to wasmir instructions and module fields.
//
// Useful spec anchors in https://webassembly.github.io/spec/core/valid/index.html:
//   - Validation conventions: bottom types, instruction types, contexts, and
//     recursive type conventions.
//   - Validation types and matching: type validity, equivalence, subtyping,
//     and reference matching.
//   - Validation instructions: stack effects and per-instruction typing rules.
//   - Validation modules: module-level index, type, initializer, import,
//     export, element, data, memory, table, tag, and start-function checks.
//   - Validation algorithm appendix: value stack, control stack,
//     local-initialization tracking, and stack-polymorphic unreachable-code
//     behavior mirrored by the function-body validator.
//
// In code, ValidateModule covers module-level validation. bodyValidator
// validates one function body with a value stack and control stack. Simple
// fixed-signature instructions can use metadata from internal/instrdef; rules
// that depend on indices, immediates, control context, reference subtyping, GC,
// memory/table address width, exceptions, or SIMD lanes remain handwritten in
// the instruction switch.
package validate

import (
	"fmt"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/internal/valhint"
	"github.com/eliben/watgo/wasmir"
)

const (
	maxMemoryPages32  uint64 = 65536
	maxMemoryPages64  uint64 = 1 << 48
	maxMemoryOffset32 uint64 = 1<<32 - 1
	maxTableElems32   uint64 = 1<<32 - 1
)

type validatedValue struct {
	Type    wasmir.ValueType
	Unknown bool
}

func isRefValueType(vt wasmir.ValueType) bool {
	return vt.IsRef()
}

func validatedValueFromType(vt wasmir.ValueType) validatedValue {
	return validatedValue{Type: vt}
}

func validatedUnknownValue() validatedValue {
	return validatedValue{Unknown: true}
}

func sameValidatedValue(got, want validatedValue) bool {
	if got.Unknown || want.Unknown {
		return true
	}
	return got.Type == want.Type
}

func equalValueTypeSlices(a, b []wasmir.ValueType) bool {
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

func validatedValueName(v validatedValue) string {
	if v.Unknown {
		return "unknown"
	}
	return v.Type.String()
}

func refinedNonNullValue(v validatedValue) validatedValue {
	if v.Unknown {
		return v
	}
	if isRefValueType(v.Type) {
		v.Type.Nullable = false
	}
	return v
}

func memoryAddressType(m *wasmir.Module, memoryIndex uint32) wasmir.ValueType {
	if m != nil && int(memoryIndex) < len(m.Memories) {
		if m.Memories[memoryIndex].AddressType == wasmir.ValueTypeI64 {
			return wasmir.ValueTypeI64
		}
	}
	return wasmir.ValueTypeI32
}

func tableAddressType(m *wasmir.Module, tableIndex uint32) wasmir.ValueType {
	if m != nil && int(tableIndex) < len(m.Tables) {
		if m.Tables[tableIndex].AddressType == wasmir.ValueTypeI64 {
			return wasmir.ValueTypeI64
		}
	}
	return wasmir.ValueTypeI32
}

func validModuleValueType(m *wasmir.Module, vt wasmir.ValueType) bool {
	if !vt.IsRef() {
		return vt.Kind != wasmir.ValueKindInvalid
	}
	if vt.HeapType.Kind != wasmir.HeapKindTypeIndex {
		return vt.HeapType.Kind != wasmir.HeapKindInvalid
	}
	return int(vt.HeapType.TypeIndex) < len(m.Types)
}

func isDefaultableFieldType(ft wasmir.FieldType) bool {
	if ft.Packed != wasmir.PackedTypeNone {
		return true
	}
	return isDefaultableValueType(ft.Type)
}

func elementRefType(m *wasmir.Module, elemIndex uint32) (wasmir.ValueType, bool) {
	if m == nil || int(elemIndex) >= len(m.Elements) {
		return wasmir.ValueType{}, false
	}
	elem := m.Elements[elemIndex]
	if elem.RefType.Kind != wasmir.ValueKindInvalid {
		return elem.RefType, true
	}
	if len(elem.FuncIndices) > 0 {
		return wasmir.RefTypeFunc(true), true
	}
	return wasmir.ValueType{}, false
}

func typeDefAtIndex(m *wasmir.Module, typeIndex uint32) (wasmir.TypeDef, bool) {
	if m == nil || int(typeIndex) >= len(m.Types) {
		return wasmir.TypeDef{}, false
	}
	return m.Types[typeIndex], true
}

func isDefaultableValueType(vt wasmir.ValueType) bool {
	if vt.IsRef() {
		return vt.Nullable
	}
	switch vt.Kind {
	case wasmir.ValueKindI32, wasmir.ValueKindI64, wasmir.ValueKindF32, wasmir.ValueKindF64, wasmir.ValueKindV128:
		return true
	default:
		return false
	}
}

func fieldValueType(ft wasmir.FieldType) wasmir.ValueType {
	switch ft.Packed {
	case wasmir.PackedTypeI8, wasmir.PackedTypeI16:
		return wasmir.ValueTypeI32
	default:
		return ft.Type
	}
}

func sameFieldStorage(a, b wasmir.FieldType) bool {
	if a.Packed != b.Packed {
		return false
	}
	if a.Packed != wasmir.PackedTypeNone {
		return true
	}
	return a.Type == b.Type
}

func fieldByteWidth(ft wasmir.FieldType) (uint32, bool) {
	switch ft.Packed {
	case wasmir.PackedTypeI8:
		return 1, true
	case wasmir.PackedTypeI16:
		return 2, true
	}
	switch ft.Type {
	case wasmir.ValueTypeI32, wasmir.ValueTypeF32:
		return 4, true
	case wasmir.ValueTypeI64, wasmir.ValueTypeF64:
		return 8, true
	case wasmir.ValueTypeV128:
		return 16, true
	default:
		return 0, false
	}
}

func elementSegmentType(_ *wasmir.Module, seg wasmir.ElementSegment) wasmir.ValueType {
	if seg.RefType.IsRef() {
		return seg.RefType
	}
	if len(seg.FuncIndices) > 0 {
		return wasmir.RefTypeFunc(true)
	}
	return wasmir.ValueType{}
}

func typeIndexHasKind(m *wasmir.Module, typeIndex uint32, kind wasmir.TypeDefKind) bool {
	if m == nil || int(typeIndex) >= len(m.Types) {
		return false
	}
	return m.Types[typeIndex].Kind == kind
}

func isTypeIndexSubtype(m *wasmir.Module, got, want uint32) bool {
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

type typeEquivalenceChecker struct {
	// m is the module whose recursive type graph is being compared.
	m *wasmir.Module

	// groupVisiting tracks recursive-group pairs currently being compared, so
	// cyclic references inside a rec group can terminate successfully.
	groupVisiting map[typePair]bool

	// groupMemo caches completed recursive-group equivalence checks.
	groupMemo map[typePair]bool

	// typeVisiting tracks individual type-entry pairs currently being compared.
	typeVisiting map[typePair]bool

	// typeMemo caches completed individual type-entry equivalence checks.
	typeMemo map[typePair]bool
}

func newTypeEquivalenceChecker(m *wasmir.Module) typeEquivalenceChecker {
	return typeEquivalenceChecker{
		m:             m,
		groupVisiting: make(map[typePair]bool),
		groupMemo:     make(map[typePair]bool),
		typeVisiting:  make(map[typePair]bool),
		typeMemo:      make(map[typePair]bool),
	}
}

func typeIndicesEquivalent(m *wasmir.Module, a, b uint32) bool {
	c := newTypeEquivalenceChecker(m)
	return c.typeIndicesEquivalent(a, b)
}

func (c *typeEquivalenceChecker) typeIndicesEquivalent(a, b uint32) bool {
	startA, sizeA, posA := recGroupInfo(c.m, a)
	startB, sizeB, posB := recGroupInfo(c.m, b)
	if posA != posB || sizeA != sizeB {
		return false
	}
	if sizeA > 1 {
		groupKey := typePair{a: startA, b: startB}
		if eq, ok := c.groupMemo[groupKey]; ok {
			return eq
		}
		if c.groupVisiting[groupKey] {
			return true
		}
		c.groupVisiting[groupKey] = true
		defer delete(c.groupVisiting, groupKey)
		for i := uint32(0); i < sizeA; i++ {
			if !c.typeIndicesEquivalentInGroup(startA, startB, sizeA, startA+i, startB+i) {
				c.groupMemo[groupKey] = false
				return false
			}
		}
		c.groupMemo[groupKey] = true
		return true
	}
	return c.typeIndicesEquivalentBody(a, b)
}

func (c *typeEquivalenceChecker) typeIndicesEquivalentInGroup(groupA, groupB, groupSize, a, b uint32) bool {
	if a == b && groupA == groupB {
		return true
	}
	if c.m == nil || int(a) >= len(c.m.Types) || int(b) >= len(c.m.Types) {
		return false
	}
	key := typePair{a: a, b: b}
	if eq, ok := c.typeMemo[key]; ok {
		return eq
	}
	if c.typeVisiting[key] {
		return true
	}
	c.typeVisiting[key] = true
	defer delete(c.typeVisiting, key)

	ta := c.m.Types[a]
	tb := c.m.Types[b]
	if ta.SubType != tb.SubType || ta.Final != tb.Final || ta.Kind != tb.Kind || len(ta.SuperTypes) != len(tb.SuperTypes) {
		c.typeMemo[key] = false
		return false
	}
	for i := range ta.SuperTypes {
		if !c.typeIndexRefsEquivalentInGroup(groupA, groupB, groupSize, ta.SuperTypes[i], tb.SuperTypes[i]) {
			c.typeMemo[key] = false
			return false
		}
	}

	var eq bool
	switch ta.Kind {
	case wasmir.TypeDefKindFunc:
		eq = len(ta.Params) == len(tb.Params) && len(ta.Results) == len(tb.Results)
		if eq {
			for i := range ta.Params {
				if !c.valueTypesEquivalentInRecGroup(ta.Params[i], tb.Params[i], groupA, groupB, groupSize) {
					eq = false
					break
				}
			}
		}
		if eq {
			for i := range ta.Results {
				if !c.valueTypesEquivalentInRecGroup(ta.Results[i], tb.Results[i], groupA, groupB, groupSize) {
					eq = false
					break
				}
			}
		}
	case wasmir.TypeDefKindStruct:
		eq = len(ta.Fields) == len(tb.Fields)
		if eq {
			for i := range ta.Fields {
				if !c.fieldTypesEquivalentInRecGroup(ta.Fields[i], tb.Fields[i], groupA, groupB, groupSize) {
					eq = false
					break
				}
			}
		}
	case wasmir.TypeDefKindArray:
		eq = c.fieldTypesEquivalentInRecGroup(ta.ElemField, tb.ElemField, groupA, groupB, groupSize)
	default:
		eq = false
	}
	c.typeMemo[key] = eq
	return eq
}

func (c *typeEquivalenceChecker) typeIndexRefsEquivalentInGroup(groupA, groupB, groupSize, a, b uint32) bool {
	inA := a >= groupA && a < groupA+groupSize
	inB := b >= groupB && b < groupB+groupSize
	if inA || inB {
		if !inA || !inB {
			return false
		}
		if a-groupA != b-groupB {
			return false
		}
		return c.typeIndicesEquivalentInGroup(groupA, groupB, groupSize, a, b)
	}
	return c.typeIndicesEquivalent(a, b)
}

func (c *typeEquivalenceChecker) valueTypesEquivalentInRecGroup(a, b wasmir.ValueType, groupA, groupB, groupSize uint32) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind != wasmir.ValueKindRef {
		return a == b
	}
	if a.Nullable != b.Nullable {
		return false
	}
	if a.UsesTypeIndex() && b.UsesTypeIndex() {
		return c.typeIndexRefsEquivalentInGroup(groupA, groupB, groupSize, a.HeapType.TypeIndex, b.HeapType.TypeIndex)
	}
	return a.HeapType.Kind == b.HeapType.Kind
}

func (c *typeEquivalenceChecker) fieldTypesEquivalentInRecGroup(a, b wasmir.FieldType, groupA, groupB, groupSize uint32) bool {
	if a.Mutable != b.Mutable || a.Packed != b.Packed {
		return false
	}
	if a.Packed != wasmir.PackedTypeNone {
		return true
	}
	return c.valueTypesEquivalentInRecGroup(a.Type, b.Type, groupA, groupB, groupSize)
}

func recGroupInfo(m *wasmir.Module, idx uint32) (start uint32, size uint32, pos uint32) {
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

func (c *typeEquivalenceChecker) typeIndicesEquivalentBody(a, b uint32) bool {
	if a == b {
		return true
	}
	if c.m == nil || int(a) >= len(c.m.Types) || int(b) >= len(c.m.Types) {
		return false
	}
	key := typePair{a: a, b: b}
	if eq, ok := c.typeMemo[key]; ok {
		return eq
	}
	if c.typeVisiting[key] {
		return true
	}
	c.typeVisiting[key] = true
	defer delete(c.typeVisiting, key)

	ta := c.m.Types[a]
	tb := c.m.Types[b]
	if ta.SubType != tb.SubType || ta.Final != tb.Final || ta.Kind != tb.Kind || len(ta.SuperTypes) != len(tb.SuperTypes) {
		c.typeMemo[key] = false
		return false
	}
	for i := range ta.SuperTypes {
		if !c.typeIndicesEquivalent(ta.SuperTypes[i], tb.SuperTypes[i]) {
			c.typeMemo[key] = false
			return false
		}
	}

	var eq bool
	switch ta.Kind {
	case wasmir.TypeDefKindFunc:
		eq = len(ta.Params) == len(tb.Params) && len(ta.Results) == len(tb.Results)
		if eq {
			for i := range ta.Params {
				if !c.valueTypesEquivalent(ta.Params[i], tb.Params[i]) {
					eq = false
					break
				}
			}
		}
		if eq {
			for i := range ta.Results {
				if !c.valueTypesEquivalent(ta.Results[i], tb.Results[i]) {
					eq = false
					break
				}
			}
		}
	case wasmir.TypeDefKindStruct:
		eq = len(ta.Fields) == len(tb.Fields)
		if eq {
			for i := range ta.Fields {
				if !c.fieldTypesEquivalent(ta.Fields[i], tb.Fields[i]) {
					eq = false
					break
				}
			}
		}
	case wasmir.TypeDefKindArray:
		eq = c.fieldTypesEquivalent(ta.ElemField, tb.ElemField)
	default:
		eq = false
	}
	c.typeMemo[key] = eq
	return eq
}

func (c *typeEquivalenceChecker) valueTypesEquivalent(a, b wasmir.ValueType) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind != wasmir.ValueKindRef {
		return a == b
	}
	if a.Nullable != b.Nullable {
		return false
	}
	if a.UsesTypeIndex() && b.UsesTypeIndex() {
		return c.typeIndicesEquivalent(a.HeapType.TypeIndex, b.HeapType.TypeIndex)
	}
	return a.HeapType.Kind == b.HeapType.Kind
}

func (c *typeEquivalenceChecker) fieldTypesEquivalent(a, b wasmir.FieldType) bool {
	if a.Mutable != b.Mutable || a.Packed != b.Packed {
		return false
	}
	if a.Packed != wasmir.PackedTypeNone {
		return true
	}
	return c.valueTypesEquivalent(a.Type, b.Type)
}

func isModuleValueSubtype(m *wasmir.Module, got, want wasmir.ValueType) bool {
	if got == want {
		return true
	}
	if got.IsRef() && want.IsRef() {
		return matchesRefTypeInModule(m, got, want)
	}
	return false
}

func isTypeFinal(td wasmir.TypeDef) bool {
	return !td.SubType || td.Final
}

func isValidDeclaredSubtype(m *wasmir.Module, sub, super wasmir.TypeDef) bool {
	if sub.Kind != super.Kind {
		return false
	}
	switch sub.Kind {
	case wasmir.TypeDefKindFunc:
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
	case wasmir.TypeDefKindStruct:
		if len(sub.Fields) < len(super.Fields) {
			return false
		}
		for i := range super.Fields {
			sf := sub.Fields[i]
			gf := super.Fields[i]
			if sf.Mutable != gf.Mutable || sf.Packed != gf.Packed {
				return false
			}
			if sf.Packed != wasmir.PackedTypeNone {
				continue
			}
			if sf.Mutable {
				c := newTypeEquivalenceChecker(m)
				if !c.fieldTypesEquivalent(sf, gf) {
					return false
				}
			} else if !isModuleValueSubtype(m, sf.Type, gf.Type) {
				return false
			}
		}
		return true
	case wasmir.TypeDefKindArray:
		if sub.ElemField.Mutable != super.ElemField.Mutable || sub.ElemField.Packed != super.ElemField.Packed {
			return false
		}
		if sub.ElemField.Packed != wasmir.PackedTypeNone {
			return true
		}
		if sub.ElemField.Mutable {
			c := newTypeEquivalenceChecker(m)
			return c.fieldTypesEquivalent(sub.ElemField, super.ElemField)
		}
		return isModuleValueSubtype(m, sub.ElemField.Type, super.ElemField.Type)
	default:
		return false
	}
}

func typeRefVisibleFromType(m *wasmir.Module, fromIndex, targetIndex uint32) bool {
	if m == nil || int(fromIndex) >= len(m.Types) || int(targetIndex) >= len(m.Types) {
		return false
	}
	if targetIndex == fromIndex {
		return true
	}
	if targetIndex < fromIndex {
		return true
	}
	groupStart, groupSize, _ := recGroupInfo(m, fromIndex)
	return groupSize > 1 && targetIndex >= groupStart && targetIndex < groupStart+groupSize
}

func validateTypeVisibilityRef(m *wasmir.Module, fromIndex uint32, vt wasmir.ValueType) bool {
	if !vt.UsesTypeIndex() {
		return true
	}
	return typeRefVisibleFromType(m, fromIndex, vt.HeapType.TypeIndex)
}

func validateTypeDefVisibility(m *wasmir.Module, typeIndex uint32, td wasmir.TypeDef) bool {
	for _, super := range td.SuperTypes {
		if !typeRefVisibleFromType(m, typeIndex, super) {
			return false
		}
	}
	switch td.Kind {
	case wasmir.TypeDefKindFunc:
		for _, vt := range td.Params {
			if !validateTypeVisibilityRef(m, typeIndex, vt) {
				return false
			}
		}
		for _, vt := range td.Results {
			if !validateTypeVisibilityRef(m, typeIndex, vt) {
				return false
			}
		}
	case wasmir.TypeDefKindStruct:
		for _, field := range td.Fields {
			if field.Packed == wasmir.PackedTypeNone && !validateTypeVisibilityRef(m, typeIndex, field.Type) {
				return false
			}
		}
	case wasmir.TypeDefKindArray:
		if td.ElemField.Packed == wasmir.PackedTypeNone && !validateTypeVisibilityRef(m, typeIndex, td.ElemField.Type) {
			return false
		}
	}
	return true
}

func matchesRefTypeInModule(m *wasmir.Module, got, want wasmir.ValueType) bool {
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
	case wasmir.HeapKindAny:
		switch got.HeapType.Kind {
		case wasmir.HeapKindNone, wasmir.HeapKindAny, wasmir.HeapKindEq, wasmir.HeapKindI31, wasmir.HeapKindArray, wasmir.HeapKindStruct:
			return true
		case wasmir.HeapKindTypeIndex:
			return true
		default:
			return false
		}
	case wasmir.HeapKindNone:
		return got.HeapType.Kind == wasmir.HeapKindNone
	case wasmir.HeapKindEq:
		switch got.HeapType.Kind {
		case wasmir.HeapKindNone, wasmir.HeapKindEq, wasmir.HeapKindI31, wasmir.HeapKindArray, wasmir.HeapKindStruct:
			return true
		case wasmir.HeapKindTypeIndex:
			return typeIndexHasKind(m, got.HeapType.TypeIndex, wasmir.TypeDefKindStruct) ||
				typeIndexHasKind(m, got.HeapType.TypeIndex, wasmir.TypeDefKindArray)
		default:
			return false
		}
	case wasmir.HeapKindI31:
		return got.HeapType.Kind == wasmir.HeapKindI31
	case wasmir.HeapKindArray:
		return got.HeapType.Kind == wasmir.HeapKindArray ||
			(got.HeapType.Kind == wasmir.HeapKindTypeIndex && typeIndexHasKind(m, got.HeapType.TypeIndex, wasmir.TypeDefKindArray))
	case wasmir.HeapKindStruct:
		return got.HeapType.Kind == wasmir.HeapKindStruct ||
			(got.HeapType.Kind == wasmir.HeapKindTypeIndex && typeIndexHasKind(m, got.HeapType.TypeIndex, wasmir.TypeDefKindStruct))
	case wasmir.HeapKindFunc:
		return got.HeapType.Kind == wasmir.HeapKindFunc ||
			got.HeapType.Kind == wasmir.HeapKindNoFunc ||
			(got.HeapType.Kind == wasmir.HeapKindTypeIndex && typeIndexHasKind(m, got.HeapType.TypeIndex, wasmir.TypeDefKindFunc))
	case wasmir.HeapKindExtern:
		return got.HeapType.Kind == wasmir.HeapKindExtern || got.HeapType.Kind == wasmir.HeapKindNoExtern
	case wasmir.HeapKindNoExtern:
		return got.HeapType.Kind == wasmir.HeapKindNoExtern
	case wasmir.HeapKindExn:
		return got.HeapType.Kind == wasmir.HeapKindExn || got.HeapType.Kind == wasmir.HeapKindNoExn
	case wasmir.HeapKindNoExn:
		return got.HeapType.Kind == wasmir.HeapKindNoExn
	case wasmir.HeapKindNoFunc:
		return got.HeapType.Kind == wasmir.HeapKindNoFunc
	case wasmir.HeapKindTypeIndex:
		if got.HeapType.Kind == wasmir.HeapKindTypeIndex {
			return isTypeIndexSubtype(m, got.HeapType.TypeIndex, want.HeapType.TypeIndex)
		}
		return (got.HeapType.Kind == wasmir.HeapKindNone && want.Nullable &&
			(typeIndexHasKind(m, want.HeapType.TypeIndex, wasmir.TypeDefKindStruct) ||
				typeIndexHasKind(m, want.HeapType.TypeIndex, wasmir.TypeDefKindArray))) ||
			(got.HeapType.Kind == wasmir.HeapKindNoFunc && want.Nullable &&
				typeIndexHasKind(m, want.HeapType.TypeIndex, wasmir.TypeDefKindFunc))
	default:
		return false
	}
}

func diffRefType(src, target wasmir.ValueType) wasmir.ValueType {
	if !src.IsRef() || !target.IsRef() {
		return wasmir.ValueType{}
	}
	if src == target && !src.Nullable {
		return wasmir.ValueType{}
	}
	if src.Nullable && target.Nullable {
		out := src
		out.Nullable = false
		return out
	}
	return src
}

func matchesGCExpectedValue(m *wasmir.Module, got, want wasmir.ValueType) bool {
	if got.IsRef() || want.IsRef() {
		return matchesRefTypeInModule(m, got, want)
	}
	return got == want
}

func matchesExpectedValueInModule(m *wasmir.Module, got, want validatedValue) bool {
	if got.Unknown {
		return true
	}
	return matchesGCExpectedValue(m, got.Type, want.Type)
}

func naturalMemoryAlignExponent(kind wasmir.InstrKind) (uint32, bool) {
	switch kind {
	case wasmir.InstrI32Load8S, wasmir.InstrI32Load8U, wasmir.InstrI64Load8S, wasmir.InstrI64Load8U, wasmir.InstrI32Store8, wasmir.InstrI64Store8:
		return 0, true
	case wasmir.InstrV128Load8Splat:
		return 0, true
	case wasmir.InstrV128Load8Lane:
		return 0, true
	case wasmir.InstrV128Store8Lane:
		return 0, true
	case wasmir.InstrI32Load16S, wasmir.InstrI32Load16U, wasmir.InstrI64Load16S, wasmir.InstrI64Load16U, wasmir.InstrI32Store16, wasmir.InstrI64Store16:
		return 1, true
	case wasmir.InstrV128Load16Splat:
		return 1, true
	case wasmir.InstrV128Load16Lane:
		return 1, true
	case wasmir.InstrV128Store16Lane:
		return 1, true
	case wasmir.InstrI32Load, wasmir.InstrF32Load, wasmir.InstrI64Load32S, wasmir.InstrI64Load32U, wasmir.InstrI32Store, wasmir.InstrI64Store32, wasmir.InstrF32Store:
		return 2, true
	case wasmir.InstrV128Load32Splat:
		return 2, true
	case wasmir.InstrV128Load32Zero:
		return 2, true
	case wasmir.InstrV128Load32Lane:
		return 2, true
	case wasmir.InstrV128Store32Lane:
		return 2, true
	case wasmir.InstrI64Load, wasmir.InstrF64Load, wasmir.InstrI64Store, wasmir.InstrF64Store:
		return 3, true
	case wasmir.InstrV128Load8x8S, wasmir.InstrV128Load8x8U, wasmir.InstrV128Load16x4S, wasmir.InstrV128Load16x4U, wasmir.InstrV128Load32x2S, wasmir.InstrV128Load32x2U, wasmir.InstrV128Load64Splat:
		return 3, true
	case wasmir.InstrV128Load64Zero:
		return 3, true
	case wasmir.InstrV128Load64Lane:
		return 3, true
	case wasmir.InstrV128Store64Lane:
		return 3, true
	case wasmir.InstrV128Load, wasmir.InstrV128Store:
		return 4, true
	default:
		return 0, false
	}
}

// ValidateModule validates m.
//
// hints may be nil. When provided, they must be aligned to m's defined
// functions and instruction bodies and typically come from
// textformat.LowerModule. Binary-decoded modules and callers that do
// not have folded-source metadata should pass nil.
//
// Validation includes module-level checks (type/export indices) and function
// body type checks for the currently supported instruction subset.
// It returns nil on success. On any failure, it returns diag.ErrorList.
func ValidateModule(m *wasmir.Module, hints *valhint.ModuleHints) error {
	if m == nil {
		return diag.Fromf("module is nil")
	}

	var diags diag.ErrorList
	funcImportTypeIdx := importedFunctionTypeIndices(m)
	funcImportCount := uint32(len(funcImportTypeIdx))
	tagImportTypeIdx := importedTagTypeIndices(m)
	totalTagCount := uint32(len(tagImportTypeIdx) + len(m.Tags))
	declaredFuncs := declaredFunctionRefs(m)
	totalFuncCount := funcImportCount + uint32(len(m.Funcs))
	if m.StartFuncIndex != nil {
		if *m.StartFuncIndex >= totalFuncCount {
			diags.Addf("start: unknown function")
		} else {
			var startTypeIdx uint32
			if *m.StartFuncIndex < funcImportCount {
				startTypeIdx = funcImportTypeIdx[*m.StartFuncIndex]
			} else {
				startTypeIdx = m.Funcs[*m.StartFuncIndex-funcImportCount].TypeIdx
			}
			if int(startTypeIdx) >= len(m.Types) || m.Types[startTypeIdx].Kind != wasmir.TypeDefKindFunc {
				diags.Addf("start: unknown function")
			} else {
				startType := m.Types[startTypeIdx]
				if len(startType.Params) != 0 || len(startType.Results) != 0 {
					diags.Addf("start: start function")
				}
			}
		}
	}
	for i, td := range m.Types {
		if !validateTypeDefVisibility(m, uint32(i), td) {
			diags.Addf("type[%d]: unknown type", i)
		}
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
		case wasmir.TypeDefKindFunc:
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
		case wasmir.TypeDefKindStruct:
			for j, field := range td.Fields {
				if field.Packed == wasmir.PackedTypeNone && !validModuleValueType(m, field.Type) {
					diags.Addf("type[%d] field[%d]: unknown type", i, j)
				}
			}
		case wasmir.TypeDefKindArray:
			if td.ElemField.Packed == wasmir.PackedTypeNone && !validModuleValueType(m, td.ElemField.Type) {
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
		if m.Types[f.TypeIdx].Kind != wasmir.TypeDefKindFunc {
			diags.Addf("%s has non-function type index %d", fnCtx, f.TypeIdx)
			continue
		}
		for j, local := range f.Locals {
			if !validModuleValueType(m, local) {
				diags.Addf("%s local[%d]: unknown type", fnCtx, j)
			}
		}
		var funcHints *valhint.FuncHints
		if hints != nil && i < len(hints.Funcs) {
			funcHints = &hints.Funcs[i]
		}
		funcErrs := (&bodyValidator{
			m:                 m,
			ft:                m.Types[f.TypeIdx],
			f:                 f,
			funcImportTypeIdx: funcImportTypeIdx,
			tagImportTypeIdx:  tagImportTypeIdx,
			declaredFuncs:     declaredFuncs,
			hints:             funcHints,
		}).validate()
		for _, err := range funcErrs {
			diags.Addf("%s: %v", fnCtx, err)
		}
	}

	for i, g := range m.Globals {
		if !validModuleValueType(m, g.Type) {
			diags.Addf("global[%d]: unknown type", i)
			continue
		}
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
		if !validModuleValueType(m, table.RefType) {
			diags.Addf("table[%d]: unknown type", i)
			continue
		}
		addrType := tableAddressType(m, uint32(i))
		if addrType == wasmir.ValueTypeI32 && table.Min > maxTableElems32 {
			diags.Addf("table[%d]: table size", i)
		}
		if table.Max != nil && table.Min > *table.Max {
			diags.Addf("table[%d]: size minimum must not be greater than maximum", i)
		}
		if addrType == wasmir.ValueTypeI32 && table.Max != nil && *table.Max > maxTableElems32 {
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
		if addrType == wasmir.ValueTypeI64 {
			maxPages = maxMemoryPages64
		}
		if mem.Min > maxPages {
			diags.Addf("memory[%d]: memory size", i)
		}
		if mem.Max != nil {
			if *mem.Max > maxPages {
				diags.Addf("memory[%d]: memory size", i)
			}
			if mem.Min > *mem.Max {
				diags.Addf("memory[%d]: size minimum must not be greater than maximum", i)
			}
		}
	}

	for i, data := range m.Data {
		if data.Mode != wasmir.DataSegmentModeActive {
			continue
		}
		if int(data.MemoryIndex) >= len(m.Memories) {
			diags.Addf("data[%d]: unknown memory", i)
			continue
		}
		wantOffsetType := memoryAddressType(m, data.MemoryIndex)
		if len(data.OffsetExpr) > 0 {
			gotOffsetType, ok := globalInitType(m, data.OffsetExpr)
			if !ok {
				diags.Addf("data[%d]: constant expression required", i)
			} else if gotOffsetType != wantOffsetType {
				diags.Addf("data[%d]: offset type mismatch", i)
			}
		} else if data.OffsetType != wantOffsetType {
			diags.Addf("data[%d]: offset type mismatch", i)
		}
	}

	for i, f := range m.Funcs {
		for j, ins := range f.Body {
			natural, ok := naturalMemoryAlignExponent(ins.Kind)
			if !ok {
				continue
			}
			if ins.MemoryAlign > natural {
				diags.Addf("func[%d] instruction[%d]: alignment must not be larger than natural", i, j)
			}
			if int(ins.MemoryIndex) < len(m.Memories) && memoryAddressType(m, ins.MemoryIndex) != wasmir.ValueTypeI64 &&
				ins.MemoryOffset > maxMemoryOffset32 {
				diags.Addf("func[%d] instruction[%d]: offset out of range", i, j)
			}
		}
	}

	for i, elem := range m.Elements {
		if elem.RefType.Kind != wasmir.ValueKindInvalid && !validModuleValueType(m, elem.RefType) {
			diags.Addf("element[%d]: unknown type", i)
			continue
		}
		tableTy := wasmir.RefTypeFunc(true)
		if elem.Mode == wasmir.ElemSegmentModeActive {
			if int(elem.TableIndex) >= len(m.Tables) {
				diags.Addf("element[%d] has invalid table index %d", i, elem.TableIndex)
				continue
			}
			tableTy = m.Tables[elem.TableIndex].RefType
			wantOffsetType := tableAddressType(m, elem.TableIndex)
			if len(elem.OffsetExpr) > 0 {
				gotOffsetType, ok := globalInitType(m, elem.OffsetExpr)
				if !ok {
					diags.Addf("element[%d]: constant expression required", i)
				} else if gotOffsetType != wantOffsetType {
					diags.Addf("element[%d]: offset type mismatch", i)
				}
			} else if elem.OffsetType != wantOffsetType {
				diags.Addf("element[%d]: offset type mismatch", i)
			}
			if len(elem.FuncIndices) > 0 && tableTy.HeapType.Kind != wasmir.HeapKindFunc && tableTy.HeapType.Kind != wasmir.HeapKindTypeIndex {
				diags.Addf("element[%d]: type mismatch", i)
			}
		}
		for j, funcIdx := range elem.FuncIndices {
			if funcIdx >= totalFuncCount {
				diags.Addf("element[%d] func[%d] index %d out of range", i, j, funcIdx)
			}
		}
		if len(elem.Exprs) > 0 {
			if elem.Mode == wasmir.ElemSegmentModeActive &&
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

	for i, typeIdx := range tagImportTypeIdx {
		if int(typeIdx) >= len(m.Types) || m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
			diags.Addf("tag import[%d]: unknown type", i)
			continue
		}
		if len(m.Types[typeIdx].Results) != 0 {
			diags.Addf("tag import[%d]: non-empty tag result type", i)
		}
	}

	for i, tag := range m.Tags {
		if int(tag.TypeIdx) >= len(m.Types) || m.Types[tag.TypeIdx].Kind != wasmir.TypeDefKindFunc {
			diags.Addf("tag[%d]: unknown type", i)
			continue
		}
		if len(m.Types[tag.TypeIdx].Results) != 0 {
			diags.Addf("tag[%d]: non-empty tag result type", i)
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
		case wasmir.ExternalKindFunction:
			if exp.Index >= totalFuncCount {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case wasmir.ExternalKindTable:
			if int(exp.Index) >= len(m.Tables) {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case wasmir.ExternalKindMemory:
			if int(exp.Index) >= len(m.Memories) {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case wasmir.ExternalKindGlobal:
			if int(exp.Index) >= len(m.Globals) {
				diags.Addf("export[%d] index %d out of range", i, exp.Index)
			}
		case wasmir.ExternalKindTag:
			if exp.Index >= totalTagCount {
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

// bodyValidator holds the per-function state needed while validating one
// lowered function body against its declared type.
type bodyValidator struct {
	// m is the module owning the function under validation.
	m *wasmir.Module

	// ft is the declared function type referenced by f.TypeIdx.
	ft wasmir.TypeDef

	// f is the function body currently being validated.
	f wasmir.Function

	// funcImportTypeIdx maps imported function indices to wasmir.Module.Types entries.
	funcImportTypeIdx []uint32

	// tagImportTypeIdx maps imported tag indices to wasmir.Module.Types entries.
	tagImportTypeIdx []uint32

	// declaredFuncs records function indices that are valid targets of
	// ref.func.
	declaredFuncs map[uint32]bool

	// hints carries optional folded-source validation metadata aligned to
	// f.Body.
	hints *valhint.FuncHints
}

// validate checks one function body and returns any diagnostics found while
// checking instruction ordering, local-index bounds, and stack/result typing.
func (v *bodyValidator) validate() diag.ErrorList {
	m := v.m
	ft := v.ft
	f := v.f
	funcImportTypeIdx := v.funcImportTypeIdx
	tagImportTypeIdx := v.tagImportTypeIdx
	declaredFuncs := v.declaredFuncs
	hints := v.hints

	var diags diag.ErrorList
	funcLocCtx := functionLocationContext(f)
	funcImportCount := uint32(len(funcImportTypeIdx))
	totalFuncCount := funcImportCount + uint32(len(m.Funcs))

	if len(f.Body) == 0 {
		diags.Addf("%sempty function body", funcLocCtx)
		return diags
	}
	if f.Body[len(f.Body)-1].Kind != wasmir.InstrEnd {
		diags.Addf("%sfunction body must terminate with end", funcLocCtx)
		return diags
	}

	valueAt := func(types []wasmir.ValueType, i int) validatedValue {
		return validatedValue{Type: types[i]}
	}
	valuesOf := func(types []wasmir.ValueType) []validatedValue {
		out := make([]validatedValue, len(types))
		for i := range types {
			out[i] = valueAt(types, i)
		}
		return out
	}

	locals := make([]wasmir.ValueType, 0, len(ft.Params)+len(f.Locals))
	locals = append(locals, ft.Params...)
	locals = append(locals, f.Locals...)
	localInitialized := make([]bool, len(locals))
	for i := range ft.Params {
		localInitialized[i] = true
	}
	for i, vt := range f.Locals {
		localInitialized[len(ft.Params)+i] = isDefaultableValueType(vt)
	}

	stack := make([]validatedValue, 0)
	stackValue := func(i int) validatedValue {
		return stack[i]
	}
	appendStackValue := func(v validatedValue) {
		stack = append(stack, v)
	}
	appendStackType := func(vt wasmir.ValueType) {
		appendStackValue(validatedValueFromType(vt))
	}
	appendStackValues := func(values []validatedValue) {
		for _, v := range values {
			appendStackValue(v)
		}
	}
	instrHintAt := func(i int) valhint.InstrHints {
		if hints == nil || i < 0 || i >= len(hints.Instrs) {
			return valhint.InstrHints{}
		}
		return hints.Instrs[i]
	}
	truncateStack := func(n int) {
		stack = stack[:n]
	}
	setStackValue := func(i int, v validatedValue) {
		stack[i] = v
	}
	stackValueHasType := func(i int, want wasmir.ValueType) bool {
		got := stackValue(i)
		return got.Unknown || got.Type == want
	}
	stackValueIsRef := func(i int) bool {
		got := stackValue(i)
		return got.Unknown || isRefValueType(got.Type)
	}
	type controlKind uint8
	const (
		controlKindBlock controlKind = iota
		controlKindLoop
		controlKindIf
		controlKindTryTable
	)
	type controlFrame struct {
		kind               controlKind
		entryHeight        int
		paramTypes         []wasmir.ValueType
		resultTypes        []wasmir.ValueType
		localInit          []bool
		sawElse            bool
		enteredUnreachable bool
		unreachable        bool
	}
	var controlStack []controlFrame
	// The function body is typed under an implicit outer block whose label
	// carries the function result types. This makes top-level br/br_if/br_table
	// depth 0 target the function return arity/types.
	controlStack = append(controlStack, controlFrame{
		kind:        controlKindBlock,
		entryHeight: 0,
		resultTypes: append([]wasmir.ValueType(nil), ft.Results...),
		localInit:   append([]bool(nil), localInitialized...),
	})

	currentFrameUnreachable := func() bool {
		return len(controlStack) > 0 && controlStack[len(controlStack)-1].unreachable
	}

	ensureCurrentFrameOperands := func(n int, explicitOperands int, bottomOperands int) bool {
		if len(controlStack) == 0 {
			return len(stack) >= n
		}
		frame := controlStack[len(controlStack)-1]
		available := len(stack) - frame.entryHeight
		requiredConcrete := explicitOperands
		if bottomOperands > 0 {
			requiredConcrete = 0
		}
		if requiredConcrete > n {
			requiredConcrete = n
		}
		if available < requiredConcrete {
			return false
		}
		if available >= n {
			return true
		}
		if !frame.unreachable {
			return false
		}
		missing := n - available
		padding := make([]validatedValue, missing)
		for i := range padding {
			padding[i] = validatedUnknownValue()
		}
		stack = append(stack[:frame.entryHeight], append(padding, stack[frame.entryHeight:]...)...)
		return true
	}

	validateFrameResult := func(insCtx string, frame controlFrame, context string) {
		if frame.unreachable {
			actualCount := len(stack) - frame.entryHeight
			if actualCount > len(frame.resultTypes) {
				diags.Addf("%s: %s stack height mismatch: got %d want at most %d", insCtx, context, len(stack), frame.entryHeight+len(frame.resultTypes))
				return
			}
			wantBase := len(frame.resultTypes) - actualCount
			for i := 0; i < actualCount; i++ {
				got := stackValue(frame.entryHeight + i)
				want := valueAt(frame.resultTypes, wantBase+i)
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: %s result type mismatch at %d: got %s want %s", insCtx, context, wantBase+i, validatedValueName(got), validatedValueName(want))
					return
				}
			}
			return
		}
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
		if !ensureCurrentFrameOperands(len(targetValues), 0, 0) {
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

	tryTableCatchValues := func(catch wasmir.TryTableCatch) ([]validatedValue, bool) {
		switch catch.Kind {
		case wasmir.TryTableCatchKindTag:
			tagType, ok := tagTypeAtIndex(m, tagImportTypeIdx, catch.TagIndex)
			if !ok {
				return nil, false
			}
			return valuesOf(tagType.Params), true
		case wasmir.TryTableCatchKindTagRef:
			tagType, ok := tagTypeAtIndex(m, tagImportTypeIdx, catch.TagIndex)
			if !ok {
				return nil, false
			}
			values := valuesOf(tagType.Params)
			values = append(values, validatedValueFromType(wasmir.RefTypeExn(false)))
			return values, true
		case wasmir.TryTableCatchKindAll:
			return nil, true
		case wasmir.TryTableCatchKindAllRef:
			return []validatedValue{validatedValueFromType(wasmir.RefTypeExn(false))}, true
		default:
			return nil, false
		}
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

	controlSignature := func(ins wasmir.Instruction, insCtx, opname string) ([]wasmir.ValueType, []wasmir.ValueType, bool) {
		if ins.BlockTypeUsesIndex {
			if int(ins.BlockTypeIndex) >= len(m.Types) {
				diags.Addf("%s: %s has invalid block type index %d", insCtx, opname, ins.BlockTypeIndex)
				return nil, nil, false
			}
			ft := m.Types[ins.BlockTypeIndex]
			return ft.Params, ft.Results, true
		}
		if ins.BlockType != nil {
			return nil, []wasmir.ValueType{*ins.BlockType}, true
		}
		return nil, nil, true
	}

	stackSigOperandText := func(sig instrdef.FixedStackSig) string {
		switch sig.ParamCount {
		case 0:
			return "no operands"
		case 1:
			return fmt.Sprintf("%s operand", sig.Params[0])
		case 2:
			if sig.Params[0] == sig.Params[1] {
				return fmt.Sprintf("%s operands", sig.Params[0])
			}
			return fmt.Sprintf("%s and %s operands", sig.Params[0], sig.Params[1])
		case 3:
			return fmt.Sprintf("%s, %s, and %s operands", sig.Params[0], sig.Params[1], sig.Params[2])
		default:
			return "operands"
		}
	}

	applyFixedStackSig := func(insCtx string, ins wasmir.Instruction, sig instrdef.FixedStackSig, hint valhint.InstrHints) {
		kind := ins.Kind
		if !ensureCurrentFrameOperands(int(sig.ParamCount), int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
			diags.Addf("%s: %s needs %d operands", insCtx, instrName(kind), sig.ParamCount)
			return
		}
		base := len(stack) - int(sig.ParamCount)
		for j := uint8(0); j < sig.ParamCount; j++ {
			if !matchesExpectedValueInModule(m, stackValue(base+int(j)), validatedValueFromType(sig.Params[j])) {
				diags.Addf("%s: %s expects %s", insCtx, instrName(kind), stackSigOperandText(sig))
				return
			}
		}
		truncateStack(base)
		for j := uint8(0); j < sig.ResultCount; j++ {
			appendStackType(sig.Results[j])
		}
	}

	for i, ins := range f.Body {
		insCtx := fmt.Sprintf("instruction %d", i)
		if ins.SourceLoc != "" {
			insCtx = fmt.Sprintf("%s at %s", insCtx, ins.SourceLoc)
		}
		hint := instrHintAt(i)

		if def, ok := instrdef.LookupInstructionByKind(ins.Kind); ok && def.Validate.StackSig.Enabled {
			applyFixedStackSig(insCtx, ins, def.Validate.StackSig, hint)
			continue
		}
		switch ins.Kind {
		case wasmir.InstrBlock:
			params, results, ok := controlSignature(ins, insCtx, "block")
			if !ok {
				continue
			}
			if !ensureCurrentFrameOperands(len(params), 0, 0) {
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
				kind:               controlKindBlock,
				entryHeight:        len(stack) - len(params),
				paramTypes:         params,
				resultTypes:        results,
				localInit:          append([]bool(nil), localInitialized...),
				enteredUnreachable: currentFrameUnreachable(),
				unreachable:        currentFrameUnreachable(),
			})
		case wasmir.InstrLoop:
			params, results, ok := controlSignature(ins, insCtx, "loop")
			if !ok {
				continue
			}
			if !ensureCurrentFrameOperands(len(params), 0, 0) {
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
				kind:               controlKindLoop,
				entryHeight:        len(stack) - len(params),
				paramTypes:         params,
				resultTypes:        results,
				localInit:          append([]bool(nil), localInitialized...),
				enteredUnreachable: currentFrameUnreachable(),
				unreachable:        currentFrameUnreachable(),
			})
		case wasmir.InstrIf:
			params, results, ok := controlSignature(ins, insCtx, "if")
			if !ok {
				continue
			}
			if !ensureCurrentFrameOperands(1+len(params), 0, 0) {
				diags.Addf("%s: if needs 1 i32 condition operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: if expects i32 condition operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 1) // pop condition
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
				kind:               controlKindIf,
				entryHeight:        len(stack) - len(params),
				paramTypes:         params,
				resultTypes:        results,
				localInit:          append([]bool(nil), localInitialized...),
				enteredUnreachable: currentFrameUnreachable(),
				unreachable:        currentFrameUnreachable(),
			})
		case wasmir.InstrTryTable:
			params, results, ok := controlSignature(ins, insCtx, "try_table")
			if !ok {
				continue
			}
			catchesOK := true
			for i, catch := range ins.TryTableCatches {
				if int(catch.LabelDepth) >= len(controlStack) {
					diags.Addf("%s: try_table catch %d label depth %d out of range", insCtx, i, catch.LabelDepth)
					catchesOK = false
					continue
				}
				target := controlStack[len(controlStack)-1-int(catch.LabelDepth)]
				targetValues := branchTargetTypes(target)
				catchValues, ok := tryTableCatchValues(catch)
				if !ok {
					if catch.Kind == wasmir.TryTableCatchKindTag || catch.Kind == wasmir.TryTableCatchKindTagRef {
						diags.Addf("%s: try_table catch %d tag index %d out of range", insCtx, i, catch.TagIndex)
					} else {
						diags.Addf("%s: try_table catch %d has invalid kind %d", insCtx, i, catch.Kind)
					}
					catchesOK = false
					continue
				}
				if len(catchValues) != len(targetValues) {
					diags.Addf("%s: try_table catch %d target type mismatch", insCtx, i)
					catchesOK = false
					continue
				}
				for j := range targetValues {
					if !matchesExpectedValueInModule(m, catchValues[j], targetValues[j]) {
						diags.Addf("%s: try_table catch %d target type mismatch at %d: got %s want %s", insCtx, i, j, validatedValueName(catchValues[j]), validatedValueName(targetValues[j]))
						catchesOK = false
						break
					}
				}
			}
			if !catchesOK {
				continue
			}
			if !ensureCurrentFrameOperands(len(params), 0, 0) {
				diags.Addf("%s: try_table needs %d parameter operands", insCtx, len(params))
				continue
			}
			base := len(stack) - len(params)
			matched := true
			for j := range params {
				want := valueAt(params, j)
				got := stackValue(base + j)
				if !matchesExpectedValueInModule(m, got, want) {
					diags.Addf("%s: try_table parameter %d expects %s", insCtx, j, validatedValueName(want))
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
				kind:               controlKindTryTable,
				entryHeight:        len(stack) - len(params),
				paramTypes:         params,
				resultTypes:        results,
				localInit:          append([]bool(nil), localInitialized...),
				enteredUnreachable: currentFrameUnreachable(),
				unreachable:        currentFrameUnreachable(),
			})
		case wasmir.InstrElse:
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
			validateFrameResult(insCtx, frame, "then-branch")
			truncateStack(frame.entryHeight + len(frame.paramTypes))
			localInitialized = append(localInitialized[:0], frame.localInit...)
			frame.sawElse = true
			frame.unreachable = frame.enteredUnreachable
			controlStack[len(controlStack)-1] = frame
		case wasmir.InstrLocalGet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if !localInitialized[ins.LocalIndex] {
				diags.Addf("%s: uninitialized local", insCtx)
				continue
			}
			appendStackValue(valueAt(locals, int(ins.LocalIndex)))
		case wasmir.InstrLocalSet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
		case wasmir.InstrLocalTee:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: local.tee needs 1 operand", insCtx)
				continue
			}
			want := valueAt(locals, int(ins.LocalIndex))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: local.tee expects %s operand", insCtx, validatedValueName(want))
				continue
			}
			localInitialized[ins.LocalIndex] = true
			// local.tee writes the local and leaves the local's declared type on
			// the stack, just like a subsequent local.get would.
			setStackValue(len(stack)-1, want)
		case wasmir.InstrGlobalGet:
			if int(ins.GlobalIndex) >= len(m.Globals) {
				diags.Addf("%s: global index %d out of range", insCtx, ins.GlobalIndex)
				continue
			}
			appendStackValue(validatedValueFromType(m.Globals[ins.GlobalIndex].Type))
		case wasmir.InstrGlobalSet:
			if int(ins.GlobalIndex) >= len(m.Globals) {
				diags.Addf("%s: global index %d out of range", insCtx, ins.GlobalIndex)
				continue
			}
			g := m.Globals[ins.GlobalIndex]
			if !g.Mutable {
				diags.Addf("%s: global.set on immutable global %d", insCtx, ins.GlobalIndex)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: global.set needs 1 operand", insCtx)
				continue
			}
			want := validatedValueFromType(g.Type)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: global.set expects %s operand", insCtx, validatedValueName(want))
				continue
			}
			truncateStack(len(stack) - 1)
		case wasmir.InstrTableGet:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: table.get needs 1 operand", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: table.get expects %s index operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(m.Tables[ins.TableIndex].RefType))
		case wasmir.InstrTableSet:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: table.set needs 2 operands", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: table.set expects %s index operand", insCtx, addrType)
				continue
			}
			want := validatedValueFromType(m.Tables[ins.TableIndex].RefType)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: table.set expects %s value operand", insCtx, validatedValueName(want))
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrTableCopy:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.TableIndex)
				continue
			}
			if int(ins.SourceTableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.SourceTableIndex)
				continue
			}
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
			if !stackValueHasType(len(stack)-3, dstAddrType) {
				diags.Addf("%s: table.copy expects %s destination index operand", insCtx, dstAddrType)
				continue
			}
			if !stackValueHasType(len(stack)-2, srcAddrType) {
				diags.Addf("%s: table.copy expects %s source index operand", insCtx, srcAddrType)
				continue
			}
			lenType := wasmir.ValueTypeI32
			if dstAddrType == wasmir.ValueTypeI64 && srcAddrType == wasmir.ValueTypeI64 {
				lenType = wasmir.ValueTypeI64
			}
			if !stackValueHasType(len(stack)-1, lenType) {
				diags.Addf("%s: table.copy expects %s length operand", insCtx, lenType)
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrTableFill:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.TableIndex)
				continue
			}
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: table.fill needs 3 operands", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-3, addrType) {
				diags.Addf("%s: table.fill expects %s index operand", insCtx, addrType)
				continue
			}
			want := validatedValueFromType(m.Tables[ins.TableIndex].RefType)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), want) {
				diags.Addf("%s: table.fill expects %s value operand", insCtx, validatedValueName(want))
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: table.fill expects %s length operand", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrTableInit:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: unknown table %d", insCtx, ins.TableIndex)
				continue
			}
			if int(ins.ElemIndex) >= len(m.Elements) {
				diags.Addf("%s: unknown elem segment %d", insCtx, ins.ElemIndex)
				continue
			}
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: table.init needs 3 operands", insCtx)
				continue
			}
			elemType, ok := elementRefType(m, ins.ElemIndex)
			if !ok || !matchesExpectedValueInModule(m, validatedValueFromType(elemType), validatedValueFromType(m.Tables[ins.TableIndex].RefType)) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-3, addrType) {
				diags.Addf("%s: table.init expects %s destination index operand", insCtx, addrType)
				continue
			}
			if !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) {
				diags.Addf("%s: table.init expects i32 source index operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: table.init expects i32 length operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrElemDrop:
			if int(ins.ElemIndex) >= len(m.Elements) {
				diags.Addf("%s: unknown elem segment %d", insCtx, ins.ElemIndex)
				continue
			}
		case wasmir.InstrTableGrow:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: table.grow needs 2 operands", insCtx)
				continue
			}
			want := validatedValueFromType(m.Tables[ins.TableIndex].RefType)
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), want) {
				diags.Addf("%s: table.grow expects %s value operand", insCtx, validatedValueName(want))
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: table.grow expects %s delta operand", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(addrType)
		case wasmir.InstrTableSize:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			appendStackType(tableAddressType(m, ins.TableIndex))
		case wasmir.InstrCall:
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
			if !ensureCurrentFrameOperands(len(calleeType.Params), int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
		case wasmir.InstrReturnCall:
			if ins.FuncIndex >= totalFuncCount {
				diags.Addf("%s: return_call function index %d out of range", insCtx, ins.FuncIndex)
				continue
			}
			calleeType, calleeDef, ok := functionTypeAtIndex(m, funcImportTypeIdx, ins.FuncIndex)
			calleeCtx := functionContext(m, ins.FuncIndex, funcImportCount)
			if !ok {
				diags.Addf("%s: return_call target %s has invalid type index", insCtx, calleeCtx)
				continue
			}
			if !ensureCurrentFrameOperands(len(calleeType.Params), int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: return_call to %s needs %d operands", insCtx, calleeCtx, len(calleeType.Params))
				continue
			}
			base := len(stack) - len(calleeType.Params)
			operandsOK := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: return_call to %s expects operand %s to be %s", insCtx, calleeCtx, operandLabelFromDef(calleeDef, j), validatedValueName(want))
					operandsOK = false
					break
				}
			}
			if !operandsOK {
				continue
			}
			if len(calleeType.Results) != len(ft.Results) {
				diags.Addf("%s: return_call result arity mismatch", insCtx)
				continue
			}
			resultsOK := true
			for j := range ft.Results {
				if !matchesExpectedValueInModule(m, validatedValueFromType(calleeType.Results[j]), valueAt(ft.Results, j)) {
					diags.Addf("%s: return_call result %d must be %s", insCtx, j, validatedValueName(valueAt(ft.Results, j)))
					resultsOK = false
					break
				}
			}
			if !resultsOK {
				continue
			}
			markCurrentFrameUnreachable()
		case wasmir.InstrCallIndirect:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: call_indirect table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if !matchesExpectedValueInModule(m, validatedValueFromType(m.Tables[ins.TableIndex].RefType), validatedValueFromType(wasmir.RefTypeFunc(true))) {
				diags.Addf("%s: call_indirect table must have function references", insCtx)
				continue
			}
			if int(ins.CallTypeIndex) >= len(m.Types) {
				diags.Addf("%s: call_indirect type index %d out of range", insCtx, ins.CallTypeIndex)
				continue
			}
			calleeType := m.Types[ins.CallTypeIndex]
			if calleeType.Kind != wasmir.TypeDefKindFunc {
				diags.Addf("%s: call_indirect type index %d is not a function type", insCtx, ins.CallTypeIndex)
				continue
			}
			need := len(calleeType.Params) + 1 // +1 for table element index
			if !ensureCurrentFrameOperands(need, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: call_indirect needs %d operands", insCtx, need)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-1, addrType) {
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
		case wasmir.InstrReturnCallIndirect:
			if int(ins.TableIndex) >= len(m.Tables) {
				diags.Addf("%s: return_call_indirect table index %d out of range", insCtx, ins.TableIndex)
				continue
			}
			if !matchesExpectedValueInModule(m, validatedValueFromType(m.Tables[ins.TableIndex].RefType), validatedValueFromType(wasmir.RefTypeFunc(true))) {
				diags.Addf("%s: return_call_indirect table must have function references", insCtx)
				continue
			}
			if int(ins.CallTypeIndex) >= len(m.Types) {
				diags.Addf("%s: return_call_indirect type index %d out of range", insCtx, ins.CallTypeIndex)
				continue
			}
			calleeType := m.Types[ins.CallTypeIndex]
			if calleeType.Kind != wasmir.TypeDefKindFunc {
				diags.Addf("%s: return_call_indirect type index %d is not a function type", insCtx, ins.CallTypeIndex)
				continue
			}
			need := len(calleeType.Params) + 1
			if !ensureCurrentFrameOperands(need, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: return_call_indirect needs %d operands", insCtx, need)
				continue
			}
			addrType := tableAddressType(m, ins.TableIndex)
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: return_call_indirect expects %s table index operand", insCtx, addrType)
				continue
			}
			base := len(stack) - 1 - len(calleeType.Params)
			ok := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: return_call_indirect expects operand %d to be %s", insCtx, j, validatedValueName(want))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			if len(calleeType.Results) != len(ft.Results) {
				diags.Addf("%s: return_call_indirect result arity mismatch", insCtx)
				continue
			}
			resultsOK := true
			for j := range ft.Results {
				if !matchesExpectedValueInModule(m, validatedValueFromType(calleeType.Results[j]), valueAt(ft.Results, j)) {
					diags.Addf("%s: return_call_indirect result %d must be %s", insCtx, j, validatedValueName(valueAt(ft.Results, j)))
					resultsOK = false
					break
				}
			}
			if !resultsOK {
				continue
			}
			markCurrentFrameUnreachable()
		case wasmir.InstrCallRef:
			if int(ins.CallTypeIndex) >= len(m.Types) {
				diags.Addf("%s: call_ref type index %d out of range", insCtx, ins.CallTypeIndex)
				continue
			}
			calleeType := m.Types[ins.CallTypeIndex]
			if calleeType.Kind != wasmir.TypeDefKindFunc {
				diags.Addf("%s: call_ref type index %d is not a function type", insCtx, ins.CallTypeIndex)
				continue
			}
			need := len(calleeType.Params) + 1
			if !ensureCurrentFrameOperands(need, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: call_ref needs %d operands", insCtx, need)
				continue
			}
			calleeRefWant := validatedValue{Type: wasmir.RefTypeIndexed(ins.CallTypeIndex, true)}
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
		case wasmir.InstrReturnCallRef:
			if int(ins.CallTypeIndex) >= len(m.Types) {
				diags.Addf("%s: return_call_ref type index %d out of range", insCtx, ins.CallTypeIndex)
				continue
			}
			calleeType := m.Types[ins.CallTypeIndex]
			if calleeType.Kind != wasmir.TypeDefKindFunc {
				diags.Addf("%s: return_call_ref type index %d is not a function type", insCtx, ins.CallTypeIndex)
				continue
			}
			need := len(calleeType.Params) + 1
			if !ensureCurrentFrameOperands(need, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: return_call_ref needs %d operands", insCtx, need)
				continue
			}
			calleeRefWant := validatedValue{Type: wasmir.RefTypeIndexed(ins.CallTypeIndex, true)}
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), calleeRefWant) {
				diags.Addf("%s: return_call_ref expects operand of type %s", insCtx, validatedValueName(calleeRefWant))
				continue
			}
			base := len(stack) - 1 - len(calleeType.Params)
			ok := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: return_call_ref expects operand %d to be %s", insCtx, j, validatedValueName(want))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			if len(calleeType.Results) != len(ft.Results) {
				diags.Addf("%s: return_call_ref result arity mismatch", insCtx)
				continue
			}
			resultsOK := true
			for j := range ft.Results {
				if !matchesExpectedValueInModule(m, validatedValueFromType(calleeType.Results[j]), valueAt(ft.Results, j)) {
					diags.Addf("%s: return_call_ref result %d must be %s", insCtx, j, validatedValueName(valueAt(ft.Results, j)))
					resultsOK = false
					break
				}
			}
			if !resultsOK {
				continue
			}
			markCurrentFrameUnreachable()
		case wasmir.InstrThrow:
			tagType, ok := tagTypeAtIndex(m, tagImportTypeIdx, ins.TagIndex)
			if !ok {
				diags.Addf("%s: throw tag index %d out of range", insCtx, ins.TagIndex)
				continue
			}
			if len(tagType.Results) != 0 {
				diags.Addf("%s: throw tag %d must reference a function type with no results", insCtx, ins.TagIndex)
				continue
			}
			if !ensureCurrentFrameOperands(len(tagType.Params), int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: throw needs %d operands", insCtx, len(tagType.Params))
				continue
			}
			base := len(stack) - len(tagType.Params)
			paramsOK := true
			for j := range tagType.Params {
				want := valueAt(tagType.Params, j)
				if !matchesExpectedValueInModule(m, stackValue(base+j), want) {
					diags.Addf("%s: throw expects operand %d to be %s", insCtx, j, validatedValueName(want))
					paramsOK = false
					break
				}
			}
			if !paramsOK {
				continue
			}
			truncateStack(base)
			markCurrentFrameUnreachable()
		case wasmir.InstrThrowRef:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: throw_ref needs 1 operand", insCtx)
				continue
			}
			want := validatedValueFromType(wasmir.RefTypeExn(true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), want) {
				diags.Addf("%s: throw_ref expects operand of type %s", insCtx, validatedValueName(want))
				continue
			}
			truncateStack(len(stack) - 1)
			markCurrentFrameUnreachable()
		case wasmir.InstrStructNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.new type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindStruct {
				diags.Addf("%s: struct.new type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if !ensureCurrentFrameOperands(len(td.Fields), int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
			appendStackValue(validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrStructNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.new_default type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindStruct {
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
			appendStackValue(validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrStructGet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.get type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindStruct {
				diags.Addf("%s: struct.get type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.get field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: struct.get needs 1 operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(td.Fields[ins.FieldIndex].Type))
		case wasmir.InstrStructGetS:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.get_s type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindStruct {
				diags.Addf("%s: struct.get_s type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.get_s field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			field := td.Fields[ins.FieldIndex]
			if field.Packed == wasmir.PackedTypeNone {
				diags.Addf("%s: struct.get_s requires packed field type", insCtx)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: struct.get_s needs 1 operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get_s expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrStructGetU:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.get_u type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindStruct {
				diags.Addf("%s: struct.get_u type index %d is not a struct type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.FieldIndex) >= len(td.Fields) {
				diags.Addf("%s: struct.get_u field index %d out of range", insCtx, ins.FieldIndex)
				continue
			}
			field := td.Fields[ins.FieldIndex]
			if field.Packed == wasmir.PackedTypeNone {
				diags.Addf("%s: struct.get_u requires packed field type", insCtx)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: struct.get_u needs 1 operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get_u expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrStructSet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: struct.set type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindStruct {
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
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: struct.set needs 2 operands", insCtx)
				continue
			}
			wantValue := validatedValueFromType(fieldValueType(field))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantValue) {
				diags.Addf("%s: struct.set expects value operand of type %s", insCtx, validatedValueName(wantValue))
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: struct.set expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrArrayLen:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.len needs 1 operand", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, wasmir.RefTypeArray(true)) {
				diags.Addf("%s: array.len expects array reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrArrayNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_default type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.new_default type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !isDefaultableValueType(fieldValueType(td.ElemField)) {
				diags.Addf("%s: array.new_default requires defaultable element type", insCtx)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.new_default needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.new_default expects i32 length operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrArrayNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.new type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.new needs 2 operands", insCtx)
				continue
			}
			elemType := fieldValueType(td.ElemField)
			if !matchesGCExpectedValue(m, stackValue(len(stack)-2).Type, elemType) {
				diags.Addf("%s: array.new expects element operand of type %s", insCtx, elemType)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.new expects i32 length operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrArrayNewFixed:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_fixed type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.new_fixed type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !ensureCurrentFrameOperands(int(ins.FixedCount), int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
			appendStackValue(validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrArrayNewData:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_data type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.new_data type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if int(ins.DataIndex) >= len(m.Data) {
				diags.Addf("%s: array.new_data data index %d out of range", insCtx, ins.DataIndex)
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.new_data needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.new_data expects i32 offset and i32 length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrArrayNewElem:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.new_elem type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
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
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.new_elem needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.new_elem expects i32 offset and i32 length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, false)))
		case wasmir.InstrArrayInitData:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.init_data type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
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
			if !ensureCurrentFrameOperands(4, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.init_data needs 4 operands", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-4), wantRef) {
				diags.Addf("%s: array.init_data expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			if !stackValueHasType(len(stack)-3, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.init_data expects i32 destination, source, and length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 4)
		case wasmir.InstrArrayInitElem:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.init_elem type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
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
			if !ensureCurrentFrameOperands(4, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.init_elem needs 4 operands", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-4), wantRef) {
				diags.Addf("%s: array.init_elem expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			if !stackValueHasType(len(stack)-3, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.init_elem expects i32 destination, source, and length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 4)
		case wasmir.InstrArrayGet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.get type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.get type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.get needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.get expects i32 index operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: array.get expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(fieldValueType(td.ElemField)))
		case wasmir.InstrArrayGetS, wasmir.InstrArrayGetU:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: %s type index %d out of range", insCtx, instrName(ins.Kind), ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: %s type index %d is not an array type", insCtx, instrName(ins.Kind), ins.TypeIndex)
				continue
			}
			if td.ElemField.Packed == wasmir.PackedTypeNone {
				diags.Addf("%s: %s requires packed array element type", insCtx, instrName(ins.Kind))
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: %s expects i32 index operand", insCtx, instrName(ins.Kind))
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: %s expects operand of type %s", insCtx, instrName(ins.Kind), validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrArraySet:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.set type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.set type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !td.ElemField.Mutable {
				diags.Addf("%s: array.set requires mutable array", insCtx)
				continue
			}
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.set needs 3 operands", insCtx)
				continue
			}
			wantValue := validatedValueFromType(fieldValueType(td.ElemField))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-1), wantValue) {
				diags.Addf("%s: array.set expects value operand of type %s", insCtx, validatedValueName(wantValue))
				continue
			}
			if !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.set expects i32 index operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-3), wantRef) {
				diags.Addf("%s: array.set expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrArrayFill:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.fill type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if td.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.fill type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			if !td.ElemField.Mutable {
				diags.Addf("%s: immutable array", insCtx)
				continue
			}
			if !ensureCurrentFrameOperands(4, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.fill needs 4 operands", insCtx)
				continue
			}
			wantRef := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-4), wantRef) {
				diags.Addf("%s: array.fill expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			if !stackValueHasType(len(stack)-3, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.fill expects i32 index operand", insCtx)
				continue
			}
			wantValue := validatedValueFromType(fieldValueType(td.ElemField))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-2), wantValue) {
				diags.Addf("%s: type mismatch", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.fill expects i32 length operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 4)
		case wasmir.InstrArrayCopy:
			dstType, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok {
				diags.Addf("%s: array.copy destination type index %d out of range", insCtx, ins.TypeIndex)
				continue
			}
			if dstType.Kind != wasmir.TypeDefKindArray {
				diags.Addf("%s: array.copy destination type index %d is not an array type", insCtx, ins.TypeIndex)
				continue
			}
			srcType, ok := typeDefAtIndex(m, ins.SourceTypeIndex)
			if !ok {
				diags.Addf("%s: array.copy source type index %d out of range", insCtx, ins.SourceTypeIndex)
				continue
			}
			if srcType.Kind != wasmir.TypeDefKindArray {
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
			if !ensureCurrentFrameOperands(5, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: array.copy needs 5 operands", insCtx)
				continue
			}
			dstWant := validatedValueFromType(wasmir.RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-5), dstWant) {
				diags.Addf("%s: array.copy expects destination operand of type %s", insCtx, validatedValueName(dstWant))
				continue
			}
			if !stackValueHasType(len(stack)-4, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.copy expects i32 destination index operand", insCtx)
				continue
			}
			srcWant := validatedValueFromType(wasmir.RefTypeIndexed(ins.SourceTypeIndex, true))
			if !matchesExpectedValueInModule(m, stackValue(len(stack)-3), srcWant) {
				diags.Addf("%s: array.copy expects source operand of type %s", insCtx, validatedValueName(srcWant))
				continue
			}
			if !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: array.copy expects i32 source index and length operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 5)
		case wasmir.InstrRefEq:
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: ref.eq needs 2 operands", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-2).Type, wasmir.RefTypeEq(true)) ||
				!matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, wasmir.RefTypeEq(true)) {
				diags.Addf("%s: ref.eq expects eqref operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrRefTest:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: ref.test needs 1 operand", insCtx)
				continue
			}
			if !stackValueIsRef(len(stack) - 1) {
				diags.Addf("%s: ref.test expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrRefCast:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: ref.cast needs 1 operand", insCtx)
				continue
			}
			if !stackValueIsRef(len(stack) - 1) {
				diags.Addf("%s: ref.cast expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(ins.RefType))
		case wasmir.InstrRefI31:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: ref.i31 needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: ref.i31 expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.RefTypeI31(false)))
		case wasmir.InstrExternConvertAny:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: extern.convert_any needs 1 operand", insCtx)
				continue
			}
			got := stackValue(len(stack) - 1)
			if !matchesGCExpectedValue(m, got.Type, wasmir.RefTypeAny(true)) {
				diags.Addf("%s: extern.convert_any expects any reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.RefTypeExtern(got.Type.Nullable)))
		case wasmir.InstrAnyConvertExtern:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: any.convert_extern needs 1 operand", insCtx)
				continue
			}
			got := stackValue(len(stack) - 1)
			if !matchesGCExpectedValue(m, got.Type, wasmir.RefTypeExtern(true)) {
				diags.Addf("%s: any.convert_extern expects extern reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.RefTypeAny(got.Type.Nullable)))
		case wasmir.InstrI31GetS:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i31.get_s needs 1 operand", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, wasmir.RefTypeI31(true)) {
				diags.Addf("%s: i31.get_s expects i31 reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrI31GetU:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i31.get_u needs 1 operand", insCtx)
				continue
			}
			if !matchesGCExpectedValue(m, stackValue(len(stack)-1).Type, wasmir.RefTypeI31(true)) {
				diags.Addf("%s: i31.get_u expects i31 reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))

		case wasmir.InstrI32Const:
			appendStackType(wasmir.ValueTypeI32)

		case wasmir.InstrI64Const:
			appendStackType(wasmir.ValueTypeI64)

		case wasmir.InstrF32Const:
			appendStackType(wasmir.ValueTypeF32)

		case wasmir.InstrF64Const:
			appendStackType(wasmir.ValueTypeF64)

		case wasmir.InstrV128Const:
			appendStackType(wasmir.ValueTypeV128)

		case wasmir.InstrDrop:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: drop needs 1 operand", insCtx)
				continue
			}
			truncateStack(len(stack) - 1)
		case wasmir.InstrSelect:
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: select needs 3 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: select expects i32 condition operand", insCtx)
				continue
			}
			v2 := stackValue(len(stack) - 2)
			v1 := stackValue(len(stack) - 3)
			truncateStack(len(stack) - 3)
			if ins.SelectType != nil {
				if !validModuleValueType(m, *ins.SelectType) {
					diags.Addf("%s: invalid select result type %s", insCtx, *ins.SelectType)
					continue
				}
				want := validatedValueFromType(*ins.SelectType)
				if !matchesExpectedValueInModule(m, v1, want) || !matchesExpectedValueInModule(m, v2, want) {
					diags.Addf("%s: select expects operands of type %s", insCtx, validatedValueName(want))
					continue
				}
				appendStackValue(want)
				continue
			}
			if !sameValidatedValue(v1, v2) {
				diags.Addf("%s: select expects same-typed value operands", insCtx)
				continue
			}
			if (!v1.Unknown && v1.Type.IsRef()) || (!v2.Unknown && v2.Type.IsRef()) {
				diags.Addf("%s: select expects non-reference value operands", insCtx)
				continue
			}
			switch {
			case v1.Unknown && !v2.Unknown:
				appendStackValue(v2)
			case v2.Unknown && !v1.Unknown:
				appendStackValue(v1)
			case v1.Unknown && v2.Unknown:
				appendStackValue(validatedUnknownValue())
			default:
				appendStackValue(v1)
			}
		case wasmir.InstrI32Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i32.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i32.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i32.load needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: i32.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrI64Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i64.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i64.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i64.load needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: i64.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI64))
		case wasmir.InstrF32Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f32.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f32.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f32.load needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: f32.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeF32))
		case wasmir.InstrF64Load:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f64.load requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f64.load memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f64.load needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: f64.load expects %s address operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeF64))
		case wasmir.InstrV128Load, wasmir.InstrV128Load8x8S, wasmir.InstrV128Load8x8U, wasmir.InstrV128Load16x4S, wasmir.InstrV128Load16x4U, wasmir.InstrV128Load32x2S, wasmir.InstrV128Load32x2U, wasmir.InstrV128Load8Splat, wasmir.InstrV128Load16Splat, wasmir.InstrV128Load32Splat, wasmir.InstrV128Load64Splat, wasmir.InstrV128Load32Zero, wasmir.InstrV128Load64Zero:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: %s expects %s address operand", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeV128))
		case wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane,
			wasmir.InstrV128Store8Lane, wasmir.InstrV128Store16Lane, wasmir.InstrV128Store32Lane, wasmir.InstrV128Store64Lane:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			laneLimit := uint32(0)
			switch ins.Kind {
			case wasmir.InstrV128Load8Lane, wasmir.InstrV128Store8Lane:
				laneLimit = 16
			case wasmir.InstrV128Load16Lane, wasmir.InstrV128Store16Lane:
				laneLimit = 8
			case wasmir.InstrV128Load32Lane, wasmir.InstrV128Store32Lane:
				laneLimit = 4
			case wasmir.InstrV128Load64Lane, wasmir.InstrV128Store64Lane:
				laneLimit = 2
			}
			if ins.LaneIndex >= laneLimit {
				diags.Addf("%s: %s lane %d out of range", insCtx, instrName(ins.Kind), ins.LaneIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: %s expects v128 value and %s address operands", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			truncateStack(len(stack) - 2)
			switch ins.Kind {
			case wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane:
				appendStackType(wasmir.ValueTypeV128)
			}
		case wasmir.InstrI32Load8S, wasmir.InstrI32Load8U, wasmir.InstrI32Load16S, wasmir.InstrI32Load16U:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: %s expects %s address operand", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrI64Load8S, wasmir.InstrI64Load8U, wasmir.InstrI64Load16S, wasmir.InstrI64Load16U, wasmir.InstrI64Load32S, wasmir.InstrI64Load32U:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: %s expects %s address operand", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI64))
		case wasmir.InstrI32Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i32.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i32.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i32.store needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: i32.store expects i32 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrI64Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: i64.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: i64.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i64.store needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: i64.store expects i64 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrI32Store8, wasmir.InstrI32Store16:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: %s expects i32 value and %s address operands", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrI64Store8, wasmir.InstrI64Store16, wasmir.InstrI64Store32:
			if len(m.Memories) == 0 {
				diags.Addf("%s: %s requires memory", insCtx, instrName(ins.Kind))
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: %s memory index %d out of range", insCtx, instrName(ins.Kind), ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, instrName(ins.Kind))
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: %s expects i64 value and %s address operands", insCtx, instrName(ins.Kind), addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrF32Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f32.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f32.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f32.store needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF32) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: f32.store expects f32 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrF64Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: f64.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: f64.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f64.store needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF64) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: f64.store expects f64 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrV128Store:
			if len(m.Memories) == 0 {
				diags.Addf("%s: v128.store requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: v128.store memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: v128.store needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, addrType) {
				diags.Addf("%s: v128.store expects v128 value and %s address operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 2)
		case wasmir.InstrMemorySize:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.size requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.size memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			appendStackType(memoryAddressType(m, ins.MemoryIndex))
		case wasmir.InstrMemoryGrow:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.grow requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.grow memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: memory.grow needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: memory.grow expects %s operand", insCtx, addrType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(addrType))
		case wasmir.InstrMemoryCopy:
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
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: memory.copy needs 3 operands", insCtx)
				continue
			}
			dstAddrType := memoryAddressType(m, ins.MemoryIndex)
			srcAddrType := memoryAddressType(m, ins.SourceMemoryIndex)
			if !stackValueHasType(len(stack)-3, dstAddrType) || !stackValueHasType(len(stack)-2, srcAddrType) || !stackValueHasType(len(stack)-1, dstAddrType) {
				diags.Addf("%s: memory.copy expects %s destination, %s source, and %s length operands", insCtx, dstAddrType, srcAddrType, dstAddrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrMemoryInit:
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
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: memory.init needs 3 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-3, addrType) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: memory.init expects %s destination, i32 source, and i32 length operands", insCtx, addrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrMemoryFill:
			if len(m.Memories) == 0 {
				diags.Addf("%s: memory.fill requires memory", insCtx)
				continue
			}
			if int(ins.MemoryIndex) >= len(m.Memories) {
				diags.Addf("%s: memory.fill memory index %d out of range", insCtx, ins.MemoryIndex)
				continue
			}
			addrType := memoryAddressType(m, ins.MemoryIndex)
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: memory.fill needs 3 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-3, addrType) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-1, addrType) {
				diags.Addf("%s: memory.fill expects %s destination, i32 value, and %s length operands", insCtx, addrType, addrType)
				continue
			}
			truncateStack(len(stack) - 3)
		case wasmir.InstrDataDrop:
			if int(ins.DataIndex) >= len(m.Data) {
				diags.Addf("%s: data.drop data index %d out of range", insCtx, ins.DataIndex)
				continue
			}
		case wasmir.InstrBr:
			target, _, _, ok := validateBranchTarget(insCtx, ins.BranchDepth, "br")
			if !ok {
				continue
			}
			_ = target
			markCurrentFrameUnreachable()
		case wasmir.InstrBrIf:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: br_if needs 1 i32 condition operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
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
		case wasmir.InstrBrOnNull:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: br_on_null needs 1 reference operand", insCtx)
				continue
			}
			refVal := stackValue(len(stack) - 1)
			if !stackValueIsRef(len(stack) - 1) {
				diags.Addf("%s: br_on_null expects reference operand", insCtx)
				continue
			}
			if int(ins.BranchDepth) >= len(controlStack) {
				diags.Addf("%s: br_on_null depth %d out of range", insCtx, ins.BranchDepth)
				continue
			}
			target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
			targetValues := branchTargetTypes(target)
			if !ensureCurrentFrameOperands(len(targetValues)+1, 0, 0) {
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
		case wasmir.InstrBrOnNonNull:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: br_on_non_null needs 1 reference operand", insCtx)
				continue
			}
			refVal := stackValue(len(stack) - 1)
			if !stackValueIsRef(len(stack) - 1) {
				diags.Addf("%s: br_on_non_null expects reference operand", insCtx)
				continue
			}
			if int(ins.BranchDepth) >= len(controlStack) {
				diags.Addf("%s: br_on_non_null depth %d out of range", insCtx, ins.BranchDepth)
				continue
			}
			target := controlStack[len(controlStack)-1-int(ins.BranchDepth)]
			targetValues := branchTargetTypes(target)
			if len(targetValues) == 0 || !ensureCurrentFrameOperands(len(targetValues), 0, 0) {
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
		case wasmir.InstrBrOnCast:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
			if len(targetValues) == 0 || !ensureCurrentFrameOperands(len(targetValues), 0, 0) {
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
		case wasmir.InstrBrOnCastFail:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
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
			if len(targetValues) == 0 || !ensureCurrentFrameOperands(len(targetValues), 0, 0) {
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
		case wasmir.InstrBrTable:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: br_table needs 1 i32 selector operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
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
		case wasmir.InstrUnreachable:
			markCurrentFrameUnreachable()
		case wasmir.InstrReturn:
			if !ensureCurrentFrameOperands(len(ft.Results), 0, 0) {
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
			markCurrentFrameUnreachable()

		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul, wasmir.InstrI32DivS, wasmir.InstrI32DivU,
			wasmir.InstrI32RemS, wasmir.InstrI32RemU, wasmir.InstrI32Shl, wasmir.InstrI32ShrS, wasmir.InstrI32ShrU,
			wasmir.InstrI32And, wasmir.InstrI32Or, wasmir.InstrI32Xor, wasmir.InstrI32Rotl, wasmir.InstrI32Rotr:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) {
				diags.Addf("%s: %s expects i32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeI32)

		case wasmir.InstrI32Eq, wasmir.InstrI32Ne, wasmir.InstrI32LtS, wasmir.InstrI32LtU, wasmir.InstrI32LeS, wasmir.InstrI32LeU,
			wasmir.InstrI32GtS, wasmir.InstrI32GtU, wasmir.InstrI32GeS, wasmir.InstrI32GeU:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI32) {
				diags.Addf("%s: %s expects i32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeI32)
		case wasmir.InstrI32Eqz:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i32.eqz needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: i32.eqz expects i32 operand", insCtx)
				continue
			}
			// i32.eqz replaces i32 with i32 at top-of-stack.
		case wasmir.InstrI32Clz, wasmir.InstrI32Ctz, wasmir.InstrI32Popcnt, wasmir.InstrI32Extend8S, wasmir.InstrI32Extend16S:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: %s expects i32 operand", insCtx, name)
				continue
			}
			// i32 unary operators preserve i32 on stack.

		case wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul, wasmir.InstrI64DivS, wasmir.InstrI64DivU,
			wasmir.InstrI64RemS, wasmir.InstrI64RemU, wasmir.InstrI64Shl, wasmir.InstrI64ShrS, wasmir.InstrI64ShrU,
			wasmir.InstrI64And, wasmir.InstrI64Or, wasmir.InstrI64Xor, wasmir.InstrI64Rotl, wasmir.InstrI64Rotr:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI64) {
				diags.Addf("%s: %s expects i64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeI64)

		case wasmir.InstrI64Eq, wasmir.InstrI64Ne, wasmir.InstrI64LtS, wasmir.InstrI64LtU, wasmir.InstrI64GtS, wasmir.InstrI64GtU,
			wasmir.InstrI64LeS, wasmir.InstrI64LeU, wasmir.InstrI64GeS, wasmir.InstrI64GeU:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeI64) {
				diags.Addf("%s: %s expects i64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeI32)
		case wasmir.InstrI64Eqz:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i64.eqz needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) {
				diags.Addf("%s: i64.eqz expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrI64Clz, wasmir.InstrI64Ctz, wasmir.InstrI64Popcnt, wasmir.InstrI64Extend8S, wasmir.InstrI64Extend16S, wasmir.InstrI64Extend32S:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) {
				diags.Addf("%s: %s expects i64 operand", insCtx, name)
				continue
			}
			// i64 unary operators preserve i64 on stack.

		case wasmir.InstrI32WrapI64:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i32.wrap_i64 needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) {
				diags.Addf("%s: i32.wrap_i64 expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))

		case wasmir.InstrI64ExtendI32S, wasmir.InstrI64ExtendI32U:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: %s expects i32 operand", insCtx, name)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI64))

		case wasmir.InstrF32ConvertI32S:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f32.convert_i32_s needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: f32.convert_i32_s expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeF32))

		case wasmir.InstrF64ConvertI64S:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f64.convert_i64_s needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) {
				diags.Addf("%s: f64.convert_i64_s expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeF64))

		case wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div, wasmir.InstrF32Min, wasmir.InstrF32Max:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeF32) {
				diags.Addf("%s: %s expects f32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeF32)

		case wasmir.InstrF32Sqrt, wasmir.InstrF32Ceil, wasmir.InstrF32Floor, wasmir.InstrF32Trunc, wasmir.InstrF32Nearest, wasmir.InstrF32Neg:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF32) {
				diags.Addf("%s: %s expects f32 operand", insCtx, name)
				continue
			}
			// Unary f32 operators preserve top-of-stack type.
		case wasmir.InstrF32Eq, wasmir.InstrF32Lt, wasmir.InstrF32Gt, wasmir.InstrF32Ne:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeF32) {
				diags.Addf("%s: %s expects f32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeI32)

		case wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div, wasmir.InstrF64Min, wasmir.InstrF64Max:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF64) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeF64) {
				diags.Addf("%s: %s expects f64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeF64)

		case wasmir.InstrF64Sqrt, wasmir.InstrF64Ceil, wasmir.InstrF64Floor, wasmir.InstrF64Trunc, wasmir.InstrF64Nearest, wasmir.InstrF64Neg:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF64) {
				diags.Addf("%s: %s expects f64 operand", insCtx, name)
				continue
			}
			// Unary f64 operators preserve top-of-stack type.
		case wasmir.InstrF64Eq, wasmir.InstrF64Le:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF64) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeF64) {
				diags.Addf("%s: %s expects f64 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeI32)
		case wasmir.InstrI8x16Shuffle:
			lanesOK := true
			for _, lane := range ins.ShuffleLanes {
				if lane >= 32 {
					diags.Addf("%s: i8x16.shuffle lane %d out of range", insCtx, lane)
					lanesOK = false
					break
				}
			}
			if !lanesOK {
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i8x16.shuffle needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) {
				diags.Addf("%s: i8x16.shuffle expects v128 operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrI8x16Swizzle:
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i8x16.swizzle needs 2 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) {
				diags.Addf("%s: i8x16.swizzle expects v128 operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrV128AnyTrue, wasmir.InstrI8x16AllTrue, wasmir.InstrI8x16Bitmask,
			wasmir.InstrI16x8AllTrue, wasmir.InstrI16x8Bitmask,
			wasmir.InstrI32x4AllTrue, wasmir.InstrI32x4Bitmask,
			wasmir.InstrI64x2AllTrue, wasmir.InstrI64x2Bitmask:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects v128 operand", insCtx, name)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrV128Not:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: v128.not needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) {
				diags.Addf("%s: v128.not expects v128 operand", insCtx)
				continue
			}
		case wasmir.InstrV128And, wasmir.InstrV128AndNot, wasmir.InstrV128Or, wasmir.InstrV128Xor:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects v128 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrI8x16Shl, wasmir.InstrI8x16ShrS, wasmir.InstrI8x16ShrU,
			wasmir.InstrI16x8Shl, wasmir.InstrI16x8ShrS, wasmir.InstrI16x8ShrU,
			wasmir.InstrI32x4Shl, wasmir.InstrI32x4ShrS, wasmir.InstrI32x4ShrU,
			wasmir.InstrI64x2Shl, wasmir.InstrI64x2ShrS, wasmir.InstrI64x2ShrU:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects v128 and i32 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrI8x16Splat, wasmir.InstrI16x8Splat, wasmir.InstrI32x4Splat, wasmir.InstrI64x2Splat, wasmir.InstrF32x4Splat, wasmir.InstrF64x2Splat:
			name := instrName(ins.Kind)
			operandType := wasmir.ValueTypeI32
			switch ins.Kind {
			case wasmir.InstrI8x16Splat, wasmir.InstrI16x8Splat, wasmir.InstrI32x4Splat:
				operandType = wasmir.ValueTypeI32
			case wasmir.InstrI64x2Splat:
				operandType = wasmir.ValueTypeI64
			case wasmir.InstrF32x4Splat:
				operandType = wasmir.ValueTypeF32
			case wasmir.InstrF64x2Splat:
				operandType = wasmir.ValueTypeF64
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, operandType) {
				diags.Addf("%s: %s expects %s operand", insCtx, name, operandType)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeV128))
		case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU,
			wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU,
			wasmir.InstrI32x4ExtractLane,
			wasmir.InstrI64x2ExtractLane,
			wasmir.InstrF32x4ExtractLane,
			wasmir.InstrF64x2ExtractLane:
			name := instrName(ins.Kind)
			laneLimit := uint32(0)
			resultType := wasmir.ValueTypeI32
			switch ins.Kind {
			case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU:
				laneLimit = 16
				resultType = wasmir.ValueTypeI32
			case wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU:
				laneLimit = 8
				resultType = wasmir.ValueTypeI32
			case wasmir.InstrI32x4ExtractLane:
				laneLimit = 4
				resultType = wasmir.ValueTypeI32
			case wasmir.InstrI64x2ExtractLane:
				laneLimit = 2
				resultType = wasmir.ValueTypeI64
			case wasmir.InstrF32x4ExtractLane:
				laneLimit = 4
				resultType = wasmir.ValueTypeF32
			case wasmir.InstrF64x2ExtractLane:
				laneLimit = 2
				resultType = wasmir.ValueTypeF64
			}
			if ins.LaneIndex >= laneLimit {
				diags.Addf("%s: %s lane %d out of range", insCtx, name, ins.LaneIndex)
				continue
			}
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects v128 operand", insCtx, name)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(resultType))
		case wasmir.InstrI8x16ReplaceLane, wasmir.InstrI16x8ReplaceLane, wasmir.InstrI32x4ReplaceLane,
			wasmir.InstrI64x2ReplaceLane, wasmir.InstrF32x4ReplaceLane, wasmir.InstrF64x2ReplaceLane:
			name := instrName(ins.Kind)
			laneLimit := uint32(0)
			valueType := wasmir.ValueTypeI32
			switch ins.Kind {
			case wasmir.InstrI8x16ReplaceLane:
				laneLimit = 16
				valueType = wasmir.ValueTypeI32
			case wasmir.InstrI16x8ReplaceLane:
				laneLimit = 8
				valueType = wasmir.ValueTypeI32
			case wasmir.InstrI32x4ReplaceLane:
				laneLimit = 4
				valueType = wasmir.ValueTypeI32
			case wasmir.InstrI64x2ReplaceLane:
				laneLimit = 2
				valueType = wasmir.ValueTypeI64
			case wasmir.InstrF32x4ReplaceLane:
				laneLimit = 4
				valueType = wasmir.ValueTypeF32
			case wasmir.InstrF64x2ReplaceLane:
				laneLimit = 2
				valueType = wasmir.ValueTypeF64
			}
			if ins.LaneIndex >= laneLimit {
				diags.Addf("%s: %s lane %d out of range", insCtx, name, ins.LaneIndex)
				continue
			}
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, valueType) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects %s and %s operands", insCtx, name, wasmir.ValueTypeV128, valueType)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrI8x16NarrowI16x8S, wasmir.InstrI8x16NarrowI16x8U,
			wasmir.InstrI16x8Eq, wasmir.InstrI16x8Ne, wasmir.InstrI16x8LtS, wasmir.InstrI16x8LtU, wasmir.InstrI16x8GtS, wasmir.InstrI16x8GtU, wasmir.InstrI16x8LeS, wasmir.InstrI16x8LeU, wasmir.InstrI16x8GeS, wasmir.InstrI16x8GeU,
			wasmir.InstrI16x8NarrowI32x4S, wasmir.InstrI16x8NarrowI32x4U,
			wasmir.InstrI16x8Add, wasmir.InstrI16x8AddSatS, wasmir.InstrI16x8AddSatU,
			wasmir.InstrI16x8Sub, wasmir.InstrI16x8SubSatS, wasmir.InstrI16x8SubSatU,
			wasmir.InstrI16x8Mul, wasmir.InstrI16x8MinS, wasmir.InstrI16x8MinU, wasmir.InstrI16x8MaxS, wasmir.InstrI16x8MaxU, wasmir.InstrI16x8AvgrU,
			wasmir.InstrI16x8Q15mulrSatS,
			wasmir.InstrI16x8ExtmulLowI8x16S, wasmir.InstrI16x8ExtmulHighI8x16S, wasmir.InstrI16x8ExtmulLowI8x16U, wasmir.InstrI16x8ExtmulHighI8x16U,
			wasmir.InstrI32x4Eq, wasmir.InstrI32x4LtS, wasmir.InstrI32x4Add, wasmir.InstrI32x4Sub, wasmir.InstrI32x4Mul, wasmir.InstrI32x4MinS,
			wasmir.InstrI64x2Add,
			wasmir.InstrF32x4Eq, wasmir.InstrF32x4Ne, wasmir.InstrF32x4Lt, wasmir.InstrF32x4Gt, wasmir.InstrF32x4Le, wasmir.InstrF32x4Ge,
			wasmir.InstrF32x4Add, wasmir.InstrF32x4Sub, wasmir.InstrF32x4Mul, wasmir.InstrF32x4Div, wasmir.InstrF32x4Min, wasmir.InstrF32x4Max, wasmir.InstrF32x4Pmin, wasmir.InstrF32x4Pmax,
			wasmir.InstrF64x2Eq, wasmir.InstrF64x2Ne, wasmir.InstrF64x2Lt, wasmir.InstrF64x2Gt, wasmir.InstrF64x2Le, wasmir.InstrF64x2Ge,
			wasmir.InstrF64x2Add, wasmir.InstrF64x2Sub, wasmir.InstrF64x2Mul, wasmir.InstrF64x2Div, wasmir.InstrF64x2Min, wasmir.InstrF64x2Max, wasmir.InstrF64x2Pmin, wasmir.InstrF64x2Pmax:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(2, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects v128 operands", insCtx, name)
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrI16x8ExtaddPairwiseI8x16S, wasmir.InstrI16x8ExtaddPairwiseI8x16U,
			wasmir.InstrI16x8Abs, wasmir.InstrI16x8Neg,
			wasmir.InstrI16x8ExtendLowI8x16S, wasmir.InstrI16x8ExtendLowI8x16U,
			wasmir.InstrI32x4ExtendLowI16x8S, wasmir.InstrI32x4ExtendLowI16x8U,
			wasmir.InstrF32x4Ceil, wasmir.InstrF32x4Floor, wasmir.InstrF32x4Trunc, wasmir.InstrF32x4Nearest,
			wasmir.InstrF32x4Abs, wasmir.InstrF32x4Neg, wasmir.InstrF32x4Sqrt,
			wasmir.InstrF64x2Ceil, wasmir.InstrF64x2Floor, wasmir.InstrF64x2Trunc, wasmir.InstrF64x2Nearest,
			wasmir.InstrF64x2Abs, wasmir.InstrF64x2Neg, wasmir.InstrF64x2Sqrt,
			wasmir.InstrF32x4ConvertI32x4S, wasmir.InstrF32x4ConvertI32x4U,
			wasmir.InstrF64x2ConvertLowI32x4S, wasmir.InstrF64x2ConvertLowI32x4U,
			wasmir.InstrF32x4DemoteF64x2Zero, wasmir.InstrF64x2PromoteLowF32x4,
			wasmir.InstrI32x4Neg:
			name := instrName(ins.Kind)
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: %s needs 1 operand", insCtx, name)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) {
				diags.Addf("%s: %s expects v128 operand", insCtx, name)
				continue
			}
		case wasmir.InstrV128Bitselect:
			if !ensureCurrentFrameOperands(3, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: v128.bitselect needs 3 operands", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-2, wasmir.ValueTypeV128) || !stackValueHasType(len(stack)-3, wasmir.ValueTypeV128) {
				diags.Addf("%s: v128.bitselect expects v128 operands", insCtx)
				continue
			}
			truncateStack(len(stack) - 3)
			appendStackType(wasmir.ValueTypeV128)
		case wasmir.InstrI32ReinterpretF32:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i32.reinterpret_f32 needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF32) {
				diags.Addf("%s: i32.reinterpret_f32 expects f32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrI64ReinterpretF64:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: i64.reinterpret_f64 needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeF64) {
				diags.Addf("%s: i64.reinterpret_f64 expects f64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI64))
		case wasmir.InstrF32ReinterpretI32:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f32.reinterpret_i32 needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI32) {
				diags.Addf("%s: f32.reinterpret_i32 expects i32 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeF32))
		case wasmir.InstrF64ReinterpretI64:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: f64.reinterpret_i64 needs 1 operand", insCtx)
				continue
			}
			if !stackValueHasType(len(stack)-1, wasmir.ValueTypeI64) {
				diags.Addf("%s: f64.reinterpret_i64 expects i64 operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeF64))
		case wasmir.InstrRefNull:
			appendStackValue(validatedValue{Type: ins.RefType})
		case wasmir.InstrRefFunc:
			if ins.FuncIndex >= totalFuncCount {
				diags.Addf("%s: ref.func function index %d out of range", insCtx, ins.FuncIndex)
				continue
			}
			if !declaredFuncs[ins.FuncIndex] {
				diags.Addf("%s: undeclared function reference", insCtx)
				continue
			}
			typeIdx, ok := functionTypeIndexAtIndex(m, funcImportTypeIdx, ins.FuncIndex)
			if !ok {
				diags.Addf("%s: ref.func function index %d has invalid type", insCtx, ins.FuncIndex)
				continue
			}
			appendStackValue(validatedValue{Type: wasmir.RefTypeIndexed(typeIdx, false)})
		case wasmir.InstrRefIsNull:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: ref.is_null needs 1 operand", insCtx)
				continue
			}
			top := stackValue(len(stack) - 1)
			if !top.Unknown && !isRefValueType(top.Type) {
				diags.Addf("%s: ref.is_null expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(wasmir.ValueTypeI32))
		case wasmir.InstrRefAsNonNull:
			if !ensureCurrentFrameOperands(1, int(hint.ExplicitInstrArgs), int(hint.BottomInstrArgs)) {
				diags.Addf("%s: ref.as_non_null needs 1 operand", insCtx)
				continue
			}
			top := stackValue(len(stack) - 1)
			if !top.Unknown && !isRefValueType(top.Type) {
				diags.Addf("%s: ref.as_non_null expects reference operand", insCtx)
				continue
			}
			setStackValue(len(stack)-1, refinedNonNullValue(top))

		case wasmir.InstrEnd:
			if len(controlStack) > 0 {
				frame := controlStack[len(controlStack)-1]
				controlStack = controlStack[:len(controlStack)-1]
				switch frame.kind {
				case controlKindIf:
					validateFrameResult(insCtx, frame, "if-branch")
					if len(frame.resultTypes) > 0 && !frame.sawElse &&
						!equalValueTypeSlices(frame.paramTypes, frame.resultTypes) &&
						!frame.enteredUnreachable {
						diags.Addf("%s: if with result requires else branch", insCtx)
					}
				case controlKindBlock:
					validateFrameResult(insCtx, frame, "block")
				case controlKindLoop:
					validateFrameResult(insCtx, frame, "loop")
				case controlKindTryTable:
					validateFrameResult(insCtx, frame, "try_table")
				}

				localInitialized = append(localInitialized[:0], frame.localInit...)
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

func functionContext(m *wasmir.Module, funcIdx uint32, funcImportCount uint32) string {
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

func importedFunctionTypeIndices(m *wasmir.Module) []uint32 {
	out := make([]uint32, 0, len(m.Imports))
	for _, imp := range m.Imports {
		if imp.Kind == wasmir.ExternalKindFunction {
			out = append(out, imp.TypeIdx)
		}
	}
	return out
}

func importedTagTypeIndices(m *wasmir.Module) []uint32 {
	out := make([]uint32, 0, len(m.Imports))
	for _, imp := range m.Imports {
		if imp.Kind == wasmir.ExternalKindTag {
			out = append(out, imp.TypeIdx)
		}
	}
	return out
}

func moduleFunctionCount(m *wasmir.Module) uint32 {
	var count uint32 = uint32(len(m.Funcs))
	for _, imp := range m.Imports {
		if imp.Kind == wasmir.ExternalKindFunction {
			count++
		}
	}
	return count
}

// declaredFunctionRefs returns the function indices declared for ref.func in
// function bodies. We derive this from module-level declaration sites only:
// function exports, global initializers, and element segments.
func declaredFunctionRefs(m *wasmir.Module) map[uint32]bool {
	declared := make(map[uint32]bool)
	for _, exp := range m.Exports {
		if exp.Kind == wasmir.ExternalKindFunction {
			declared[exp.Index] = true
		}
	}
	for _, g := range m.Globals {
		for _, ins := range g.Init {
			if ins.Kind == wasmir.InstrRefFunc {
				declared[ins.FuncIndex] = true
			}
		}
	}
	for _, elem := range m.Elements {
		for _, idx := range elem.FuncIndices {
			declared[idx] = true
		}
		for _, expr := range elem.Exprs {
			for _, ins := range expr {
				if ins.Kind == wasmir.InstrRefFunc {
					declared[ins.FuncIndex] = true
				}
			}
		}
	}
	return declared
}

func functionTypeAtIndex(m *wasmir.Module, funcImportTypeIdx []uint32, funcIdx uint32) (wasmir.TypeDef, *wasmir.Function, bool) {
	importCount := uint32(len(funcImportTypeIdx))
	if funcIdx < importCount {
		typeIdx := funcImportTypeIdx[funcIdx]
		if int(typeIdx) >= len(m.Types) {
			return wasmir.TypeDef{}, nil, false
		}
		if m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
			return wasmir.TypeDef{}, nil, false
		}
		return m.Types[typeIdx], nil, true
	}
	defIdx := funcIdx - importCount
	if int(defIdx) >= len(m.Funcs) {
		return wasmir.TypeDef{}, nil, false
	}
	def := &m.Funcs[defIdx]
	if int(def.TypeIdx) >= len(m.Types) {
		return wasmir.TypeDef{}, nil, false
	}
	if m.Types[def.TypeIdx].Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, nil, false
	}
	return m.Types[def.TypeIdx], def, true
}

// tagTypeAtIndex resolves an absolute tag index to its referenced function
// type, accounting for both imported and module-defined tags.
func tagTypeAtIndex(m *wasmir.Module, tagImportTypeIdx []uint32, tagIdx uint32) (wasmir.TypeDef, bool) {
	importCount := uint32(len(tagImportTypeIdx))
	if tagIdx < importCount {
		typeIdx := tagImportTypeIdx[tagIdx]
		if int(typeIdx) >= len(m.Types) {
			return wasmir.TypeDef{}, false
		}
		if m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
			return wasmir.TypeDef{}, false
		}
		return m.Types[typeIdx], true
	}
	defIdx := tagIdx - importCount
	if int(defIdx) >= len(m.Tags) {
		return wasmir.TypeDef{}, false
	}
	typeIdx := m.Tags[defIdx].TypeIdx
	if int(typeIdx) >= len(m.Types) {
		return wasmir.TypeDef{}, false
	}
	if m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, false
	}
	return m.Types[typeIdx], true
}

func functionTypeIndexAtIndex(m *wasmir.Module, funcImportTypeIdx []uint32, funcIdx uint32) (uint32, bool) {
	importCount := uint32(len(funcImportTypeIdx))
	if funcIdx < importCount {
		typeIdx := funcImportTypeIdx[funcIdx]
		if int(typeIdx) >= len(m.Types) {
			return 0, false
		}
		if m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
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
	if m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
		return 0, false
	}
	return typeIdx, true
}

func functionExportName(m *wasmir.Module, funcIdx uint32) (string, bool) {
	for _, exp := range m.Exports {
		if exp.Kind == wasmir.ExternalKindFunction && exp.Index == funcIdx {
			return exp.Name, true
		}
	}
	return "", false
}

func functionLocationContext(f wasmir.Function) string {
	if f.SourceLoc == "" {
		return ""
	}
	return "at " + f.SourceLoc + ": "
}

func operandLabel(callee wasmir.Function, operandIndex int) string {
	if operandIndex < len(callee.ParamNames) && callee.ParamNames[operandIndex] != "" {
		return fmt.Sprintf("%d (%s)", operandIndex, callee.ParamNames[operandIndex])
	}
	return fmt.Sprintf("%d", operandIndex)
}

func operandLabelFromDef(callee *wasmir.Function, operandIndex int) string {
	if callee == nil {
		return fmt.Sprintf("%d", operandIndex)
	}
	return operandLabel(*callee, operandIndex)
}

func valueTypeName(vt wasmir.ValueType) string {
	return vt.String()
}

func globalInitType(m *wasmir.Module, init []wasmir.Instruction) (wasmir.ValueType, bool) {
	stack := make([]wasmir.ValueType, 0, len(init))
	push := func(vt wasmir.ValueType) {
		stack = append(stack, vt)
	}
	pop := func() (wasmir.ValueType, bool) {
		if len(stack) == 0 {
			return wasmir.ValueType{}, false
		}
		vt := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return vt, true
	}

	for _, ins := range init {
		switch ins.Kind {
		case wasmir.InstrI32Const:
			push(wasmir.ValueTypeI32)
		case wasmir.InstrI64Const:
			push(wasmir.ValueTypeI64)
		case wasmir.InstrF32Const:
			push(wasmir.ValueTypeF32)
		case wasmir.InstrF64Const:
			push(wasmir.ValueTypeF64)
		case wasmir.InstrV128Const:
			push(wasmir.ValueTypeV128)
		case wasmir.InstrRefNull:
			push(ins.RefType)
		case wasmir.InstrRefFunc:
			if ins.FuncIndex >= moduleFunctionCount(m) {
				return wasmir.ValueType{}, false
			}
			typeIdx, ok := functionTypeIndexAtIndex(m, importedFunctionTypeIndices(m), ins.FuncIndex)
			if !ok {
				return wasmir.ValueType{}, false
			}
			push(wasmir.RefTypeIndexed(typeIdx, false))
		case wasmir.InstrGlobalGet:
			if int(ins.GlobalIndex) >= len(m.Globals) {
				return wasmir.ValueType{}, false
			}
			g := m.Globals[ins.GlobalIndex]
			if g.Mutable {
				return wasmir.ValueType{}, false
			}
			push(g.Type)
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul:
			right, ok := pop()
			if !ok || right != wasmir.ValueTypeI32 {
				return wasmir.ValueType{}, false
			}
			left, ok := pop()
			if !ok || left != wasmir.ValueTypeI32 {
				return wasmir.ValueType{}, false
			}
			push(wasmir.ValueTypeI32)
		case wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul:
			right, ok := pop()
			if !ok || right != wasmir.ValueTypeI64 {
				return wasmir.ValueType{}, false
			}
			left, ok := pop()
			if !ok || left != wasmir.ValueTypeI64 {
				return wasmir.ValueType{}, false
			}
			push(wasmir.ValueTypeI64)
		case wasmir.InstrArrayNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != wasmir.TypeDefKindArray {
				return wasmir.ValueType{}, false
			}
			lenType, ok := pop()
			if !ok || lenType != wasmir.ValueTypeI32 {
				return wasmir.ValueType{}, false
			}
			elemType, ok := pop()
			if !ok || !matchesGCExpectedValue(m, elemType, fieldValueType(td.ElemField)) {
				return wasmir.ValueType{}, false
			}
			push(wasmir.RefTypeIndexed(ins.TypeIndex, false))
		case wasmir.InstrStructNew:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != wasmir.TypeDefKindStruct {
				return wasmir.ValueType{}, false
			}
			if len(stack) < len(td.Fields) {
				return wasmir.ValueType{}, false
			}
			base := len(stack) - len(td.Fields)
			for j, field := range td.Fields {
				if !matchesExpectedValueInModule(m, validatedValueFromType(stack[base+j]), validatedValueFromType(fieldValueType(field))) {
					return wasmir.ValueType{}, false
				}
			}
			stack = stack[:base]
			push(wasmir.RefTypeIndexed(ins.TypeIndex, false))
		case wasmir.InstrStructNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != wasmir.TypeDefKindStruct {
				return wasmir.ValueType{}, false
			}
			for _, field := range td.Fields {
				if !isDefaultableFieldType(field) {
					return wasmir.ValueType{}, false
				}
			}
			push(wasmir.RefTypeIndexed(ins.TypeIndex, false))
		case wasmir.InstrArrayNewDefault:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != wasmir.TypeDefKindArray {
				return wasmir.ValueType{}, false
			}
			if !isDefaultableValueType(fieldValueType(td.ElemField)) {
				return wasmir.ValueType{}, false
			}
			lenType, ok := pop()
			if !ok || lenType != wasmir.ValueTypeI32 {
				return wasmir.ValueType{}, false
			}
			push(wasmir.RefTypeIndexed(ins.TypeIndex, false))
		case wasmir.InstrArrayNewFixed:
			td, ok := typeDefAtIndex(m, ins.TypeIndex)
			if !ok || td.Kind != wasmir.TypeDefKindArray {
				return wasmir.ValueType{}, false
			}
			if len(stack) < int(ins.FixedCount) {
				return wasmir.ValueType{}, false
			}
			base := len(stack) - int(ins.FixedCount)
			elemType := fieldValueType(td.ElemField)
			for j := 0; j < int(ins.FixedCount); j++ {
				if !matchesGCExpectedValue(m, stack[base+j], elemType) {
					return wasmir.ValueType{}, false
				}
			}
			stack = stack[:base]
			push(wasmir.RefTypeIndexed(ins.TypeIndex, false))
		case wasmir.InstrRefI31:
			valueType, ok := pop()
			if !ok || valueType != wasmir.ValueTypeI32 {
				return wasmir.ValueType{}, false
			}
			push(wasmir.RefTypeI31(false))
		case wasmir.InstrExternConvertAny:
			valueType, ok := pop()
			if !ok || !matchesGCExpectedValue(m, valueType, wasmir.RefTypeAny(true)) {
				return wasmir.ValueType{}, false
			}
			push(wasmir.RefTypeExtern(valueType.Nullable))
		case wasmir.InstrAnyConvertExtern:
			valueType, ok := pop()
			if !ok || !matchesGCExpectedValue(m, valueType, wasmir.RefTypeExtern(true)) {
				return wasmir.ValueType{}, false
			}
			push(wasmir.RefTypeAny(valueType.Nullable))
		default:
			return wasmir.ValueType{}, false
		}
	}

	if len(stack) != 1 {
		return wasmir.ValueType{}, false
	}
	return stack[0], true
}

func instrName(kind wasmir.InstrKind) string {
	if def, ok := instrdef.LookupInstructionByKind(kind); ok {
		return def.TextName
	}
	return fmt.Sprintf("instruction(%d)", kind)
}
