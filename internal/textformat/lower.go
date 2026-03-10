package textformat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/numlit"
	"github.com/eliben/watgo/wasmir"
)

// moduleLowerer owns module-wide lowering state.
type moduleLowerer struct {
	// out is the semantic module being constructed during lowering. All
	// successfully lowered types, functions, and exports are appended here as
	// we walk the AST, even if other parts fail and diagnostics are collected.
	out *wasmir.Module

	// diags accumulates every lowering diagnostic discovered for the module.
	// Lowering keeps going after errors so callers get a complete error list in
	// one pass instead of failing at the first issue.
	diags diag.ErrorList

	// funcsByName maps function identifiers to their function indices in the
	// source module. It is used to resolve call operands like "$f" to concrete
	// function indices.
	funcsByName map[string]uint32
}

// functionLowerer owns state while lowering one function.
type functionLowerer struct {
	// mod points to the parent module-level lowering context.
	mod *moduleLowerer

	// funcIdx is this function's index in the source module's function list.
	funcIdx int

	// fn is the source text-format function AST currently being lowered.
	// Per-function methods read declarations and instructions from this node.
	fn *Function

	// params holds lowered parameter value types in declaration order.
	params []wasmir.ValueType

	// paramNames holds source parameter identifiers aligned 1:1 with params.
	// Empty entries represent unnamed parameters.
	paramNames []string

	// results holds lowered result value types in declaration order.
	results []wasmir.ValueType

	// locals holds lowered local variable value types (excluding params).
	locals []wasmir.ValueType

	// localNames holds source local identifiers aligned 1:1 with locals.
	// Empty entries represent unnamed locals.
	localNames []string

	// body stores lowered semantic instructions as they are produced.
	body []wasmir.Instruction

	// localsByName maps text local identifiers (for params and locals) to their
	// resolved local indices.
	localsByName map[string]uint32

	// nextLocalIndex tracks the next available local index while processing
	// params and locals.
	nextLocalIndex uint32
}

// LowerModule lowers astm (a parsed text-format module) into a semantic
// wasmir.Module.
// It returns the lowered module (possibly partial) and nil on success.
// On any failure, it returns diag.ErrorList.
func LowerModule(astm *Module) (*wasmir.Module, error) {
	if astm == nil {
		return nil, diag.Fromf("module is nil")
	}

	l := newModuleLowerer()
	l.lowerModule(astm)
	if l.diags.HasAny() {
		return l.out, l.diags
	}
	return l.out, nil
}

// newModuleLowerer creates a module lowerer with an empty output module.
func newModuleLowerer() *moduleLowerer {
	return &moduleLowerer{
		out:         &wasmir.Module{},
		funcsByName: map[string]uint32{},
	}
}

// lowerModule lowers all functions in astm into l.out and accumulates
// diagnostics in l.diags.
func (l *moduleLowerer) lowerModule(astm *Module) {
	l.collectFunctionNames(astm)
	for i, f := range astm.Funcs {
		if f == nil {
			l.diags.Addf("func[%d]: nil function", i)
			continue
		}
		l.lowerFunction(i, f)
	}
}

// collectFunctionNames pre-scans astm and records named function indices.
func (l *moduleLowerer) collectFunctionNames(astm *Module) {
	for i, f := range astm.Funcs {
		if f == nil || f.Id == "" {
			continue
		}
		if prev, exists := l.funcsByName[f.Id]; exists {
			l.diags.Addf("func[%d] %s: duplicate function id (first seen at func[%d])", i, f.Id, prev)
			continue
		}
		l.funcsByName[f.Id] = uint32(i)
	}
}

// lowerFunction lowers one text-format function f as function number funcIdx
// into the output module.
func (l *moduleLowerer) lowerFunction(funcIdx int, f *Function) {
	fl := newFunctionLowerer(l, funcIdx, f)
	fl.lower()
}

// newFunctionLowerer constructs a per-function lowering context.
func newFunctionLowerer(mod *moduleLowerer, funcIdx int, fn *Function) *functionLowerer {
	return &functionLowerer{
		mod:          mod,
		funcIdx:      funcIdx,
		fn:           fn,
		localsByName: map[string]uint32{},
		body:         make([]wasmir.Instruction, 0, len(fn.Instrs)+1),
	}
}

// lower lowers fl.fn into fl.mod.out and records any diagnostics.
func (fl *functionLowerer) lower() {
	fl.lowerTypeUse()
	fl.lowerLocals()

	typeIdx := uint32(len(fl.mod.out.Types))
	fl.mod.out.Types = append(fl.mod.out.Types, wasmir.FuncType{Params: fl.params, Results: fl.results})

	fl.lowerInstrs()
	fl.body = append(fl.body, wasmir.Instruction{Kind: wasmir.InstrEnd, SourceLoc: fl.fn.loc.String()})

	fl.mod.out.Funcs = append(fl.mod.out.Funcs, wasmir.Function{
		TypeIdx:    typeIdx,
		Name:       fl.fn.Id,
		ParamNames: fl.paramNames,
		LocalNames: fl.localNames,
		Locals:     fl.locals,
		Body:       fl.body,
		SourceLoc:  fl.fn.loc.String(),
	})

	if fl.fn.Export != "" {
		fl.mod.out.Exports = append(fl.mod.out.Exports, wasmir.Export{
			Name:  fl.fn.Export,
			Kind:  wasmir.ExternalKindFunction,
			Index: uint32(len(fl.mod.out.Funcs) - 1),
		})
	}
}

// lowerTypeUse lowers params/results from fl.fn.TyUse.
func (fl *functionLowerer) lowerTypeUse() {
	if fl.fn.TyUse == nil {
		fl.diagf(fl.fn.loc.String(), "missing function type use")
		return
	}
	fl.lowerParams(fl.fn.TyUse.Params)
	fl.lowerResults(fl.fn.TyUse.Results)
}

// lowerParams lowers parameter declarations and updates the local index space.
func (fl *functionLowerer) lowerParams(params []*ParamDecl) {
	for _, pd := range params {
		if pd == nil {
			fl.diagf("", "nil param declaration")
			continue
		}
		vt, ok := lowerValueType(pd.Ty)
		if !ok {
			fl.diagf(pd.loc.String(), "unsupported param type %q", pd.Ty)
			continue
		}

		fl.params = append(fl.params, vt)
		fl.paramNames = append(fl.paramNames, pd.Id)
		if pd.Id != "" {
			if _, exists := fl.localsByName[pd.Id]; exists {
				fl.diagf(pd.loc.String(), "duplicate param id %q", pd.Id)
			} else {
				fl.localsByName[pd.Id] = fl.nextLocalIndex
			}
		}
		fl.nextLocalIndex++
	}
}

// lowerResults lowers result declarations.
func (fl *functionLowerer) lowerResults(results []*ResultDecl) {
	for _, rd := range results {
		if rd == nil {
			fl.diagf("", "nil result declaration")
			continue
		}
		vt, ok := lowerValueType(rd.Ty)
		if !ok {
			fl.diagf(rd.loc.String(), "unsupported result type %q", rd.Ty)
			continue
		}
		fl.results = append(fl.results, vt)
	}
}

// lowerLocals lowers local declarations and updates the local index space.
func (fl *functionLowerer) lowerLocals() {
	for _, ld := range fl.fn.Locals {
		if ld == nil {
			fl.diagf("", "nil local declaration")
			continue
		}
		vt, ok := lowerValueType(ld.Ty)
		if !ok {
			fl.diagf(ld.loc.String(), "unsupported local type %q", ld.Ty)
			continue
		}

		fl.locals = append(fl.locals, vt)
		fl.localNames = append(fl.localNames, ld.Id)
		if ld.Id != "" {
			if _, exists := fl.localsByName[ld.Id]; exists {
				fl.diagf(ld.loc.String(), "duplicate local id %q", ld.Id)
			} else {
				fl.localsByName[ld.Id] = fl.nextLocalIndex
			}
		}
		fl.nextLocalIndex++
	}
}

// lowerInstrs lowers all instructions in fl.fn into fl.body.
func (fl *functionLowerer) lowerInstrs() {
	for _, instr := range fl.fn.Instrs {
		fl.lowerInstruction(instr)
	}
}

// lowerInstruction lowers one instruction node (plain or folded).
func (fl *functionLowerer) lowerInstruction(instr Instruction) {
	switch in := instr.(type) {
	case *PlainInstr:
		fl.lowerPlainInstr(in)
	case *FoldedInstr:
		fl.lowerFoldedInstr(in)
	default:
		fl.diagf(instr.Loc(), "unsupported instruction type %T", instr)
	}
}

// lowerFoldedInstr lowers one folded instruction expression.
func (fl *functionLowerer) lowerFoldedInstr(fi *FoldedInstr) {
	if fi == nil {
		fl.diagf("", "nil folded instruction")
		return
	}
	if fi.Name == "if" {
		fl.lowerFoldedIf(fi)
		return
	}

	var operands []Operand
	for _, arg := range fi.Args {
		if arg.Instr != nil {
			fl.lowerInstruction(arg.Instr)
			continue
		}
		if arg.Operand != nil {
			operands = append(operands, arg.Operand)
			continue
		}
		fl.diagf(fi.Loc(), "invalid folded argument in %q", fi.Name)
	}

	fl.lowerPlainInstr(&PlainInstr{Name: fi.Name, Operands: operands, loc: fi.loc})
}

// lowerFoldedIf lowers a folded if-expression preserving then/else blocks.
func (fl *functionLowerer) lowerFoldedIf(fi *FoldedInstr) {
	var resultOp Operand
	var thenClause *FoldedInstr
	var elseClause *FoldedInstr

	for _, arg := range fi.Args {
		if arg.Operand != nil {
			fl.diagf(arg.Operand.Loc(), "if expects nested expressions/clauses")
			continue
		}

		nested, ok := arg.Instr.(*FoldedInstr)
		if !ok {
			fl.lowerInstruction(arg.Instr)
			continue
		}

		switch nested.Name {
		case "result":
			if len(nested.Args) != 1 || nested.Args[0].Operand == nil || nested.Args[0].Instr != nil {
				fl.diagf(nested.Loc(), "invalid if result clause")
				continue
			}
			if resultOp != nil {
				fl.diagf(nested.Loc(), "duplicate if result clause")
				continue
			}
			resultOp = nested.Args[0].Operand
		case "then":
			if thenClause != nil {
				fl.diagf(nested.Loc(), "duplicate then clause")
				continue
			}
			thenClause = nested
		case "else":
			if elseClause != nil {
				fl.diagf(nested.Loc(), "duplicate else clause")
				continue
			}
			elseClause = nested
		default:
			// Condition expressions.
			fl.lowerInstruction(nested)
		}
	}

	if thenClause == nil {
		fl.diagf(fi.Loc(), "if requires then clause")
		return
	}

	var ifOps []Operand
	if resultOp != nil {
		ifOps = append(ifOps, resultOp)
	}
	fl.lowerPlainInstr(&PlainInstr{Name: "if", Operands: ifOps, loc: fi.loc})
	fl.lowerFoldedClauseInstrs(thenClause)
	if elseClause != nil {
		fl.lowerPlainInstr(&PlainInstr{Name: "else", loc: elseClause.loc})
		fl.lowerFoldedClauseInstrs(elseClause)
	}
	fl.lowerPlainInstr(&PlainInstr{Name: "end", loc: fi.loc})
}

// lowerFoldedClauseInstrs lowers all instruction children in a then/else
// folded clause.
func (fl *functionLowerer) lowerFoldedClauseInstrs(clause *FoldedInstr) {
	for _, arg := range clause.Args {
		if arg.Instr == nil || arg.Operand != nil {
			fl.diagf(clause.Loc(), "%s clause expects nested instruction expressions", clause.Name)
			continue
		}
		fl.lowerInstruction(arg.Instr)
	}
}

// lowerPlainInstr lowers one plain instruction into fl.body.
func (fl *functionLowerer) lowerPlainInstr(pi *PlainInstr) {
	instrLoc := pi.Loc()

	switch pi.Name {
	case "local.get":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "local.get expects 1 operand")
			return
		}
		localIndex, ok := lowerLocalIndexOperand(pi.Operands[0], fl.localsByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid local.get operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrLocalGet, LocalIndex: localIndex, SourceLoc: instrLoc})
	case "call":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "call expects 1 operand")
			return
		}
		funcIndex, ok := lowerFuncIndexOperand(pi.Operands[0], fl.mod.funcsByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid call operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrCall, FuncIndex: funcIndex, SourceLoc: instrLoc})
	case "if":
		if len(pi.Operands) > 1 {
			fl.diagf(instrLoc, "if expects at most 1 operand")
			return
		}
		ins := wasmir.Instruction{Kind: wasmir.InstrIf, SourceLoc: instrLoc}
		if len(pi.Operands) == 1 {
			vt, ok := lowerBlockResultTypeOperand(pi.Operands[0])
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid if result type")
				return
			}
			ins.BlockHasResult = true
			ins.BlockType = vt
		}
		fl.emitInstr(ins)
	case "else":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "else expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrElse, SourceLoc: instrLoc})
	case "end":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "end expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrEnd, SourceLoc: instrLoc})

	case "i32.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "i32.const expects 1 operand")
			return
		}
		imm, ok := lowerI32ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid i32.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Const, I32Const: imm, SourceLoc: instrLoc})

	case "i64.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "i64.const expects 1 operand")
			return
		}
		imm, ok := lowerI64ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid i64.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Const, I64Const: imm, SourceLoc: instrLoc})

	case "f32.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "f32.const expects 1 operand")
			return
		}
		imm, ok := lowerF32ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid f32.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Const, F32Const: imm, SourceLoc: instrLoc})

	case "f64.const":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "f64.const expects 1 operand")
			return
		}
		imm, ok := lowerF64ConstOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid f64.const operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Const, F64Const: imm, SourceLoc: instrLoc})

	case "drop":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "drop expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrDrop, SourceLoc: instrLoc})

	case "i32.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Add, SourceLoc: instrLoc})

	case "i32.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Sub, SourceLoc: instrLoc})

	case "i32.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32Mul, SourceLoc: instrLoc})

	case "i32.div_s":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.div_s expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32DivS, SourceLoc: instrLoc})

	case "i32.div_u":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i32.div_u expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI32DivU, SourceLoc: instrLoc})

	case "i64.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Add, SourceLoc: instrLoc})
	case "i64.eqz":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.eqz expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Eqz, SourceLoc: instrLoc})
	case "i64.le_u":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.le_u expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64LeU, SourceLoc: instrLoc})

	case "i64.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Sub, SourceLoc: instrLoc})

	case "i64.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64Mul, SourceLoc: instrLoc})

	case "i64.div_s":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.div_s expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64DivS, SourceLoc: instrLoc})

	case "i64.div_u":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "i64.div_u expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrI64DivU, SourceLoc: instrLoc})

	case "f32.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Add, SourceLoc: instrLoc})

	case "f32.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Sub, SourceLoc: instrLoc})

	case "f32.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Mul, SourceLoc: instrLoc})

	case "f32.div":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.div expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Div, SourceLoc: instrLoc})

	case "f32.sqrt":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.sqrt expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Sqrt, SourceLoc: instrLoc})

	case "f32.min":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.min expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Min, SourceLoc: instrLoc})

	case "f32.max":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.max expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Max, SourceLoc: instrLoc})

	case "f32.ceil":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.ceil expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Ceil, SourceLoc: instrLoc})

	case "f32.floor":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.floor expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Floor, SourceLoc: instrLoc})

	case "f32.trunc":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.trunc expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Trunc, SourceLoc: instrLoc})

	case "f32.nearest":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f32.nearest expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF32Nearest, SourceLoc: instrLoc})

	case "f64.add":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.add expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Add, SourceLoc: instrLoc})

	case "f64.sub":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.sub expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Sub, SourceLoc: instrLoc})

	case "f64.mul":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.mul expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Mul, SourceLoc: instrLoc})

	case "f64.div":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.div expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Div, SourceLoc: instrLoc})

	case "f64.sqrt":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.sqrt expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Sqrt, SourceLoc: instrLoc})

	case "f64.min":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.min expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Min, SourceLoc: instrLoc})

	case "f64.max":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.max expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Max, SourceLoc: instrLoc})

	case "f64.ceil":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.ceil expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Ceil, SourceLoc: instrLoc})

	case "f64.floor":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.floor expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Floor, SourceLoc: instrLoc})

	case "f64.trunc":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.trunc expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Trunc, SourceLoc: instrLoc})

	case "f64.nearest":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "f64.nearest expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrF64Nearest, SourceLoc: instrLoc})

	default:
		fl.diagf(instrLoc, "unsupported instruction %q", pi.Name)
	}
}

// emitInstr appends one lowered instruction to the current function body.
func (fl *functionLowerer) emitInstr(instr wasmir.Instruction) {
	fl.body = append(fl.body, instr)
}

// diagf adds one lowering diagnostic for the current function.
func (fl *functionLowerer) diagf(loc string, format string, args ...any) {
	addLowerDiag(&fl.mod.diags, fl.funcIdx, fl.fn.Id, loc, format, args...)
}

// lowerI32ConstOperand resolves op as an i32.const immediate.
// It returns the immediate value and true on success.
func lowerI32ConstOperand(op Operand) (int32, bool) {
	o, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}

	bits, err := numlit.ParseIntBits(o.Value, 32)
	if err != nil {
		return 0, false
	}
	return int32(bits), true
}

// lowerI64ConstOperand resolves op as an i64.const immediate.
// It returns the immediate value and true on success.
func lowerI64ConstOperand(op Operand) (int64, bool) {
	o, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}

	bits, err := numlit.ParseIntBits(o.Value, 64)
	if err != nil {
		return 0, false
	}
	return int64(bits), true
}

// lowerF32ConstOperand resolves op as an f32.const immediate.
// It returns IEEE-754 f32 bits and true on success.
func lowerF32ConstOperand(op Operand) (uint32, bool) {
	switch o := op.(type) {
	case *FloatOperand:
		bits, err := numlit.ParseF32Bits(o.Value)
		return bits, err == nil
	case *IntOperand:
		bits, err := numlit.ParseF32Bits(o.Value)
		return bits, err == nil
	default:
		return 0, false
	}
}

// lowerF64ConstOperand resolves op as an f64.const immediate.
// It returns IEEE-754 f64 bits and true on success.
func lowerF64ConstOperand(op Operand) (uint64, bool) {
	switch o := op.(type) {
	case *FloatOperand:
		bits, err := numlit.ParseF64Bits(o.Value)
		return bits, err == nil
	case *IntOperand:
		bits, err := numlit.ParseF64Bits(o.Value)
		return bits, err == nil
	default:
		return 0, false
	}
}

// lowerLocalIndexOperand resolves op as a local index using localsByName.
// It returns the resolved index and true on success, or 0/false otherwise.
func lowerLocalIndexOperand(op Operand, localsByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := localsByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// lowerFuncIndexOperand resolves op as a function index using funcsByName.
// It returns the resolved index and true on success, or 0/false otherwise.
func lowerFuncIndexOperand(op Operand, funcsByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := funcsByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// lowerBlockResultTypeOperand resolves op as a block/if result type keyword.
// It returns the lowered type and true on success.
func lowerBlockResultTypeOperand(op Operand) (wasmir.ValueType, bool) {
	kw, ok := op.(*KeywordOperand)
	if !ok {
		return 0, false
	}
	return lowerValueType(&BasicType{Name: kw.Value})
}

// parseU32Literal parses s as an unsigned 32-bit integer literal.
// It returns the parsed value and true on success, or 0/false on failure.
func parseU32Literal(s string) (uint32, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	value, err := strconv.ParseInt(clean, 0, 64)
	if err != nil || value < 0 || value > (1<<32-1) {
		return 0, false
	}
	return uint32(value), true
}

// lowerValueType lowers ty from textformat type syntax into semantic wasmir
// type representation.
// It returns the lowered type and true on success, or 0/false if ty is
// unsupported.
func lowerValueType(ty Type) (wasmir.ValueType, bool) {
	bt, ok := ty.(*BasicType)
	if !ok {
		return 0, false
	}

	switch bt.Name {
	case "i32":
		return wasmir.ValueTypeI32, true
	case "i64":
		return wasmir.ValueTypeI64, true
	case "f32":
		return wasmir.ValueTypeF32, true
	case "f64":
		return wasmir.ValueTypeF64, true
	default:
		return 0, false
	}
}

// addLowerDiag appends one lowering diagnostic prefixed with function context
// and optional source location.
// If loc is non-empty, the message format is:
//
//	"func[...] at <loc>: <message>"
//
// Otherwise:
//
//	"func[...]: <message>"
//
// Function context always includes the numeric function index and includes the
// textual function identifier too when present.
func addLowerDiag(diags *diag.ErrorList, funcIdx int, funcName string, loc string, format string, args ...any) {
	fnCtx := formatFunctionContext(funcIdx, funcName)
	if loc != "" {
		diags.Addf("%s at %s: "+format, append([]any{fnCtx, loc}, args...)...)
		return
	}
	diags.Addf("%s: "+format, append([]any{fnCtx}, args...)...)
}

// formatFunctionContext formats a function diagnostic prefix using function
// index and, when available, the source function identifier.
func formatFunctionContext(funcIdx int, funcName string) string {
	if funcName == "" {
		return fmt.Sprintf("func[%d]", funcIdx)
	}
	return fmt.Sprintf("func[%d] %s", funcIdx, funcName)
}
