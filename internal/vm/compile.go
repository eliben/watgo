// Package vm contains the private execution representation used by wasmvm.
package vm

import (
	"fmt"
	"slices"

	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

// Function is the VM's execution form for a module-defined function.
//
// It is intentionally separate from wasmir.Function: wasmir is the semantic
// interchange representation, while Function stores runtime-oriented immediates
// such as resolved branch targets.
type Function struct {
	Locals []wasmir.ValueType
	Code   []Instr
}

// Instr is one instruction in the VM's execution form.
type Instr struct {
	// Kind is the semantic instruction kind executed by the interpreter.
	Kind wasmir.InstrKind

	// Target is the resolved program counter for control-flow instructions.
	// It is used by if, else, br, and br_if; other instructions leave it at -1.
	Target int

	// Index is the resolved index immediate for local and function instructions.
	Index uint32

	// I32 is the immediate payload for i32.const.
	I32 int32

	// I64 is the immediate payload for i64.const.
	I64 int64

	// F32 is the raw IEEE-754 bit pattern immediate for f32.const.
	F32 uint32

	// F64 is the raw IEEE-754 bit pattern immediate for f64.const.
	F64 uint64
}

// CompileFunction compiles a semantic function body into the VM's execution
// form. It supports exactly the instruction subset currently implemented by
// wasmvm.
func CompileFunction(fn *wasmir.Function) (*Function, error) {
	ctrl, err := analyzeControl(fn.Body)
	if err != nil {
		return nil, err
	}

	out := &Function{
		Locals: slices.Clone(fn.Locals),
		Code:   make([]Instr, len(fn.Body)),
	}
	labelStack := make([]int, 0)

	for pc, ins := range fn.Body {
		op := Instr{Kind: ins.Kind, Target: -1}
		switch ins.Kind {
		case wasmir.InstrBlock:
			if _, ok := ctrl.labels[pc]; !ok {
				return nil, fmt.Errorf("block at %d has no matching end", pc)
			}
			labelStack = append(labelStack, pc)
		case wasmir.InstrIf:
			label, ok := ctrl.labels[pc]
			if !ok {
				return nil, fmt.Errorf("if at %d has no matching end", pc)
			}
			if label.elseIndex >= 0 {
				op.Target = label.elseIndex
			} else {
				op.Target = label.endIndex
			}
			labelStack = append(labelStack, pc)
		case wasmir.InstrElse:
			if len(labelStack) == 0 {
				return nil, fmt.Errorf("else at %d without active label", pc)
			}
			start := labelStack[len(labelStack)-1]
			label := ctrl.labels[start]
			if label.elseIndex != pc {
				return nil, fmt.Errorf("else at %d does not match active label", pc)
			}
			op.Target = label.endIndex
		case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
			op.Index = ins.LocalIndex
		case wasmir.InstrCall:
			op.Index = ins.FuncIndex
		case wasmir.InstrBr, wasmir.InstrBrIf:
			target, err := compileBranchTarget(ins.BranchDepth, labelStack, ctrl)
			if err != nil {
				return nil, fmt.Errorf("%s at %d: %w", InstrName(ins.Kind), pc, err)
			}
			op.Target = target
		case wasmir.InstrI32Const:
			op.I32 = ins.I32Const
		case wasmir.InstrI64Const:
			op.I64 = ins.I64Const
		case wasmir.InstrF32Const:
			op.F32 = ins.F32Const
		case wasmir.InstrF64Const:
			op.F64 = ins.F64Const
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul,
			wasmir.InstrI32Eq, wasmir.InstrI32Ne,
			wasmir.InstrI32LtS, wasmir.InstrI32LeS, wasmir.InstrI32GtS, wasmir.InstrI32GeS,
			wasmir.InstrI32Eqz,
			wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul,
			wasmir.InstrI64Eq, wasmir.InstrI64Ne,
			wasmir.InstrI64LtS, wasmir.InstrI64LeS, wasmir.InstrI64GtS, wasmir.InstrI64GeS,
			wasmir.InstrI64Eqz,
			wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div,
			wasmir.InstrF32Eq, wasmir.InstrF32Ne,
			wasmir.InstrF32Lt, wasmir.InstrF32Le, wasmir.InstrF32Gt, wasmir.InstrF32Ge,
			wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div,
			wasmir.InstrF64Eq, wasmir.InstrF64Ne,
			wasmir.InstrF64Lt, wasmir.InstrF64Le, wasmir.InstrF64Gt, wasmir.InstrF64Ge,
			wasmir.InstrDrop, wasmir.InstrReturn:
		case wasmir.InstrEnd:
			if len(labelStack) == 0 {
				if pc != len(fn.Body)-1 {
					return nil, fmt.Errorf("end without active label")
				}
			} else {
				start := labelStack[len(labelStack)-1]
				label := ctrl.labels[start]
				if label.endIndex == pc {
					labelStack = labelStack[:len(labelStack)-1]
				}
			}
		default:
			return nil, fmt.Errorf("unsupported instruction %s", InstrName(ins.Kind))
		}
		out.Code[pc] = op
	}

	if len(labelStack) != 0 {
		start := labelStack[len(labelStack)-1]
		return nil, fmt.Errorf("%s at %d without matching end", InstrName(fn.Body[start].Kind), start)
	}
	return out, nil
}

func compileBranchTarget(depth uint32, labelStack []int, ctrl controlInfo) (int, error) {
	if int(depth) >= len(labelStack) {
		return 0, fmt.Errorf("branch depth %d out of range", depth)
	}
	start := labelStack[len(labelStack)-1-int(depth)]
	label, ok := ctrl.labels[start]
	if !ok {
		return 0, fmt.Errorf("branch target at %d has no matching end", start)
	}
	return label.endIndex, nil
}

// controlLabel describes one structured-control label in the flattened
// instruction stream.
//
// endIndex is the matching end instruction and is currently the branch target
// for both block and if labels. elseIndex is the matching else instruction for
// if labels, or -1 when the label has no else arm.
type controlLabel struct {
	endIndex  int
	elseIndex int
}

// controlInfo stores precomputed control-boundary metadata by opening
// instruction index. Only block and if instructions have entries.
type controlInfo struct {
	labels map[int]controlLabel
}

// analyzeControl records matching structured-control boundaries in body.
//
// The VM assumes modules were validated before instantiation, but it still
// receives a plain wasmir.Module and should not rely on nested source syntax.
// This pass treats block/if as openers, else as metadata on the current if, and
// end as the closer for the innermost opener. End instructions with no opener
// are accepted here because the final function end is represented the same way
// as a structured-control end.
func analyzeControl(body []wasmir.Instruction) (controlInfo, error) {
	ctrl := controlInfo{labels: make(map[int]controlLabel)}
	stack := make([]int, 0)
	elseIndex := make(map[int]int)

	for pc, ins := range body {
		switch ins.Kind {
		case wasmir.InstrBlock, wasmir.InstrIf:
			stack = append(stack, pc)
		case wasmir.InstrElse:
			if len(stack) == 0 {
				return controlInfo{}, fmt.Errorf("else at %d without matching if", pc)
			}
			start := stack[len(stack)-1]
			if body[start].Kind != wasmir.InstrIf {
				return controlInfo{}, fmt.Errorf("else at %d matched non-if instruction %s", pc, InstrName(body[start].Kind))
			}
			elseIndex[start] = pc
		case wasmir.InstrEnd:
			if len(stack) == 0 {
				continue
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			label := controlLabel{
				endIndex:  pc,
				elseIndex: -1,
			}
			if elsePC, ok := elseIndex[start]; ok {
				label.elseIndex = elsePC
			}
			ctrl.labels[start] = label
		}
	}
	if len(stack) != 0 {
		return controlInfo{}, fmt.Errorf("%s at %d without matching end", InstrName(body[stack[len(stack)-1]].Kind), stack[len(stack)-1])
	}
	return ctrl, nil
}

// InstrName formats instruction kinds for VM errors.
func InstrName(kind wasmir.InstrKind) string {
	if def, ok := instrdef.LookupInstructionByKind(kind); ok {
		return def.TextName
	}
	return fmt.Sprintf("instruction(%d)", kind)
}
