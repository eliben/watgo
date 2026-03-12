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

	// labelStack tracks active structured control labels from innermost to
	// outermost for lowering branch operands (br/br_if).
	labelStack []labelScope
}

// labelScope describes one active structured control label.
type labelScope struct {
	// name is the optional textual label identifier (for example "$loop").
	// Empty means anonymous label used for numeric depths only.
	name string
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
		labelStack:   make([]labelScope, 0, 8),
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
	if fi.Name == "block" {
		fl.lowerFoldedBlock(fi, false)
		return
	}
	if fi.Name == "loop" {
		fl.lowerFoldedBlock(fi, true)
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
	fl.pushLabel("")
	fl.lowerFoldedClauseInstrs(thenClause)
	if elseClause != nil {
		fl.lowerPlainInstr(&PlainInstr{Name: "else", loc: elseClause.loc})
		fl.lowerFoldedClauseInstrs(elseClause)
	}
	fl.popLabel()
	fl.lowerPlainInstr(&PlainInstr{Name: "end", loc: fi.loc})
}

// lowerFoldedBlock lowers folded structured control forms "(block ...)" and
// "(loop ...)" while preserving their nested instruction bodies.
//
// Examples this handles:
//
//	(block
//	  (i64.const 1)
//	  (br 0))
//
//	(loop $l (param i64 i64) (result i64)
//	  (br_if $l)
//	  (return))
//
//	(block $done (result i64)
//	  (i64.const 7))
//
// Parsing/shape comes from the folded text forms in the core text format
// grammar (see "folded instruction" conventions in the spec text syntax). We
// also map block signatures to the binary blocktype model:
//   - empty blocktype (no params/results),
//   - valtype blocktype (single result),
//   - type-index blocktype (multi-value signature).
//
// This follows the core binary blocktype rules.
func (fl *functionLowerer) lowerFoldedBlock(fi *FoldedInstr, isLoop bool) {
	var labelName string
	var paramTypes []wasmir.ValueType
	var resultTypes []wasmir.ValueType
	var bodyInstrs []Instruction

	for i, arg := range fi.Args {
		if arg.Operand != nil {
			// The first operand in folded block/loop may be a label identifier:
			//   (block $name ...)
			//   (loop $name ...)
			// All other raw operands are invalid for these forms; everything
			// else should be nested instruction/annotation lists.
			if i == 0 {
				if id, ok := arg.Operand.(*IdOperand); ok {
					labelName = id.Value
					continue
				}
			}
			fl.diagf(arg.Operand.Loc(), "%s expects nested instructions/clauses", fi.Name)
			continue
		}

		nested, ok := arg.Instr.(*FoldedInstr)
		if !ok {
			bodyInstrs = append(bodyInstrs, arg.Instr)
			continue
		}

		switch nested.Name {
		case "result":
			// Result annotation in text:
			//   (result t1 t2 ...)
			// For this lowering pass we allow at most one explicit result
			// clause and collect all listed result value types.
			if len(nested.Args) == 0 {
				fl.diagf(nested.Loc(), "invalid %s result clause", fi.Name)
				continue
			}
			if len(resultTypes) > 0 {
				fl.diagf(nested.Loc(), "duplicate %s result clause", fi.Name)
				continue
			}
			for _, resultArg := range nested.Args {
				if resultArg.Operand == nil || resultArg.Instr != nil {
					fl.diagf(nested.Loc(), "invalid %s result clause", fi.Name)
					continue
				}
				vt, ok := lowerBlockResultTypeOperand(resultArg.Operand)
				if !ok {
					fl.diagf(resultArg.Operand.Loc(), "unsupported %s result type", fi.Name)
					continue
				}
				resultTypes = append(resultTypes, vt)
			}
		case "param":
			// Parameter annotation in text:
			//   (param t1 t2 ...)
			// Loop parameters are important for branch-to-loop typing and
			// become part of the blocktype signature when we select a
			// type-index blocktype.
			for _, paramArg := range nested.Args {
				if paramArg.Operand == nil || paramArg.Instr != nil {
					fl.diagf(nested.Loc(), "invalid %s param clause", fi.Name)
					continue
				}
				vt, ok := lowerBlockResultTypeOperand(paramArg.Operand)
				if !ok {
					fl.diagf(paramArg.Operand.Loc(), "unsupported %s param type", fi.Name)
					continue
				}
				paramTypes = append(paramTypes, vt)
			}
		default:
			// Any other nested list is treated as a normal body instruction.
			bodyInstrs = append(bodyInstrs, nested)
		}
	}

	kind := wasmir.InstrBlock
	if isLoop {
		kind = wasmir.InstrLoop
	}

	switch {
	case len(paramTypes) > 0 || len(resultTypes) > 1:
		// Multi-value signatures (or any explicit params) require a type-index
		// blocktype per the binary format. We append a synthetic function type
		// to Module.Types and reference it from the instruction.
		typeIdx := uint32(len(fl.mod.out.Types))
		fl.mod.out.Types = append(fl.mod.out.Types, wasmir.FuncType{
			Params:  paramTypes,
			Results: resultTypes,
		})
		fl.emitInstr(wasmir.Instruction{
			Kind:               kind,
			BlockTypeUsesIndex: true,
			BlockTypeIndex:     typeIdx,
			SourceLoc:          fi.loc.String(),
		})
	case len(resultTypes) == 1:
		// Single-result blocktype can be encoded directly as a value type.
		fl.emitInstr(wasmir.Instruction{
			Kind:           kind,
			BlockHasResult: true,
			BlockType:      resultTypes[0],
			SourceLoc:      fi.loc.String(),
		})
	default:
		// No signature annotation => empty blocktype.
		fl.emitInstr(wasmir.Instruction{Kind: kind, SourceLoc: fi.loc.String()})
	}

	// The label scope is active only for this structured body. Branch labels
	// resolve from innermost to outermost against this stack.
	fl.pushLabel(labelName)
	for _, body := range bodyInstrs {
		fl.lowerInstruction(body)
	}
	fl.popLabel()
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

// loweringSpec describes table-driven lowering for one plain instruction.
type loweringSpec struct {
	kind         wasmir.InstrKind
	operandCount int
	decode       loweringOperandDecoder
}

// loweringOperandDecoder decodes instruction operands into ins.
// It returns true on success and false when operands are invalid.
type loweringOperandDecoder func(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool

// loweringSpecs maps plain instruction names to table-driven lowering rules.
var loweringSpecs = map[string]loweringSpec{
	"else":             {kind: wasmir.InstrElse, operandCount: 0},
	"end":              {kind: wasmir.InstrEnd, operandCount: 0},
	"drop":             {kind: wasmir.InstrDrop, operandCount: 0},
	"i32.add":          {kind: wasmir.InstrI32Add, operandCount: 0},
	"i32.sub":          {kind: wasmir.InstrI32Sub, operandCount: 0},
	"i32.mul":          {kind: wasmir.InstrI32Mul, operandCount: 0},
	"i32.div_s":        {kind: wasmir.InstrI32DivS, operandCount: 0},
	"i32.div_u":        {kind: wasmir.InstrI32DivU, operandCount: 0},
	"i32.rem_s":        {kind: wasmir.InstrI32RemS, operandCount: 0},
	"i32.rem_u":        {kind: wasmir.InstrI32RemU, operandCount: 0},
	"i32.shl":          {kind: wasmir.InstrI32Shl, operandCount: 0},
	"i32.shr_s":        {kind: wasmir.InstrI32ShrS, operandCount: 0},
	"i32.shr_u":        {kind: wasmir.InstrI32ShrU, operandCount: 0},
	"i32.lt_s":         {kind: wasmir.InstrI32LtS, operandCount: 0},
	"i32.lt_u":         {kind: wasmir.InstrI32LtU, operandCount: 0},
	"i64.add":          {kind: wasmir.InstrI64Add, operandCount: 0},
	"i64.eq":           {kind: wasmir.InstrI64Eq, operandCount: 0},
	"i64.eqz":          {kind: wasmir.InstrI64Eqz, operandCount: 0},
	"i64.gt_s":         {kind: wasmir.InstrI64GtS, operandCount: 0},
	"i64.gt_u":         {kind: wasmir.InstrI64GtU, operandCount: 0},
	"i64.le_u":         {kind: wasmir.InstrI64LeU, operandCount: 0},
	"i64.sub":          {kind: wasmir.InstrI64Sub, operandCount: 0},
	"i64.mul":          {kind: wasmir.InstrI64Mul, operandCount: 0},
	"i64.div_s":        {kind: wasmir.InstrI64DivS, operandCount: 0},
	"i64.div_u":        {kind: wasmir.InstrI64DivU, operandCount: 0},
	"i64.rem_s":        {kind: wasmir.InstrI64RemS, operandCount: 0},
	"i64.rem_u":        {kind: wasmir.InstrI64RemU, operandCount: 0},
	"i64.shl":          {kind: wasmir.InstrI64Shl, operandCount: 0},
	"i64.shr_s":        {kind: wasmir.InstrI64ShrS, operandCount: 0},
	"i64.shr_u":        {kind: wasmir.InstrI64ShrU, operandCount: 0},
	"i64.lt_s":         {kind: wasmir.InstrI64LtS, operandCount: 0},
	"i64.lt_u":         {kind: wasmir.InstrI64LtU, operandCount: 0},
	"i32.wrap_i64":     {kind: wasmir.InstrI32WrapI64, operandCount: 0},
	"i64.extend_i32_s": {kind: wasmir.InstrI64ExtendI32S, operandCount: 0},
	"i64.extend_i32_u": {kind: wasmir.InstrI64ExtendI32U, operandCount: 0},
	"f32.add":          {kind: wasmir.InstrF32Add, operandCount: 0},
	"f32.sub":          {kind: wasmir.InstrF32Sub, operandCount: 0},
	"f32.mul":          {kind: wasmir.InstrF32Mul, operandCount: 0},
	"f32.div":          {kind: wasmir.InstrF32Div, operandCount: 0},
	"f32.sqrt":         {kind: wasmir.InstrF32Sqrt, operandCount: 0},
	"f32.min":          {kind: wasmir.InstrF32Min, operandCount: 0},
	"f32.max":          {kind: wasmir.InstrF32Max, operandCount: 0},
	"f32.ceil":         {kind: wasmir.InstrF32Ceil, operandCount: 0},
	"f32.floor":        {kind: wasmir.InstrF32Floor, operandCount: 0},
	"f32.trunc":        {kind: wasmir.InstrF32Trunc, operandCount: 0},
	"f32.nearest":      {kind: wasmir.InstrF32Nearest, operandCount: 0},
	"f64.add":          {kind: wasmir.InstrF64Add, operandCount: 0},
	"f64.sub":          {kind: wasmir.InstrF64Sub, operandCount: 0},
	"f64.mul":          {kind: wasmir.InstrF64Mul, operandCount: 0},
	"f64.div":          {kind: wasmir.InstrF64Div, operandCount: 0},
	"f64.sqrt":         {kind: wasmir.InstrF64Sqrt, operandCount: 0},
	"f64.min":          {kind: wasmir.InstrF64Min, operandCount: 0},
	"f64.max":          {kind: wasmir.InstrF64Max, operandCount: 0},
	"f64.ceil":         {kind: wasmir.InstrF64Ceil, operandCount: 0},
	"f64.floor":        {kind: wasmir.InstrF64Floor, operandCount: 0},
	"f64.trunc":        {kind: wasmir.InstrF64Trunc, operandCount: 0},
	"f64.nearest":      {kind: wasmir.InstrF64Nearest, operandCount: 0},
	"local.get":        {kind: wasmir.InstrLocalGet, operandCount: 1, decode: decodeLocalGetOperands},
	"local.set":        {kind: wasmir.InstrLocalSet, operandCount: 1, decode: decodeLocalSetOperands},
	"call":             {kind: wasmir.InstrCall, operandCount: 1, decode: decodeCallOperands},
	"br":               {kind: wasmir.InstrBr, operandCount: 1, decode: decodeBrOperands},
	"br_if":            {kind: wasmir.InstrBrIf, operandCount: 1, decode: decodeBrOperands},
	"return":           {kind: wasmir.InstrReturn, operandCount: 0},
	"i32.const":        {kind: wasmir.InstrI32Const, operandCount: 1, decode: decodeI32ConstOperands},
	"i64.const":        {kind: wasmir.InstrI64Const, operandCount: 1, decode: decodeI64ConstOperands},
	"f32.const":        {kind: wasmir.InstrF32Const, operandCount: 1, decode: decodeF32ConstOperands},
	"f64.const":        {kind: wasmir.InstrF64Const, operandCount: 1, decode: decodeF64ConstOperands},
}

// lowerBySpec lowers pi using loweringSpecs when pi.Name is table-driven.
// It returns true when a table entry exists, including validation failures that
// emit diagnostics.
func (fl *functionLowerer) lowerBySpec(pi *PlainInstr, instrLoc string) bool {
	spec, ok := loweringSpecs[pi.Name]
	if !ok {
		return false
	}
	if len(pi.Operands) != spec.operandCount {
		fl.diagf(instrLoc, "%s expects %s", pi.Name, operandCountText(spec.operandCount))
		return true
	}

	ins := wasmir.Instruction{Kind: spec.kind, SourceLoc: instrLoc}
	if spec.decode != nil && !spec.decode(fl, &ins, pi.Operands) {
		// Current table-driven entries with decode callbacks all consume exactly
		// one operand, so report that operand location.
		fl.diagf(pi.Operands[0].Loc(), "invalid %s operand", pi.Name)
		return true
	}
	fl.emitInstr(ins)
	return true
}

// operandCountText formats operand count in lowering diagnostics.
func operandCountText(count int) string {
	switch count {
	case 0:
		return "no operands"
	case 1:
		return "1 operand"
	default:
		return fmt.Sprintf("%d operands", count)
	}
}

// lowerPlainInstr lowers one plain instruction into fl.body.
func (fl *functionLowerer) lowerPlainInstr(pi *PlainInstr) {
	instrLoc := pi.Loc()
	if fl.lowerBySpec(pi, instrLoc) {
		return
	}

	switch pi.Name {
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
	case "block", "loop":
		if len(pi.Operands) > 1 {
			fl.diagf(instrLoc, "%s expects at most 1 operand", pi.Name)
			return
		}
		kind := wasmir.InstrBlock
		if pi.Name == "loop" {
			kind = wasmir.InstrLoop
		}
		ins := wasmir.Instruction{Kind: kind, SourceLoc: instrLoc}
		if len(pi.Operands) == 1 {
			vt, ok := lowerBlockResultTypeOperand(pi.Operands[0])
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid %s result type", pi.Name)
				return
			}
			ins.BlockHasResult = true
			ins.BlockType = vt
		}
		fl.emitInstr(ins)

	default:
		fl.diagf(instrLoc, "unsupported instruction %q", pi.Name)
	}
}

// decodeLocalGetOperands decodes operands into ins.LocalIndex for local.get.
func decodeLocalGetOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	localIndex, ok := lowerLocalIndexOperand(operands[0], fl.localsByName)
	if !ok {
		return false
	}
	ins.LocalIndex = localIndex
	return true
}

// decodeLocalSetOperands decodes operands into ins.LocalIndex for local.set.
func decodeLocalSetOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	localIndex, ok := lowerLocalIndexOperand(operands[0], fl.localsByName)
	if !ok {
		return false
	}
	ins.LocalIndex = localIndex
	return true
}

// decodeBrOperands decodes operands into ins.BranchDepth for br and br_if.
func decodeBrOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	depth, ok := fl.lowerLabelOperand(operands[0])
	if !ok {
		return false
	}
	ins.BranchDepth = depth
	return true
}

// decodeCallOperands decodes operands into ins.FuncIndex for call.
func decodeCallOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	funcIndex, ok := lowerFuncIndexOperand(operands[0], fl.mod.funcsByName)
	if !ok {
		return false
	}
	ins.FuncIndex = funcIndex
	return true
}

// decodeI32ConstOperands decodes operands into ins.I32Const for i32.const.
func decodeI32ConstOperands(_ *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	imm, ok := lowerI32ConstOperand(operands[0])
	if !ok {
		return false
	}
	ins.I32Const = imm
	return true
}

// decodeI64ConstOperands decodes operands into ins.I64Const for i64.const.
func decodeI64ConstOperands(_ *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	imm, ok := lowerI64ConstOperand(operands[0])
	if !ok {
		return false
	}
	ins.I64Const = imm
	return true
}

// decodeF32ConstOperands decodes operands into ins.F32Const for f32.const.
func decodeF32ConstOperands(_ *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	imm, ok := lowerF32ConstOperand(operands[0])
	if !ok {
		return false
	}
	ins.F32Const = imm
	return true
}

// decodeF64ConstOperands decodes operands into ins.F64Const for f64.const.
func decodeF64ConstOperands(_ *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	imm, ok := lowerF64ConstOperand(operands[0])
	if !ok {
		return false
	}
	ins.F64Const = imm
	return true
}

// emitInstr appends one lowered instruction to the current function body.
func (fl *functionLowerer) emitInstr(instr wasmir.Instruction) {
	fl.body = append(fl.body, instr)
}

// pushLabel pushes one active structured control label.
func (fl *functionLowerer) pushLabel(name string) {
	fl.labelStack = append(fl.labelStack, labelScope{name: name})
}

// popLabel pops one active structured control label.
func (fl *functionLowerer) popLabel() {
	if len(fl.labelStack) == 0 {
		return
	}
	fl.labelStack = fl.labelStack[:len(fl.labelStack)-1]
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

// lowerLabelOperand resolves op as a branch label depth.
// Numeric labels are interpreted directly as depths.
// Identifier labels are resolved from innermost to outermost active labels.
func (fl *functionLowerer) lowerLabelOperand(op Operand) (uint32, bool) {
	switch o := op.(type) {
	case *IntOperand:
		return parseU32Literal(o.Value)
	case *IdOperand:
		for i := len(fl.labelStack) - 1; i >= 0; i-- {
			if fl.labelStack[i].name == o.Value {
				return uint32(len(fl.labelStack) - 1 - i), true
			}
		}
		return 0, false
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
