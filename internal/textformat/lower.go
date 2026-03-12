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

	// typesByName maps type identifiers to type indices in out.Types.
	typesByName map[string]uint32

	// globalsByName maps global identifiers to their indices in out.Globals.
	globalsByName map[string]uint32
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
		out:           &wasmir.Module{},
		funcsByName:   map[string]uint32{},
		typesByName:   map[string]uint32{},
		globalsByName: map[string]uint32{},
	}
}

// lowerModule lowers all functions in astm into l.out and accumulates
// diagnostics in l.diags.
func (l *moduleLowerer) lowerModule(astm *Module) {
	l.collectTypeDecls(astm)
	l.collectFunctionNames(astm)
	l.collectTableDecls(astm)
	l.collectMemoryDecls(astm)
	l.collectGlobalDecls(astm)
	for i, f := range astm.Funcs {
		if f == nil {
			l.diags.Addf("func[%d]: nil function", i)
			continue
		}
		l.lowerFunction(i, f)
	}
}

// collectTypeDecls lowers module-level type declarations and records named
// type indices for later function type-use resolution.
func (l *moduleLowerer) collectTypeDecls(astm *Module) {
	for i, td := range astm.Types {
		if td == nil || td.TyUse == nil {
			l.diags.Addf("type[%d]: nil type declaration", i)
			continue
		}
		params := lowerTypeParams(td.TyUse.Params, i, &l.diags)
		results := lowerTypeResults(td.TyUse.Results, i, &l.diags)
		typeIdx := uint32(len(l.out.Types))
		l.out.Types = append(l.out.Types, wasmir.FuncType{
			Params:  params,
			Results: results,
		})
		if td.Id == "" {
			continue
		}
		if prev, exists := l.typesByName[td.Id]; exists {
			l.diags.Addf("type[%d] %s: duplicate type id (first seen at type[%d])", i, td.Id, prev)
			continue
		}
		l.typesByName[td.Id] = typeIdx
	}
}

// collectTableDecls lowers table declarations and their inline element lists.
func (l *moduleLowerer) collectTableDecls(astm *Module) {
	for i, td := range astm.Tables {
		if td == nil {
			l.diags.Addf("table[%d]: nil table declaration", i)
			continue
		}
		tableIdx := uint32(len(l.out.Tables))
		min := uint32(len(td.ElemRefs))
		l.out.Tables = append(l.out.Tables, wasmir.Table{Min: min})

		if len(td.ElemRefs) == 0 {
			continue
		}
		seg := wasmir.ElementSegment{
			TableIndex:  tableIdx,
			OffsetI32:   0,
			FuncIndices: make([]uint32, 0, len(td.ElemRefs)),
		}
		for _, ref := range td.ElemRefs {
			idx, ok := l.resolveFunctionRef(ref)
			if !ok {
				l.diags.Addf("table[%d]: unknown elem function ref %q", i, ref)
				continue
			}
			seg.FuncIndices = append(seg.FuncIndices, idx)
		}
		l.out.Elements = append(l.out.Elements, seg)
	}
}

// collectMemoryDecls lowers memory declarations.
func (l *moduleLowerer) collectMemoryDecls(astm *Module) {
	for i, md := range astm.Memories {
		if md == nil {
			l.diags.Addf("memory[%d]: nil memory declaration", i)
			continue
		}
		l.out.Memories = append(l.out.Memories, wasmir.Memory{Min: md.Min})
	}
}

// collectGlobalDecls lowers global declarations and records named globals.
func (l *moduleLowerer) collectGlobalDecls(astm *Module) {
	for i, gd := range astm.Globals {
		if gd == nil {
			l.diags.Addf("global[%d]: nil global declaration", i)
			continue
		}
		vt, ok := lowerValueType(gd.Ty)
		if !ok {
			l.diags.Addf("global[%d]: unsupported value type %q", i, gd.Ty)
			continue
		}
		init, ok := lowerGlobalInit(gd.Init)
		if !ok {
			l.diags.Addf("global[%d]: unsupported initializer", i)
			continue
		}
		globalIdx := uint32(len(l.out.Globals))
		l.out.Globals = append(l.out.Globals, wasmir.Global{
			Name:    gd.Id,
			Type:    vt,
			Mutable: gd.Mutable,
			Init:    init,
		})
		if gd.Id == "" {
			continue
		}
		if prev, exists := l.globalsByName[gd.Id]; exists {
			l.diags.Addf("global[%d] %s: duplicate global id (first seen at global[%d])", i, gd.Id, prev)
			continue
		}
		l.globalsByName[gd.Id] = globalIdx
	}
}

func (l *moduleLowerer) resolveFunctionRef(ref string) (uint32, bool) {
	if idx, ok := l.funcsByName[ref]; ok {
		return idx, true
	}
	return parseU32Literal(ref)
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

// internFuncType returns the index of a function type with the given signature.
//
// If an identical signature already exists in l.out.Types, its existing index is
// returned. Otherwise a new type is appended and its new index is returned.
func (l *moduleLowerer) internFuncType(params []wasmir.ValueType, results []wasmir.ValueType) uint32 {
	for i, ft := range l.out.Types {
		if equalValueTypeSlices(ft.Params, params) && equalValueTypeSlices(ft.Results, results) {
			return uint32(i)
		}
	}
	typeIdx := uint32(len(l.out.Types))
	l.out.Types = append(l.out.Types, wasmir.FuncType{
		Params:  params,
		Results: results,
	})
	return typeIdx
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
	typeIdx := fl.lowerTypeUse()
	fl.lowerLocals()

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
func (fl *functionLowerer) lowerTypeUse() uint32 {
	if fl.fn.TyUse == nil {
		fl.diagf(fl.fn.loc.String(), "missing function type use")
		typeIdx := uint32(len(fl.mod.out.Types))
		fl.mod.out.Types = append(fl.mod.out.Types, wasmir.FuncType{})
		return typeIdx
	}

	fl.lowerParams(fl.fn.TyUse.Params)
	fl.lowerResults(fl.fn.TyUse.Results)

	if fl.fn.TyUse.Id == "" {
		return fl.mod.internFuncType(fl.params, fl.results)
	}

	refIdx, refType, ok := fl.resolveTypeRef(fl.fn.TyUse.Id)
	if !ok {
		fl.diagf(fl.fn.loc.String(), "unknown type use %q", fl.fn.TyUse.Id)
		return fl.mod.internFuncType(fl.params, fl.results)
	}

	// If no inline param/result declarations exist, inherit signature directly
	// from the referenced type for validation and local-index accounting.
	if len(fl.params) == 0 && len(fl.results) == 0 {
		fl.params = append(fl.params, refType.Params...)
		fl.results = append(fl.results, refType.Results...)
		fl.paramNames = make([]string, len(refType.Params))
		fl.nextLocalIndex = uint32(len(refType.Params))
		return refIdx
	}

	if !equalValueTypeSlices(fl.params, refType.Params) {
		fl.diagf(fl.fn.loc.String(), "type use %q parameter types mismatch referenced type", fl.fn.TyUse.Id)
	}
	if !equalValueTypeSlices(fl.results, refType.Results) {
		fl.diagf(fl.fn.loc.String(), "type use %q result types mismatch referenced type", fl.fn.TyUse.Id)
	}
	return refIdx
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
	if fi.Name == "call_indirect" {
		fl.lowerFoldedCallIndirect(fi)
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

// lowerFoldedCallIndirect lowers folded "(call_indirect ...)" preserving
// operand evaluation order for nested argument expressions.
func (fl *functionLowerer) lowerFoldedCallIndirect(fi *FoldedInstr) {
	var typeRef string
	for _, arg := range fi.Args {
		if arg.Operand != nil {
			fl.diagf(arg.Operand.Loc(), "call_indirect expects nested expressions/clauses")
			continue
		}
		nested, ok := arg.Instr.(*FoldedInstr)
		if !ok {
			fl.lowerInstruction(arg.Instr)
			continue
		}
		if nested.Name == "type" {
			if typeRef != "" {
				fl.diagf(nested.Loc(), "duplicate call_indirect type clause")
				continue
			}
			ref, ok := parseFoldedTypeClauseRef(nested)
			if !ok {
				fl.diagf(nested.Loc(), "invalid call_indirect type clause")
				continue
			}
			typeRef = ref
			continue
		}
		fl.lowerInstruction(nested)
	}
	if typeRef == "" {
		fl.diagf(fi.Loc(), "call_indirect requires a (type ...) clause")
		return
	}
	typeIdx, _, ok := fl.resolveTypeRef(typeRef)
	if !ok {
		fl.diagf(fi.Loc(), "unknown call_indirect type use %q", typeRef)
		return
	}
	fl.emitInstr(wasmir.Instruction{
		Kind:          wasmir.InstrCallIndirect,
		CallTypeIndex: typeIdx,
		TableIndex:    0,
		SourceLoc:     fi.loc.String(),
	})
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
	var typeRef string
	seenResultClause := false
	seenBody := false

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
			seenBody = true
			continue
		}

		switch nested.Name {
		case "type":
			if seenBody || len(paramTypes) > 0 || len(resultTypes) > 0 || typeRef != "" {
				fl.diagf(nested.Loc(), "unexpected token in %s signature", fi.Name)
				continue
			}
			ref, ok := parseFoldedTypeClauseRef(nested)
			if !ok {
				fl.diagf(nested.Loc(), "invalid %s type clause", fi.Name)
				continue
			}
			typeRef = ref
		case "result":
			if seenBody {
				fl.diagf(nested.Loc(), "unexpected token in %s body", fi.Name)
				continue
			}
			// Result annotation in text:
			//   (result t1 t2 ...)
			// For this lowering pass we allow at most one explicit result
			// clause and collect all listed result value types.
			if len(nested.Args) == 0 {
				fl.diagf(nested.Loc(), "invalid %s result clause", fi.Name)
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
			seenResultClause = true
		case "param":
			if seenBody || seenResultClause {
				fl.diagf(nested.Loc(), "unexpected token in %s signature", fi.Name)
				continue
			}
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
				if _, isID := paramArg.Operand.(*IdOperand); isID {
					fl.diagf(paramArg.Operand.Loc(), "named %s params are not supported", fi.Name)
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
			seenBody = true
		}
	}

	kind := wasmir.InstrBlock
	if isLoop {
		kind = wasmir.InstrLoop
	}

	finalParams := paramTypes
	finalResults := resultTypes
	useTypeIndex := false
	var typeIdx uint32

	if typeRef != "" {
		refIdx, refType, ok := fl.resolveTypeRef(typeRef)
		if !ok {
			fl.diagf(fi.Loc(), "unknown %s type use %q", fi.Name, typeRef)
		} else {
			useTypeIndex = true
			typeIdx = refIdx
			if len(paramTypes) > 0 || len(resultTypes) > 0 {
				if !equalValueTypeSlices(paramTypes, refType.Params) || !equalValueTypeSlices(resultTypes, refType.Results) {
					fl.diagf(fi.Loc(), "inline function type mismatch in %s", fi.Name)
				}
			} else {
				finalParams = append([]wasmir.ValueType(nil), refType.Params...)
				finalResults = append([]wasmir.ValueType(nil), refType.Results...)
			}
		}
	}

	switch {
	case useTypeIndex:
		fl.emitInstr(wasmir.Instruction{
			Kind:               kind,
			BlockTypeUsesIndex: true,
			BlockTypeIndex:     typeIdx,
			SourceLoc:          fi.loc.String(),
		})
	case len(finalParams) > 0 || len(finalResults) > 1:
		// Multi-value signatures (or any explicit params) require a type-index
		// blocktype per the binary format. We append a synthetic function type
		// to Module.Types and reference it from the instruction.
		typeIdx := fl.mod.internFuncType(finalParams, finalResults)
		fl.emitInstr(wasmir.Instruction{
			Kind:               kind,
			BlockTypeUsesIndex: true,
			BlockTypeIndex:     typeIdx,
			SourceLoc:          fi.loc.String(),
		})
	case len(finalResults) == 1:
		// Single-result blocktype can be encoded directly as a value type.
		fl.emitInstr(wasmir.Instruction{
			Kind:           kind,
			BlockHasResult: true,
			BlockType:      finalResults[0],
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

func parseFoldedTypeClauseRef(fi *FoldedInstr) (string, bool) {
	if fi == nil || fi.Name != "type" || len(fi.Args) != 1 {
		return "", false
	}
	if fi.Args[0].Instr != nil || fi.Args[0].Operand == nil {
		return "", false
	}
	switch op := fi.Args[0].Operand.(type) {
	case *IdOperand:
		return op.Value, true
	case *IntOperand:
		return op.Value, true
	default:
		return "", false
	}
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
	"nop":              {kind: wasmir.InstrNop, operandCount: 0},
	"else":             {kind: wasmir.InstrElse, operandCount: 0},
	"end":              {kind: wasmir.InstrEnd, operandCount: 0},
	"drop":             {kind: wasmir.InstrDrop, operandCount: 0},
	"select":           {kind: wasmir.InstrSelect, operandCount: 0},
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
	"i32.eqz":          {kind: wasmir.InstrI32Eqz, operandCount: 0},
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
	"f32.neg":          {kind: wasmir.InstrF32Neg, operandCount: 0},
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
	"f64.neg":          {kind: wasmir.InstrF64Neg, operandCount: 0},
	"f64.min":          {kind: wasmir.InstrF64Min, operandCount: 0},
	"f64.max":          {kind: wasmir.InstrF64Max, operandCount: 0},
	"f64.ceil":         {kind: wasmir.InstrF64Ceil, operandCount: 0},
	"f64.floor":        {kind: wasmir.InstrF64Floor, operandCount: 0},
	"f64.trunc":        {kind: wasmir.InstrF64Trunc, operandCount: 0},
	"f64.nearest":      {kind: wasmir.InstrF64Nearest, operandCount: 0},
	"local.get":        {kind: wasmir.InstrLocalGet, operandCount: 1, decode: decodeLocalGetOperands},
	"local.set":        {kind: wasmir.InstrLocalSet, operandCount: 1, decode: decodeLocalSetOperands},
	"local.tee":        {kind: wasmir.InstrLocalTee, operandCount: 1, decode: decodeLocalTeeOperands},
	"call":             {kind: wasmir.InstrCall, operandCount: 1, decode: decodeCallOperands},
	"br":               {kind: wasmir.InstrBr, operandCount: 1, decode: decodeBrOperands},
	"br_if":            {kind: wasmir.InstrBrIf, operandCount: 1, decode: decodeBrOperands},
	"global.get":       {kind: wasmir.InstrGlobalGet, operandCount: 1, decode: decodeGlobalGetOperands},
	"global.set":       {kind: wasmir.InstrGlobalSet, operandCount: 1, decode: decodeGlobalSetOperands},
	"i32.load":         {kind: wasmir.InstrI32Load, operandCount: 0},
	"i32.store":        {kind: wasmir.InstrI32Store, operandCount: 0},
	"memory.grow":      {kind: wasmir.InstrMemoryGrow, operandCount: 0},
	"unreachable":      {kind: wasmir.InstrUnreachable, operandCount: 0},
	"return":           {kind: wasmir.InstrReturn, operandCount: 0},
	"i32.eq":           {kind: wasmir.InstrI32Eq, operandCount: 0},
	"i32.ctz":          {kind: wasmir.InstrI32Ctz, operandCount: 0},
	"f32.gt":           {kind: wasmir.InstrF32Gt, operandCount: 0},
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
	case "br_table":
		if len(pi.Operands) == 0 {
			fl.diagf(instrLoc, "br_table expects at least 1 label operand")
			return
		}
		depths := make([]uint32, 0, len(pi.Operands))
		for i, op := range pi.Operands {
			depth, ok := fl.lowerLabelOperand(op)
			if !ok {
				fl.diagf(op.Loc(), "invalid br_table label operand %d", i)
				return
			}
			depths = append(depths, depth)
		}
		ins := wasmir.Instruction{
			Kind:          wasmir.InstrBrTable,
			BranchDefault: depths[len(depths)-1],
			SourceLoc:     instrLoc,
		}
		if len(depths) > 1 {
			ins.BranchTable = append(ins.BranchTable, depths[:len(depths)-1]...)
		}
		fl.emitInstr(ins)
		return
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

// decodeLocalTeeOperands decodes operands into ins.LocalIndex for local.tee.
func decodeLocalTeeOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
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

// decodeGlobalGetOperands decodes operands into ins.GlobalIndex for global.get.
func decodeGlobalGetOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	globalIndex, ok := lowerGlobalIndexOperand(operands[0], fl.mod.globalsByName)
	if !ok {
		return false
	}
	ins.GlobalIndex = globalIndex
	return true
}

// decodeGlobalSetOperands decodes operands into ins.GlobalIndex for global.set.
func decodeGlobalSetOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	globalIndex, ok := lowerGlobalIndexOperand(operands[0], fl.mod.globalsByName)
	if !ok {
		return false
	}
	ins.GlobalIndex = globalIndex
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

// lowerGlobalInit lowers a global initializer expression to a semantic const
// instruction.
func lowerGlobalInit(init Instruction) (wasmir.Instruction, bool) {
	var name string
	var op Operand
	switch in := init.(type) {
	case *PlainInstr:
		if len(in.Operands) != 1 {
			return wasmir.Instruction{}, false
		}
		name = in.Name
		op = in.Operands[0]
	case *FoldedInstr:
		if len(in.Args) != 1 || in.Args[0].Instr != nil || in.Args[0].Operand == nil {
			return wasmir.Instruction{}, false
		}
		name = in.Name
		op = in.Args[0].Operand
	default:
		return wasmir.Instruction{}, false
	}

	switch name {
	case "i32.const":
		imm, ok := lowerI32ConstOperand(op)
		if !ok {
			return wasmir.Instruction{}, false
		}
		return wasmir.Instruction{Kind: wasmir.InstrI32Const, I32Const: imm}, true
	case "i64.const":
		imm, ok := lowerI64ConstOperand(op)
		if !ok {
			return wasmir.Instruction{}, false
		}
		return wasmir.Instruction{Kind: wasmir.InstrI64Const, I64Const: imm}, true
	case "f32.const":
		imm, ok := lowerF32ConstOperand(op)
		if !ok {
			return wasmir.Instruction{}, false
		}
		return wasmir.Instruction{Kind: wasmir.InstrF32Const, F32Const: imm}, true
	case "f64.const":
		imm, ok := lowerF64ConstOperand(op)
		if !ok {
			return wasmir.Instruction{}, false
		}
		return wasmir.Instruction{Kind: wasmir.InstrF64Const, F64Const: imm}, true
	default:
		return wasmir.Instruction{}, false
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

// lowerGlobalIndexOperand resolves op as a global index using globalsByName.
// It returns the resolved index and true on success, or 0/false otherwise.
func lowerGlobalIndexOperand(op Operand, globalsByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := globalsByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// resolveTypeRef resolves a text type-use reference by identifier or index.
func (fl *functionLowerer) resolveTypeRef(ref string) (uint32, wasmir.FuncType, bool) {
	if idx, ok := fl.mod.typesByName[ref]; ok {
		return idx, fl.mod.out.Types[idx], true
	}
	if idx, ok := parseU32Literal(ref); ok {
		if int(idx) < len(fl.mod.out.Types) {
			return idx, fl.mod.out.Types[idx], true
		}
	}
	return 0, wasmir.FuncType{}, false
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

func lowerTypeParams(params []*ParamDecl, typeIdx int, diags *diag.ErrorList) []wasmir.ValueType {
	out := make([]wasmir.ValueType, 0, len(params))
	for i, pd := range params {
		if pd == nil {
			diags.Addf("type[%d] param[%d]: nil param declaration", typeIdx, i)
			continue
		}
		vt, ok := lowerValueType(pd.Ty)
		if !ok {
			diags.Addf("type[%d] param[%d]: unsupported param type %q", typeIdx, i, pd.Ty)
			continue
		}
		out = append(out, vt)
	}
	return out
}

func lowerTypeResults(results []*ResultDecl, typeIdx int, diags *diag.ErrorList) []wasmir.ValueType {
	out := make([]wasmir.ValueType, 0, len(results))
	for i, rd := range results {
		if rd == nil {
			diags.Addf("type[%d] result[%d]: nil result declaration", typeIdx, i)
			continue
		}
		vt, ok := lowerValueType(rd.Ty)
		if !ok {
			diags.Addf("type[%d] result[%d]: unsupported result type %q", typeIdx, i, rd.Ty)
			continue
		}
		out = append(out, vt)
	}
	return out
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
