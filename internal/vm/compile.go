package vm

import (
	"fmt"
	"slices"

	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

const maxInt64Uint = uint64(1<<63 - 1)

// Function is the VM's execution form for a module-defined function.
type Function struct {
	// locals contains the non-parameter locals declared by the function. At
	// call time ExecuteFunction builds its local array as args followed by
	// these zero-initialized locals.
	locals []wasmir.ValueType

	// code is the linear instruction stream consumed by ExecuteFunction. It has
	// the same instruction order as wasmir.Function.Body, but immediate fields
	// have been normalized for execution.
	code []instr

	// branchTables stores the variable-length target lists used by br_table
	// instructions. A br_table instruction keeps its fixed-size instr small by
	// storing the index of its target list in instr.index.
	//
	// Each target list contains already-resolved program counters, in the same
	// order as the source instruction's BranchTable depths. The default target
	// is appended as the final element, so execution can use selector values
	// below len(table)-1 as direct table indices and use len(table)-1 for the
	// default case.
	branchTables [][]int

	// refTypes stores reference type immediates used by ref.null instructions.
	// A ref.null instruction keeps its fixed-size instr small by storing the
	// index of its type immediate in instr.index.
	refTypes []wasmir.ValueType
}

// instr is one instruction in the VM's execution form.
type instr struct {
	// kind is the semantic instruction kind executed by the interpreter.
	kind wasmir.InstrKind

	// target is the resolved program counter for fixed-target control-flow
	// instructions. It is used by if, else, br, br_if, and loop-branch targets;
	// other instructions leave it at -1. The interpreter assigns pc = target,
	// then its loop increment moves execution to the following instruction.
	target int

	// index is the resolved index immediate for local.get/set/tee,
	// global.get/set, call, memory and table instructions, data.drop/elem.drop,
	// br_table's branchTables entry, and ref.null's refTypes entry.
	index uint32

	// bits is the raw immediate payload for constant instructions.
	//
	// kind determines how to interpret it: i32.const uses int32(bits),
	// i64.const uses bits, f32.const uses uint32(bits), and f64.const uses
	// uint64(bits). For currently supported memory load/store instructions,
	// bits stores the static offset immediate. For memory.copy and memory.init,
	// bits stores the secondary index immediate. Indirect calls use bits for
	// the call type index, and table bulk instructions use it for the source
	// table or element segment index.
	bits int64
}

// CompileFunction compiles a semantic function body into the VM's execution form.
func CompileFunction(fn *wasmir.Function) (*Function, error) {
	ctrl, err := analyzeControl(fn.Body)
	if err != nil {
		return nil, err
	}

	out := &Function{
		locals: slices.Clone(fn.Locals),
		code:   make([]instr, len(fn.Body)),
	}
	labelStack := make([]int, 0)

	for pc, ins := range fn.Body {
		op := instr{kind: ins.Kind, target: -1}
		switch ins.Kind {
		case wasmir.InstrBlock, wasmir.InstrLoop:
			if _, ok := ctrl.labels[pc]; !ok {
				return nil, fmt.Errorf("%s at %d has no matching end", instrName(ins.Kind), pc)
			}
			labelStack = append(labelStack, pc)
		case wasmir.InstrIf:
			label, ok := ctrl.labels[pc]
			if !ok {
				return nil, fmt.Errorf("if at %d has no matching end", pc)
			}
			if label.elseIndex >= 0 {
				op.target = label.elseIndex
			} else {
				op.target = label.endIndex
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
			op.target = label.endIndex
		case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
			op.index = ins.LocalIndex
		case wasmir.InstrCall, wasmir.InstrReturnCall:
			op.index = ins.FuncIndex
		case wasmir.InstrCallIndirect, wasmir.InstrReturnCallIndirect:
			op.index = ins.TableIndex
			op.bits = int64(ins.CallTypeIndex)
		case wasmir.InstrRefNull:
			op.index = uint32(len(out.refTypes))
			out.refTypes = append(out.refTypes, ins.RefType)
		case wasmir.InstrRefFunc:
			op.index = ins.FuncIndex
		case wasmir.InstrGlobalGet, wasmir.InstrGlobalSet:
			op.index = ins.GlobalIndex
		case wasmir.InstrMemorySize, wasmir.InstrMemoryGrow, wasmir.InstrMemoryFill:
			op.index = ins.MemoryIndex
		case wasmir.InstrTableGet, wasmir.InstrTableSet, wasmir.InstrTableSize,
			wasmir.InstrTableGrow, wasmir.InstrTableFill:
			op.index = ins.TableIndex
		case wasmir.InstrTableCopy:
			op.index = ins.TableIndex
			op.bits = int64(ins.SourceTableIndex)
		case wasmir.InstrTableInit:
			op.index = ins.TableIndex
			op.bits = int64(ins.ElemIndex)
		case wasmir.InstrElemDrop:
			op.index = ins.ElemIndex
		case wasmir.InstrMemoryCopy:
			op.index = ins.MemoryIndex
			op.bits = int64(ins.SourceMemoryIndex)
		case wasmir.InstrMemoryInit:
			op.index = ins.MemoryIndex
			op.bits = int64(ins.DataIndex)
		case wasmir.InstrDataDrop:
			op.index = ins.DataIndex
		case wasmir.InstrI32Load, wasmir.InstrI32Store,
			wasmir.InstrI32Load8S, wasmir.InstrI32Load8U,
			wasmir.InstrI32Load16S, wasmir.InstrI32Load16U,
			wasmir.InstrI32Store8, wasmir.InstrI32Store16,
			wasmir.InstrI64Load, wasmir.InstrI64Store,
			wasmir.InstrI64Load8S, wasmir.InstrI64Load8U,
			wasmir.InstrI64Load16S, wasmir.InstrI64Load16U,
			wasmir.InstrI64Load32S, wasmir.InstrI64Load32U,
			wasmir.InstrI64Store8, wasmir.InstrI64Store16, wasmir.InstrI64Store32,
			wasmir.InstrF32Load, wasmir.InstrF32Store,
			wasmir.InstrF64Load, wasmir.InstrF64Store:
			if ins.MemoryOffset > maxInt64Uint {
				return nil, fmt.Errorf("%s at %d: memory offset %d is too large", instrName(ins.Kind), pc, ins.MemoryOffset)
			}
			op.index = ins.MemoryIndex
			op.bits = int64(ins.MemoryOffset)
		case wasmir.InstrBr, wasmir.InstrBrIf:
			target, err := compileBranchTarget(ins.BranchDepth, labelStack, ctrl)
			if err != nil {
				return nil, fmt.Errorf("%s at %d: %w", instrName(ins.Kind), pc, err)
			}
			op.target = target
		case wasmir.InstrBrTable:
			targets := make([]int, 0, len(ins.BranchTable)+1)
			for i, depth := range ins.BranchTable {
				target, err := compileBranchTarget(depth, labelStack, ctrl)
				if err != nil {
					return nil, fmt.Errorf("br_table at %d target %d: %w", pc, i, err)
				}
				targets = append(targets, target)
			}
			target, err := compileBranchTarget(ins.BranchDefault, labelStack, ctrl)
			if err != nil {
				return nil, fmt.Errorf("br_table at %d default target: %w", pc, err)
			}
			targets = append(targets, target)
			op.index = uint32(len(out.branchTables))
			out.branchTables = append(out.branchTables, targets)
		case wasmir.InstrI32Const:
			op.bits = int64(ins.I32Const)
		case wasmir.InstrI64Const:
			op.bits = ins.I64Const
		case wasmir.InstrF32Const:
			op.bits = int64(ins.F32Const)
		case wasmir.InstrF64Const:
			op.bits = int64(ins.F64Const)
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul,
			wasmir.InstrI32DivS, wasmir.InstrI32DivU, wasmir.InstrI32RemS, wasmir.InstrI32RemU,
			wasmir.InstrI32And, wasmir.InstrI32Or, wasmir.InstrI32Xor,
			wasmir.InstrI32Shl, wasmir.InstrI32ShrS, wasmir.InstrI32ShrU,
			wasmir.InstrI32Rotl, wasmir.InstrI32Rotr,
			wasmir.InstrI32Clz, wasmir.InstrI32Ctz, wasmir.InstrI32Popcnt,
			wasmir.InstrI32Extend8S, wasmir.InstrI32Extend16S,
			wasmir.InstrI32Eq, wasmir.InstrI32Ne,
			wasmir.InstrI32LtS, wasmir.InstrI32LtU, wasmir.InstrI32LeS, wasmir.InstrI32LeU,
			wasmir.InstrI32GtS, wasmir.InstrI32GtU, wasmir.InstrI32GeS, wasmir.InstrI32GeU,
			wasmir.InstrI32Eqz,
			wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul,
			wasmir.InstrI64DivS, wasmir.InstrI64DivU, wasmir.InstrI64RemS, wasmir.InstrI64RemU,
			wasmir.InstrI64And, wasmir.InstrI64Or, wasmir.InstrI64Xor,
			wasmir.InstrI64Shl, wasmir.InstrI64ShrS, wasmir.InstrI64ShrU,
			wasmir.InstrI64Rotl, wasmir.InstrI64Rotr,
			wasmir.InstrI64Clz, wasmir.InstrI64Ctz, wasmir.InstrI64Popcnt,
			wasmir.InstrI64Extend8S, wasmir.InstrI64Extend16S, wasmir.InstrI64Extend32S,
			wasmir.InstrI32WrapI64,
			wasmir.InstrI32TruncF32S, wasmir.InstrI32TruncF32U,
			wasmir.InstrI32TruncF64S, wasmir.InstrI32TruncF64U,
			wasmir.InstrI32TruncSatF32S, wasmir.InstrI32TruncSatF32U,
			wasmir.InstrI32TruncSatF64S, wasmir.InstrI32TruncSatF64U,
			wasmir.InstrI64ExtendI32S, wasmir.InstrI64ExtendI32U,
			wasmir.InstrI64TruncF32S, wasmir.InstrI64TruncF32U,
			wasmir.InstrI64TruncF64S, wasmir.InstrI64TruncF64U,
			wasmir.InstrI64TruncSatF32S, wasmir.InstrI64TruncSatF32U,
			wasmir.InstrI64TruncSatF64S, wasmir.InstrI64TruncSatF64U,
			wasmir.InstrI64Eq, wasmir.InstrI64Ne,
			wasmir.InstrI64LtS, wasmir.InstrI64LtU, wasmir.InstrI64LeS, wasmir.InstrI64LeU,
			wasmir.InstrI64GtS, wasmir.InstrI64GtU, wasmir.InstrI64GeS, wasmir.InstrI64GeU,
			wasmir.InstrI64Eqz,
			wasmir.InstrF32ConvertI32S, wasmir.InstrF32ConvertI32U,
			wasmir.InstrF32ConvertI64S, wasmir.InstrF32ConvertI64U,
			wasmir.InstrF32DemoteF64,
			wasmir.InstrF32Abs, wasmir.InstrF32Neg, wasmir.InstrF32Sqrt,
			wasmir.InstrF32Ceil, wasmir.InstrF32Floor, wasmir.InstrF32Trunc, wasmir.InstrF32Nearest,
			wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div,
			wasmir.InstrF32Min, wasmir.InstrF32Max, wasmir.InstrF32Copysign,
			wasmir.InstrF32Eq, wasmir.InstrF32Ne,
			wasmir.InstrF32Lt, wasmir.InstrF32Le, wasmir.InstrF32Gt, wasmir.InstrF32Ge,
			wasmir.InstrF64ConvertI32S, wasmir.InstrF64ConvertI32U,
			wasmir.InstrF64ConvertI64S, wasmir.InstrF64ConvertI64U,
			wasmir.InstrF64PromoteF32,
			wasmir.InstrF64Abs, wasmir.InstrF64Neg, wasmir.InstrF64Sqrt,
			wasmir.InstrF64Ceil, wasmir.InstrF64Floor, wasmir.InstrF64Trunc, wasmir.InstrF64Nearest,
			wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div,
			wasmir.InstrF64Min, wasmir.InstrF64Max, wasmir.InstrF64Copysign,
			wasmir.InstrF64Eq, wasmir.InstrF64Ne,
			wasmir.InstrF64Lt, wasmir.InstrF64Le, wasmir.InstrF64Gt, wasmir.InstrF64Ge,
			wasmir.InstrI32ReinterpretF32, wasmir.InstrI64ReinterpretF64,
			wasmir.InstrF32ReinterpretI32, wasmir.InstrF64ReinterpretI64,
			wasmir.InstrDrop, wasmir.InstrSelect, wasmir.InstrNop, wasmir.InstrUnreachable,
			wasmir.InstrRefIsNull,
			wasmir.InstrReturn:
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
			return nil, fmt.Errorf("unsupported instruction %s", instrName(ins.Kind))
		}
		out.code[pc] = op
	}

	if len(labelStack) != 0 {
		start := labelStack[len(labelStack)-1]
		return nil, fmt.Errorf("%s at %d without matching end", instrName(fn.Body[start].Kind), start)
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
	return label.branchTarget, nil
}

// controlLabel describes one structured-control label in the flattened
// instruction stream.
//
// endIndex is the matching end instruction. branchTarget is the instruction pc
// assigned to br/br_if/br_table for this label; it is endIndex for block and if,
// but the loop instruction pc for loop, so the interpreter's pc increment
// re-enters the loop body. elseIndex is the matching else instruction for if
// labels, or -1 when the label has no else arm.
type controlLabel struct {
	endIndex     int
	branchTarget int
	elseIndex    int
}

// controlInfo stores precomputed control-boundary metadata by opening
// instruction index. Only block, loop, and if instructions have entries.
type controlInfo struct {
	labels map[int]controlLabel
}

// analyzeControl records matching structured-control boundaries in body.
//
// The VM assumes modules were validated before instantiation, but it still
// receives a plain wasmir.Module and should not rely on nested source syntax.
// This pass treats block/loop/if as openers, else as metadata on the current
// if, and end as the closer for the innermost opener. End instructions with no
// opener are accepted here because the final function end is represented the
// same way as a structured-control end.
func analyzeControl(body []wasmir.Instruction) (controlInfo, error) {
	ctrl := controlInfo{labels: make(map[int]controlLabel)}
	stack := make([]int, 0)
	elseIndex := make(map[int]int)

	for pc, ins := range body {
		switch ins.Kind {
		case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf:
			stack = append(stack, pc)
		case wasmir.InstrElse:
			if len(stack) == 0 {
				return controlInfo{}, fmt.Errorf("else at %d without matching if", pc)
			}
			start := stack[len(stack)-1]
			if body[start].Kind != wasmir.InstrIf {
				return controlInfo{}, fmt.Errorf("else at %d matched non-if instruction %s", pc, instrName(body[start].Kind))
			}
			elseIndex[start] = pc
		case wasmir.InstrEnd:
			if len(stack) == 0 {
				continue
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			label := controlLabel{
				endIndex:     pc,
				branchTarget: pc,
				elseIndex:    -1,
			}
			if body[start].Kind == wasmir.InstrLoop {
				label.branchTarget = start
			}
			if elsePC, ok := elseIndex[start]; ok {
				label.elseIndex = elsePC
			}
			ctrl.labels[start] = label
		}
	}
	if len(stack) != 0 {
		return controlInfo{}, fmt.Errorf("%s at %d without matching end", instrName(body[stack[len(stack)-1]].Kind), stack[len(stack)-1])
	}
	return ctrl, nil
}

// instrName formats instruction kinds for VM errors.
func instrName(kind wasmir.InstrKind) string {
	if def, ok := instrdef.LookupInstructionByKind(kind); ok {
		return def.TextName
	}
	return fmt.Sprintf("instruction(%d)", kind)
}
