package wasmir

import (
	"fmt"

	"github.com/eliben/watgo/diag"
)

// ValidateModule validates m.
// Validation includes module-level checks (type/export indices) and function
// body type checks for the currently supported instruction subset.
// It returns nil on success. On any failure, it returns diag.ErrorList.
func ValidateModule(m *Module) error {
	if m == nil {
		return diag.Fromf("module is nil")
	}

	var diags diag.ErrorList
	for i, f := range m.Funcs {
		fnCtx := functionContext(m, i)
		if int(f.TypeIdx) >= len(m.Types) {
			diags.Addf("%s has invalid type index %d", fnCtx, f.TypeIdx)
			continue
		}
		funcErrs := validateFunctionBody(m, m.Types[f.TypeIdx], f)
		for _, err := range funcErrs {
			diags.Addf("%s: %v", fnCtx, err)
		}
	}

	for i, exp := range m.Exports {
		if exp.Kind != ExternalKindFunction {
			diags.Addf("export[%d] has unsupported kind %d", i, exp.Kind)
			continue
		}
		if int(exp.Index) >= len(m.Funcs) {
			diags.Addf("export[%d] index %d out of range", i, exp.Index)
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
func validateFunctionBody(m *Module, ft FuncType, f Function) diag.ErrorList {
	var diags diag.ErrorList
	funcLocCtx := functionLocationContext(f)

	if len(f.Body) == 0 {
		diags.Addf("%sempty function body", funcLocCtx)
		return diags
	}
	if f.Body[len(f.Body)-1].Kind != InstrEnd {
		diags.Addf("%sfunction body must terminate with end", funcLocCtx)
		return diags
	}

	locals := make([]ValueType, 0, len(ft.Params)+len(f.Locals))
	locals = append(locals, ft.Params...)
	locals = append(locals, f.Locals...)

	stack := make([]ValueType, 0)
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
	}
	var controlStack []controlFrame

	validateFrameResult := func(insCtx string, frame controlFrame, context string) {
		wantHeight := frame.entryHeight + len(frame.resultTypes)
		if len(stack) != wantHeight {
			diags.Addf("%s: %s stack height mismatch: got %d want %d", insCtx, context, len(stack), wantHeight)
			return
		}
		for i, rt := range frame.resultTypes {
			if stack[frame.entryHeight+i] != rt {
				diags.Addf("%s: %s result type mismatch at %d: got %s want %s", insCtx, context, i, valueTypeName(stack[frame.entryHeight+i]), valueTypeName(rt))
				return
			}
		}
	}

	validateBranchTarget := func(insCtx string, depth uint32, opName string) (controlFrame, bool) {
		if int(depth) >= len(controlStack) {
			diags.Addf("%s: %s depth %d out of range", insCtx, opName, depth)
			return controlFrame{}, false
		}
		target := controlStack[len(controlStack)-1-int(depth)]
		targetTypes := target.resultTypes
		if target.kind == controlKindLoop {
			targetTypes = target.paramTypes
		}
		wantHeight := target.entryHeight + len(targetTypes)
		if len(stack) < wantHeight {
			diags.Addf("%s: %s depth %d has insufficient stack height", insCtx, opName, depth)
			return controlFrame{}, false
		}
		for i, tt := range targetTypes {
			if stack[target.entryHeight+i] != tt {
				diags.Addf("%s: %s depth %d target type mismatch at %d: got %s want %s", insCtx, opName, depth, i, valueTypeName(stack[target.entryHeight+i]), valueTypeName(tt))
				return controlFrame{}, false
			}
		}
		return target, true
	}

	applyBranchTarget := func(target controlFrame) {
		targetLen := len(target.resultTypes)
		if target.kind == controlKindLoop {
			targetLen = len(target.paramTypes)
		}
		stack = stack[:target.entryHeight+targetLen]
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
		switch ins.Kind {
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
			for j, pt := range params {
				if stack[base+j] != pt {
					diags.Addf("%s: block parameter %d expects %s", insCtx, j, valueTypeName(pt))
					matched = false
					break
				}
			}
			if !matched {
				continue
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
			for j, pt := range params {
				if stack[base+j] != pt {
					diags.Addf("%s: loop parameter %d expects %s", insCtx, j, valueTypeName(pt))
					matched = false
					break
				}
			}
			if !matched {
				continue
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
			stack = stack[:len(stack)-1] // pop condition
			if len(stack) < len(params) {
				diags.Addf("%s: if needs %d parameter operands", insCtx, len(params))
				continue
			}
			base := len(stack) - len(params)
			matched := true
			for j, pt := range params {
				if stack[base+j] != pt {
					diags.Addf("%s: if parameter %d expects %s", insCtx, j, valueTypeName(pt))
					matched = false
					break
				}
			}
			if !matched {
				continue
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
			validateFrameResult(insCtx, frame, "then-branch")
			stack = stack[:frame.entryHeight+len(frame.paramTypes)]
			frame.sawElse = true
			controlStack[len(controlStack)-1] = frame
		case InstrLocalGet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			stack = append(stack, locals[ins.LocalIndex])
		case InstrLocalSet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			if len(stack) < 1 {
				diags.Addf("%s: local.set needs 1 operand", insCtx)
				continue
			}
			want := locals[ins.LocalIndex]
			if stack[len(stack)-1] != want {
				diags.Addf("%s: local.set expects %s operand", insCtx, valueTypeName(want))
				continue
			}
			stack = stack[:len(stack)-1]
		case InstrCall:
			if int(ins.FuncIndex) >= len(m.Funcs) {
				diags.Addf("%s: call function index %d out of range", insCtx, ins.FuncIndex)
				continue
			}
			callee := m.Funcs[ins.FuncIndex]
			calleeCtx := functionContext(m, int(ins.FuncIndex))
			if int(callee.TypeIdx) >= len(m.Types) {
				diags.Addf("%s: call target %s has invalid type index %d", insCtx, calleeCtx, callee.TypeIdx)
				continue
			}
			calleeType := m.Types[callee.TypeIdx]
			if len(stack) < len(calleeType.Params) {
				diags.Addf("%s: call to %s needs %d operands", insCtx, calleeCtx, len(calleeType.Params))
				continue
			}
			base := len(stack) - len(calleeType.Params)
			ok := true
			for j, pt := range calleeType.Params {
				if stack[base+j] != pt {
					diags.Addf("%s: call to %s expects operand %s to be %s", insCtx, calleeCtx, operandLabel(callee, j), valueTypeName(pt))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			stack = stack[:base]
			stack = append(stack, calleeType.Results...)

		case InstrI32Const:
			stack = append(stack, ValueTypeI32)

		case InstrI64Const:
			stack = append(stack, ValueTypeI64)

		case InstrF32Const:
			stack = append(stack, ValueTypeF32)

		case InstrF64Const:
			stack = append(stack, ValueTypeF64)

		case InstrDrop:
			if len(stack) < 1 {
				diags.Addf("%s: drop needs 1 operand", insCtx)
				continue
			}
			stack = stack[:len(stack)-1]
		case InstrBr:
			target, ok := validateBranchTarget(insCtx, ins.BranchDepth, "br")
			if !ok {
				continue
			}
			applyBranchTarget(target)
		case InstrBrIf:
			if len(stack) < 1 {
				diags.Addf("%s: br_if needs 1 i32 condition operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: br_if expects i32 condition operand", insCtx)
				continue
			}
			stack = stack[:len(stack)-1]
			_, _ = validateBranchTarget(insCtx, ins.BranchDepth, "br_if")
		case InstrUnreachable:
			// `unreachable` is stack-polymorphic and marks the current path as
			// non-returning for this simple validator.
			stack = append(stack[:0], ft.Results...)
			returned = true
			break instrLoop
		case InstrReturn:
			if len(stack) < len(ft.Results) {
				diags.Addf("%s: return needs %d operands", insCtx, len(ft.Results))
				continue
			}
			base := len(stack) - len(ft.Results)
			ok := true
			for j, rt := range ft.Results {
				if stack[base+j] != rt {
					diags.Addf("%s: return expects result %d to be %s", insCtx, j, valueTypeName(rt))
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			stack = append(stack[:0], ft.Results...)
			returned = true
			break instrLoop

		case InstrI32Add, InstrI32Sub, InstrI32Mul, InstrI32DivS, InstrI32DivU,
			InstrI32RemS, InstrI32RemU, InstrI32Shl, InstrI32ShrS, InstrI32ShrU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 operands", insCtx, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI32)

		case InstrI32LtS, InstrI32LtU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 || stack[len(stack)-2] != ValueTypeI32 {
				diags.Addf("%s: %s expects i32 operands", insCtx, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI32)
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

		case InstrI64Add, InstrI64Sub, InstrI64Mul, InstrI64DivS, InstrI64DivU,
			InstrI64RemS, InstrI64RemU, InstrI64Shl, InstrI64ShrS, InstrI64ShrU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != ValueTypeI64 {
				diags.Addf("%s: %s expects i64 operands", insCtx, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI64)

		case InstrI64Eq, InstrI64LtS, InstrI64LtU, InstrI64GtS, InstrI64GtU:
			name := instrName(ins.Kind)
			if len(stack) < 2 {
				diags.Addf("%s: %s needs 2 operands", insCtx, name)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != ValueTypeI64 {
				diags.Addf("%s: %s expects i64 operands", insCtx, name)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI32)
		case InstrI64LeU:
			if len(stack) < 2 {
				diags.Addf("%s: i64.le_u needs 2 operands", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 || stack[len(stack)-2] != ValueTypeI64 {
				diags.Addf("%s: i64.le_u expects i64 operands", insCtx)
				continue
			}
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeI32)
		case InstrI64Eqz:
			if len(stack) < 1 {
				diags.Addf("%s: i64.eqz needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: i64.eqz expects i64 operand", insCtx)
				continue
			}
			stack[len(stack)-1] = ValueTypeI32

		case InstrI32WrapI64:
			if len(stack) < 1 {
				diags.Addf("%s: i32.wrap_i64 needs 1 operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI64 {
				diags.Addf("%s: i32.wrap_i64 expects i64 operand", insCtx)
				continue
			}
			stack[len(stack)-1] = ValueTypeI32

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
			stack[len(stack)-1] = ValueTypeI64

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
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeF32)

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
			stack = stack[:len(stack)-2]
			stack = append(stack, ValueTypeF64)

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

		case InstrEnd:
			if len(controlStack) > 0 {
				frame := controlStack[len(controlStack)-1]
				controlStack = controlStack[:len(controlStack)-1]
				switch frame.kind {
				case controlKindIf:
					validateFrameResult(insCtx, frame, "if-branch")
					if len(frame.resultTypes) > 0 && !frame.sawElse {
						diags.Addf("%s: if with result requires else branch", insCtx)
					}
				case controlKindBlock:
					validateFrameResult(insCtx, frame, "block")
				case controlKindLoop:
					validateFrameResult(insCtx, frame, "loop")
				}

				stack = stack[:frame.entryHeight]
				stack = append(stack, frame.resultTypes...)
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
		if stack[i] != ft.Results[i] {
			diags.Addf("%sresult type mismatch at %d: got %s want %s", funcLocCtx, i, valueTypeName(stack[i]), valueTypeName(ft.Results[i]))
		}
	}

	return diags
}

func functionContext(m *Module, funcIdx int) string {
	ctx := fmt.Sprintf("func[%d]", funcIdx)
	if funcIdx < 0 || funcIdx >= len(m.Funcs) {
		return ctx
	}
	if name := m.Funcs[funcIdx].Name; name != "" {
		return ctx + " " + name
	}
	if exportName, ok := functionExportName(m, uint32(funcIdx)); ok {
		return fmt.Sprintf(`%s export %q`, ctx, exportName)
	}
	return ctx
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

func valueTypeName(vt ValueType) string {
	switch vt {
	case ValueTypeI32:
		return "i32"
	case ValueTypeI64:
		return "i64"
	case ValueTypeF32:
		return "f32"
	case ValueTypeF64:
		return "f64"
	default:
		return fmt.Sprintf("value_type(%d)", vt)
	}
}

func instrName(kind InstrKind) string {
	switch kind {
	case InstrLocalGet:
		return "local.get"
	case InstrLocalSet:
		return "local.set"
	case InstrCall:
		return "call"
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
	case InstrI32Add:
		return "i32.add"
	case InstrI32Sub:
		return "i32.sub"
	case InstrI32Mul:
		return "i32.mul"
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
	case InstrI32Eqz:
		return "i32.eqz"
	case InstrI32LtS:
		return "i32.lt_s"
	case InstrI32LtU:
		return "i32.lt_u"
	case InstrI64Add:
		return "i64.add"
	case InstrI64Eq:
		return "i64.eq"
	case InstrI64Eqz:
		return "i64.eqz"
	case InstrI64GtS:
		return "i64.gt_s"
	case InstrI64GtU:
		return "i64.gt_u"
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
	case InstrI64LtS:
		return "i64.lt_s"
	case InstrI64LtU:
		return "i64.lt_u"
	case InstrI32WrapI64:
		return "i32.wrap_i64"
	case InstrI64ExtendI32S:
		return "i64.extend_i32_s"
	case InstrI64ExtendI32U:
		return "i64.extend_i32_u"
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
	case InstrEnd:
		return "end"
	default:
		return "unknown"
	}
}
