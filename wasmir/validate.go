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
	type ifFrame struct {
		entryHeight int
		hasResult   bool
		resultType  ValueType
		sawElse     bool
	}
	var ifStack []ifFrame

	for i, ins := range f.Body {
		insCtx := fmt.Sprintf("instruction %d", i)
		if ins.SourceLoc != "" {
			insCtx = fmt.Sprintf("%s at %s", insCtx, ins.SourceLoc)
		}
		switch ins.Kind {
		case InstrIf:
			if len(stack) < 1 {
				diags.Addf("%s: if needs 1 i32 condition operand", insCtx)
				continue
			}
			if stack[len(stack)-1] != ValueTypeI32 {
				diags.Addf("%s: if expects i32 condition operand", insCtx)
				continue
			}
			stack = stack[:len(stack)-1]
			ifStack = append(ifStack, ifFrame{
				entryHeight: len(stack),
				hasResult:   ins.BlockHasResult,
				resultType:  ins.BlockType,
			})
		case InstrElse:
			if len(ifStack) == 0 {
				diags.Addf("%s: else without matching if", insCtx)
				continue
			}
			frame := ifStack[len(ifStack)-1]
			if frame.sawElse {
				diags.Addf("%s: duplicate else for if", insCtx)
				continue
			}
			wantHeight := frame.entryHeight
			if frame.hasResult {
				wantHeight++
			}
			if len(stack) != wantHeight {
				diags.Addf("%s: then-branch stack height mismatch: got %d want %d", insCtx, len(stack), wantHeight)
			} else if frame.hasResult && stack[frame.entryHeight] != frame.resultType {
				diags.Addf("%s: then-branch result type mismatch: got %s want %s", insCtx, valueTypeName(stack[frame.entryHeight]), valueTypeName(frame.resultType))
			}
			stack = stack[:frame.entryHeight]
			frame.sawElse = true
			ifStack[len(ifStack)-1] = frame
		case InstrLocalGet:
			if int(ins.LocalIndex) >= len(locals) {
				diags.Addf("%s: local index %d out of range", insCtx, ins.LocalIndex)
				continue
			}
			stack = append(stack, locals[ins.LocalIndex])
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

		case InstrI32Add, InstrI32Sub, InstrI32Mul, InstrI32DivS, InstrI32DivU:
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

		case InstrI64Add, InstrI64Sub, InstrI64Mul, InstrI64DivS, InstrI64DivU:
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

		case InstrF32Sqrt, InstrF32Ceil, InstrF32Floor, InstrF32Trunc, InstrF32Nearest:
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

		case InstrF64Sqrt, InstrF64Ceil, InstrF64Floor, InstrF64Trunc, InstrF64Nearest:
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
			if len(ifStack) > 0 {
				frame := ifStack[len(ifStack)-1]
				ifStack = ifStack[:len(ifStack)-1]

				wantHeight := frame.entryHeight
				if frame.hasResult {
					wantHeight++
				}
				if len(stack) != wantHeight {
					diags.Addf("%s: if-branch stack height mismatch: got %d want %d", insCtx, len(stack), wantHeight)
				} else if frame.hasResult && stack[frame.entryHeight] != frame.resultType {
					diags.Addf("%s: if-branch result type mismatch: got %s want %s", insCtx, valueTypeName(stack[frame.entryHeight]), valueTypeName(frame.resultType))
				}
				if frame.hasResult && !frame.sawElse {
					diags.Addf("%s: if with result requires else branch", insCtx)
				}

				stack = stack[:frame.entryHeight]
				if frame.hasResult {
					stack = append(stack, frame.resultType)
				}
				continue
			}
			if i != len(f.Body)-1 {
				diags.Addf("%s: end must be last", insCtx)
			}

		default:
			diags.Addf("%s: unsupported instruction kind %d", insCtx, ins.Kind)
		}
	}
	if len(ifStack) > 0 {
		diags.Addf("%sunterminated if: missing end", funcLocCtx)
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
	case InstrCall:
		return "call"
	case InstrIf:
		return "if"
	case InstrElse:
		return "else"
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
	case InstrI64Add:
		return "i64.add"
	case InstrI64Eqz:
		return "i64.eqz"
	case InstrI64Sub:
		return "i64.sub"
	case InstrI64Mul:
		return "i64.mul"
	case InstrI64DivS:
		return "i64.div_s"
	case InstrI64DivU:
		return "i64.div_u"
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
