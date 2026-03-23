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
	if want.Type.UsesTypeIndex() {
		if !got.Type.UsesTypeIndex() || got.Type.HeapType.TypeIndex != want.Type.HeapType.TypeIndex {
			return false
		}
	} else {
		switch want.Type.HeapType.Kind {
		case HeapKindFunc:
			if got.Type.HeapType.Kind != HeapKindFunc && got.Type.HeapType.Kind != HeapKindTypeIndex {
				return false
			}
		case HeapKindExtern:
			if got.Type.HeapType.Kind != HeapKindExtern {
				return false
			}
		default:
			return false
		}
	}
	if got.Type.IsRef() && got.Type.Nullable && !want.Type.Nullable {
		return false
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
	case ValueKindI32, ValueKindI64, ValueKindF32, ValueKindF64:
		return true
	default:
		return false
	}
}

func naturalMemoryAlignExponent(kind InstrKind) (uint32, bool) {
	switch kind {
	case InstrI32Load8S, InstrI32Load8U, InstrI64Load8S, InstrI64Load8U, InstrI32Store8, InstrI64Store8:
		return 0, true
	case InstrI32Load16S, InstrI32Load16U, InstrI64Load16S, InstrI64Load16U, InstrI32Store16, InstrI64Store16:
		return 1, true
	case InstrI32Load, InstrF32Load, InstrI64Load32S, InstrI64Load32U, InstrI32Store, InstrI64Store32, InstrF32Store:
		return 2, true
	case InstrI64Load, InstrF64Load, InstrI64Store, InstrF64Store:
		return 3, true
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
		if g.Imported {
			continue
		}
		initType, ok := globalInitType(m, g.Init)
		if !ok {
			diags.Addf("global[%d]: unsupported initializer instruction kind %d", i, g.Init.Kind)
			continue
		}
		if !matchesExpectedValue(validatedValue{Type: initType}, validatedValue{Type: g.Type}) {
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
		if table.HasInit {
			initType, ok := globalInitType(m, table.Init)
			if !ok {
				diags.Addf("table[%d]: invalid initializer", i)
				continue
			}
			if !matchesExpectedValue(validatedValue{Type: initType}, validatedValue{Type: table.RefType}) {
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
			if !matchesExpectedValue(validatedValue{Type: elem.RefType}, validatedValue{Type: tableTy}) {
				diags.Addf("element[%d]: type mismatch", i)
			}
			for j, expr := range elem.Exprs {
				ty, ok := globalInitType(m, expr)
				if !ok {
					diags.Addf("element[%d] expr[%d]: constant expression required", i, j)
					continue
				}
				if !matchesExpectedValue(validatedValue{Type: ty}, validatedValue{Type: elem.RefType}) {
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
			if !matchesExpectedValue(got, want) {
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
			if !matchesExpectedValue(got, want) {
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

	returned := false
instrLoop:
	for i, ins := range f.Body {
		insCtx := fmt.Sprintf("instruction %d", i)
		if ins.SourceLoc != "" {
			insCtx = fmt.Sprintf("%s at %s", insCtx, ins.SourceLoc)
		}

		if len(controlStack) > 0 && controlStack[len(controlStack)-1].unreachable {
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
				if !matchesExpectedValue(got, want) {
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
				if !matchesExpectedValue(got, want) {
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
				if !matchesExpectedValue(got, want) {
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
			if !matchesExpectedValue(stackValue(len(stack)-1), want) {
				diags.Addf("%s: local.set expects %s operand", insCtx, validatedValueName(want))
				continue
			}
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
			if !matchesExpectedValue(stackValue(len(stack)-1), want) {
				diags.Addf("%s: local.tee expects %s operand", insCtx, validatedValueName(want))
				continue
			}
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
			if !matchesExpectedValue(stackValue(len(stack)-1), want) {
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
			if !matchesExpectedValue(stackValue(len(stack)-1), want) {
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
			if !matchesExpectedValue(validatedValueFromType(srcTable.RefType), validatedValueFromType(dstTable.RefType)) {
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
			if !matchesExpectedValue(stackValue(len(stack)-2), want) {
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
			if !ok || !matchesExpectedValue(validatedValueFromType(elemType), validatedValueFromType(m.Tables[ins.TableIndex].RefType)) {
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
			if !matchesExpectedValue(stackValue(len(stack)-2), want) {
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
				if !matchesExpectedValue(stackValue(base+j), want) {
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
				if !matchesExpectedValue(stackValue(base+j), want) {
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
			if !matchesExpectedValue(stackValue(len(stack)-1), calleeRefWant) {
				diags.Addf("%s: call_ref expects operand of type %s", insCtx, validatedValueName(calleeRefWant))
				continue
			}
			base := len(stack) - 1 - len(calleeType.Params)
			ok := true
			for j := range calleeType.Params {
				want := valueAt(calleeType.Params, j)
				if !matchesExpectedValue(stackValue(base+j), want) {
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
				want := validatedValueFromType(field.Type)
				if !matchesExpectedValue(stackValue(base+j), want) {
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
			if !matchesExpectedValue(stackValue(len(stack)-1), wantRef) {
				diags.Addf("%s: struct.get expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			setStackValue(len(stack)-1, validatedValueFromType(td.Fields[ins.FieldIndex].Type))
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
			if !isDefaultableValueType(td.ElemField.Type) {
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
			if !matchesExpectedValue(stackValue(len(stack)-2), wantRef) {
				diags.Addf("%s: array.get expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 2)
			appendStackValue(validatedValueFromType(td.ElemField.Type))
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
			if len(stack) < 3 {
				diags.Addf("%s: array.set needs 3 operands", insCtx)
				continue
			}
			wantValue := validatedValueFromType(td.ElemField.Type)
			if !matchesExpectedValue(stackValue(len(stack)-1), wantValue) {
				diags.Addf("%s: array.set expects value operand of type %s", insCtx, validatedValueName(wantValue))
				continue
			}
			if stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: array.set expects i32 index operand", insCtx)
				continue
			}
			wantRef := validatedValueFromType(RefTypeIndexed(ins.TypeIndex, true))
			if !matchesExpectedValue(stackValue(len(stack)-3), wantRef) {
				diags.Addf("%s: array.set expects operand of type %s", insCtx, validatedValueName(wantRef))
				continue
			}
			truncateStack(len(stack) - 3)

		case InstrI32Const:
			appendStackType(ValueTypeI32)

		case InstrI64Const:
			appendStackType(ValueTypeI64)

		case InstrF32Const:
			appendStackType(ValueTypeF32)

		case InstrF64Const:
			appendStackType(ValueTypeF64)

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
				if !matchesExpectedValue(got, want) {
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
				if !matchesExpectedValue(got, want) {
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
			if !matchesExpectedValue(gotRef, wantRef) {
				diags.Addf("%s: br_on_non_null depth %d target type mismatch at %d: got %s want %s", insCtx, ins.BranchDepth, len(targetValues)-1, validatedValueName(gotRef), validatedValueName(wantRef))
				continue
			}
			for i := 0; i < len(targetValues)-1; i++ {
				setStackValue(base+i, targetValues[i])
			}
			truncateStack(len(stack) - 1)
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
						if !matchesExpectedValue(got, want) {
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
				if !matchesExpectedValue(stackValue(base+j), want) {
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
		if !matchesExpectedValue(got, want) {
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

func globalInitType(m *Module, init Instruction) (ValueType, bool) {
	switch init.Kind {
	case InstrI32Const:
		return ValueTypeI32, true
	case InstrI64Const:
		return ValueTypeI64, true
	case InstrF32Const:
		return ValueTypeF32, true
	case InstrF64Const:
		return ValueTypeF64, true
	case InstrRefNull:
		return init.RefType, true
	case InstrRefFunc:
		if init.FuncIndex >= moduleFunctionCount(m) {
			return ValueType{}, false
		}
		typeIdx, ok := functionTypeIndexAtIndex(m, importedFunctionTypeIndices(m), init.FuncIndex)
		if !ok {
			return ValueType{}, false
		}
		return RefTypeIndexed(typeIdx, false), true
	case InstrGlobalGet:
		if int(init.GlobalIndex) >= len(m.Globals) {
			return ValueType{}, false
		}
		g := m.Globals[init.GlobalIndex]
		if g.Mutable {
			return ValueType{}, false
		}
		return g.Type, true
	default:
		return ValueType{}, false
	}
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
	case InstrStructGet:
		return "struct.get"
	case InstrArrayNewDefault:
		return "array.new_default"
	case InstrArrayGet:
		return "array.get"
	case InstrArraySet:
		return "array.set"
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
