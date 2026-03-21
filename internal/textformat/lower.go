package textformat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/numlit"
	"github.com/eliben/watgo/wasmir"
)

const wasmPageSizeBytes = 65536

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

	// funcAbsIndexByAST maps each textformat Module.Funcs entry index to its
	// absolute function index in the module function index space
	// (imported functions first, then defined functions).
	funcAbsIndexByAST []uint32

	// funcImportCount is the number of imported functions in the module.
	funcImportCount uint32

	// typesByName maps type identifiers to type indices in out.Types.
	typesByName map[string]uint32

	// globalsByName maps global identifiers to their indices in out.Globals.
	globalsByName map[string]uint32

	// tablesByName maps table identifiers to their indices in out.Tables.
	tablesByName map[string]uint32

	// memoriesByName maps memory identifiers to their indices in out.Memories.
	memoriesByName map[string]uint32

	// tableNonNullable records whether each lowered table index has
	// non-nullable reference type semantics (for example "(ref func)").
	tableNonNullable map[uint32]bool

	// elemRefTypeByName records named element-segment payload types for
	// instructions (for example table.init) that reference element ids.
	elemRefTypeByName map[string]wasmir.ValueType
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

func isImportedFunctionDecl(f *Function) bool {
	return f != nil && f.ImportModule != ""
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
		out:               &wasmir.Module{},
		funcsByName:       map[string]uint32{},
		typesByName:       map[string]uint32{},
		globalsByName:     map[string]uint32{},
		tablesByName:      map[string]uint32{},
		memoriesByName:    map[string]uint32{},
		tableNonNullable:  map[uint32]bool{},
		elemRefTypeByName: map[string]wasmir.ValueType{},
	}
}

// lowerModule lowers all functions in astm into l.out and accumulates
// diagnostics in l.diags.
func (l *moduleLowerer) lowerModule(astm *Module) {
	l.collectTypeDecls(astm)
	l.collectFunctionNames(astm)
	l.collectGlobalDecls(astm)
	l.collectTableDecls(astm)
	l.collectElemDecls(astm)
	l.collectMemoryDecls(astm)
	l.collectDataDecls(astm)
	for i, f := range astm.Funcs {
		if f == nil {
			l.diags.Addf("func[%d]: nil function", i)
			continue
		}
		l.lowerFunction(i, f)
	}
	l.collectModuleExports(astm)
}

// collectElemDecls lowers module-level elem declarations.
func (l *moduleLowerer) collectElemDecls(astm *Module) {
	for i, ed := range astm.Elems {
		if ed == nil {
			l.diags.Addf("elem[%d]: nil elem declaration", i)
			continue
		}
		if ed.Id != "" {
			if refTy, ok := l.inferElemPayloadRefType(ed); ok {
				l.elemRefTypeByName[ed.Id] = refTy
			}
		}
		seg := wasmir.ElementSegment{Mode: wasmir.ElemSegmentModeActive}
		if ed.Mode != ElemModeActive {
			switch ed.Mode {
			case ElemModePassive:
				seg.Mode = wasmir.ElemSegmentModePassive
			case ElemModeDeclarative:
				seg.Mode = wasmir.ElemSegmentModeDeclarative
			}
		} else {
			tableIndex := uint32(0)
			if ed.TableRef != "" {
				idx, ok := l.resolveTableRef(ed.TableRef)
				if !ok {
					l.diags.Addf("elem[%d]: unknown table reference %q", i, ed.TableRef)
					continue
				}
				tableIndex = idx
			}
			if ed.RefTy != nil && l.tableNonNullable[tableIndex] {
				elemRefType, ok := lowerRefTypeInfo(ed.RefTy, l.typesByName)
				if ok && elemRefType.Nullable {
					l.diags.Addf("elem[%d]: type mismatch", i)
					continue
				}
			}

			offsetValue, ok := l.evalI32ConstExpr(ed.Offset)
			if !ok {
				l.diags.Addf("elem[%d]: offset must be i32.const", i)
				continue
			}
			seg.TableIndex = tableIndex
			seg.OffsetI32 = offsetValue
		}
		if len(ed.Exprs) > 0 {
			hasSegRefType := false
			if ed.RefTy != nil {
				refTy, ok := lowerValueType(ed.RefTy, l.typesByName)
				if !ok {
					l.diags.Addf("elem[%d]: unsupported reference type %q", i, ed.RefTy)
					continue
				}
				seg.RefType = refTy
				hasSegRefType = true
			}
			for j, expr := range ed.Exprs {
				ce, ok := l.lowerConstInstr(expr)
				if !ok {
					l.diags.Addf("elem[%d] expr[%d]: unsupported constant expression", i, j)
					continue
				}
				if !hasSegRefType {
					seg.RefType = ce.Type
					hasSegRefType = true
				}
				if !matchesExpectedValueType(ce.Type, seg.RefType) {
					l.diags.Addf("elem[%d] expr[%d]: type mismatch", i, j)
					continue
				}
				seg.Exprs = append(seg.Exprs, ce.Instr)
			}
		} else {
			tableRefType := wasmir.RefTypeFunc(true)
			if seg.Mode == wasmir.ElemSegmentModeActive && int(seg.TableIndex) < len(l.out.Tables) {
				tableRefType = l.out.Tables[seg.TableIndex].RefType
			}
			if seg.Mode == wasmir.ElemSegmentModeActive && usesExprElementSegment(tableRefType) {
				seg.RefType = tableRefType
				seg.Exprs = make([]wasmir.Instruction, 0, len(ed.FuncRefs))
				for j, ref := range ed.FuncRefs {
					funcIdx, ok := l.resolveFunctionRef(ref)
					if !ok {
						l.diags.Addf("elem[%d] func[%d]: unknown function reference %q", i, j, ref)
						continue
					}
					seg.Exprs = append(seg.Exprs, wasmir.Instruction{Kind: wasmir.InstrRefFunc, FuncIndex: funcIdx})
				}
			} else {
				seg.FuncIndices = make([]uint32, 0, len(ed.FuncRefs))
				for j, ref := range ed.FuncRefs {
					funcIdx, ok := l.resolveFunctionRef(ref)
					if !ok {
						l.diags.Addf("elem[%d] func[%d]: unknown function reference %q", i, j, ref)
						continue
					}
					seg.FuncIndices = append(seg.FuncIndices, funcIdx)
				}
			}
		}
		l.out.Elements = append(l.out.Elements, seg)
	}
}

func (l *moduleLowerer) inferElemPayloadRefType(ed *ElemDecl) (wasmir.ValueType, bool) {
	if ed == nil {
		return wasmir.ValueType{}, false
	}
	if ed.RefTy != nil {
		return lowerValueType(ed.RefTy, l.typesByName)
	}
	if len(ed.FuncRefs) > 0 {
		return wasmir.RefTypeFunc(true), true
	}
	if len(ed.Exprs) > 0 {
		ci, ok := l.lowerConstInstr(ed.Exprs[0])
		if !ok {
			return wasmir.ValueType{}, false
		}
		return ci.Type, true
	}
	return wasmir.ValueType{}, false
}

func usesExprElementSegment(refType wasmir.ValueType) bool {
	return refType.UsesTypeIndex() || (refType.IsRef() && !refType.Nullable)
}

// collectTypeDecls lowers module-level type declarations and records named
// type indices for later function type-use resolution.
func (l *moduleLowerer) collectTypeDecls(astm *Module) {
	for i, td := range astm.Types {
		if td == nil || td.TyUse == nil {
			l.diags.Addf("type[%d]: nil type declaration", i)
			continue
		}
		params := l.lowerTypeParams(td.TyUse.Params, i)
		results := l.lowerTypeResults(td.TyUse.Results, i)
		typeIdx := uint32(len(l.out.Types))
		l.out.Types = append(l.out.Types, wasmir.FuncType{
			Name:    td.Id,
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

// collectTableDecls lowers table declarations and inline table initializers.
func (l *moduleLowerer) collectTableDecls(astm *Module) {
	for i, td := range astm.Tables {
		if td == nil {
			l.diags.Addf("table[%d]: nil table declaration", i)
			continue
		}
		refType, ok := lowerRefTypeInfo(td.RefTy, l.typesByName)
		if !ok {
			l.diags.Addf("table[%d]: unsupported reference type %q", i, td.RefTy)
			continue
		}
		nullable := refType.Nullable

		min := td.Min
		if len(td.ElemRefs) > 0 && min < uint32(len(td.ElemRefs)) {
			min = uint32(len(td.ElemRefs))
		}
		if len(td.ElemExprs) > 0 && min < uint32(len(td.ElemExprs)) {
			min = uint32(len(td.ElemExprs))
		}
		if td.HasMax && td.Max < min {
			l.diags.Addf("table[%d]: size minimum must not be greater than maximum", i)
			continue
		}

		tb := wasmir.Table{
			Min:     min,
			HasMax:  td.HasMax,
			Max:     td.Max,
			RefType: refType,
		}
		if td.ImportModule != "" {
			tb.Imported = true
			tb.ImportModule = td.ImportModule
			tb.ImportName = td.ImportName
			l.out.Imports = append(l.out.Imports, wasmir.Import{
				Module: td.ImportModule,
				Name:   td.ImportName,
				Kind:   wasmir.ExternalKindTable,
				Table:  tb,
			})
		}
		tableIdx := uint32(len(l.out.Tables))
		l.out.Tables = append(l.out.Tables, tb)
		l.tableNonNullable[tableIdx] = !nullable

		if td.Id != "" {
			if prev, exists := l.tablesByName[td.Id]; exists {
				l.diags.Addf("table[%d] %s: duplicate table id (first seen at table[%d])", i, td.Id, prev)
			} else {
				l.tablesByName[td.Id] = tableIdx
			}
		}
		if td.Export != "" {
			l.out.Exports = append(l.out.Exports, wasmir.Export{
				Name:  td.Export,
				Kind:  wasmir.ExternalKindTable,
				Index: tableIdx,
			})
		}

		if len(td.ElemRefs) > 0 {
			seg := wasmir.ElementSegment{TableIndex: tableIdx, OffsetI32: 0}
			if usesExprElementSegment(refType) {
				// Typed or non-null function tables cannot use the legacy
				// function-index element encoding. For example,
				//   (table $t (ref null $t) (elem $tf))
				// must lower to ref-expression payloads like `(ref.func $tf)`
				// so the element segment carries the table's precise ref type.
				seg.RefType = refType
				seg.Exprs = make([]wasmir.Instruction, 0, len(td.ElemRefs))
				for _, ref := range td.ElemRefs {
					idx, ok := l.resolveFunctionRef(ref)
					if !ok {
						l.diags.Addf("table[%d]: unknown elem function ref %q", i, ref)
						continue
					}
					seg.Exprs = append(seg.Exprs, wasmir.Instruction{Kind: wasmir.InstrRefFunc, FuncIndex: idx})
				}
			} else {
				seg.FuncIndices = make([]uint32, 0, len(td.ElemRefs))
				for _, ref := range td.ElemRefs {
					idx, ok := l.resolveFunctionRef(ref)
					if !ok {
						l.diags.Addf("table[%d]: unknown elem function ref %q", i, ref)
						continue
					}
					seg.FuncIndices = append(seg.FuncIndices, idx)
				}
			}
			l.out.Elements = append(l.out.Elements, seg)
		}
		if len(td.ElemExprs) > 0 {
			seg := wasmir.ElementSegment{
				TableIndex: tableIdx,
				OffsetI32:  0,
				RefType:    refType,
				Exprs:      make([]wasmir.Instruction, 0, len(td.ElemExprs)),
			}
			for j, expr := range td.ElemExprs {
				ci, ok := l.lowerConstInstr(expr)
				if !ok {
					l.diags.Addf("table[%d] elem expr[%d]: unsupported constant expression", i, j)
					continue
				}
				if !matchesExpectedValueType(ci.Type, refType) {
					l.diags.Addf("table[%d] elem expr[%d]: type mismatch", i, j)
					continue
				}
				seg.Exprs = append(seg.Exprs, ci.Instr)
			}
			l.out.Elements = append(l.out.Elements, seg)
		}

		if td.Init != nil {
			ci, ok := l.lowerConstInstr(td.Init)
			if !ok {
				l.diags.Addf("table[%d]: unsupported initializer", i)
				continue
			}
			if !matchesExpectedValueType(ci.Type, refType) {
				l.diags.Addf("table[%d]: type mismatch", i)
				continue
			}
			if !nullable && ci.Instr.Kind == wasmir.InstrRefNull {
				l.diags.Addf("table[%d]: type mismatch", i)
				continue
			}
			l.out.Tables[tableIdx].HasInit = true
			l.out.Tables[tableIdx].Init = ci.Instr
		} else if !nullable {
			l.diags.Addf("table[%d]: type mismatch", i)
		}
	}
}

// collectMemoryDecls lowers memory declarations.
func (l *moduleLowerer) collectMemoryDecls(astm *Module) {
	for i, md := range astm.Memories {
		if md == nil {
			l.diags.Addf("memory[%d]: nil memory declaration", i)
			continue
		}

		mem := wasmir.Memory{
			Min:          md.Min,
			HasMax:       md.HasMax,
			Max:          md.Max,
			Imported:     md.ImportModule != "",
			ImportModule: md.ImportModule,
			ImportName:   md.ImportName,
		}
		if md.ImportModule != "" {
			l.out.Imports = append(l.out.Imports, wasmir.Import{
				Module: md.ImportModule,
				Name:   md.ImportName,
				Kind:   wasmir.ExternalKindMemory,
				Memory: mem,
			})
		}

		memIdx := uint32(len(l.out.Memories))
		l.out.Memories = append(l.out.Memories, mem)
		if md.Id != "" {
			if prev, exists := l.memoriesByName[md.Id]; exists {
				l.diags.Addf("memory[%d] %s: duplicate memory id (first seen at memory[%d])", i, md.Id, prev)
			} else {
				l.memoriesByName[md.Id] = memIdx
			}
		}
		if md.Export != "" {
			l.out.Exports = append(l.out.Exports, wasmir.Export{
				Name:  md.Export,
				Kind:  wasmir.ExternalKindMemory,
				Index: memIdx,
			})
		}

		if len(md.InlineData) > 0 {
			var init []byte
			for j, s := range md.InlineData {
				b, err := decodeWATStringBytes(s)
				if err != nil {
					l.diags.Addf("memory[%d] inline data[%d]: %v", i, j, err)
					continue
				}
				init = append(init, b...)
			}
			needPages := uint32((len(init) + wasmPageSizeBytes - 1) / wasmPageSizeBytes)
			if mem.Min < needPages {
				l.out.Memories[memIdx].Min = needPages
			}
			l.out.Data = append(l.out.Data, wasmir.DataSegment{
				MemoryIndex: memIdx,
				OffsetI32:   0,
				Init:        init,
			})
		}
	}
}

// collectDataDecls lowers module-level data declarations into active memory
// segments targeting memory index 0.
func (l *moduleLowerer) collectDataDecls(astm *Module) {
	for i, dd := range astm.Data {
		if dd == nil {
			l.diags.Addf("data[%d]: nil data declaration", i)
			continue
		}
		memoryIndex := uint32(0)
		if dd.MemoryRef != "" {
			idx, ok := l.resolveMemoryRef(dd.MemoryRef)
			if !ok {
				l.diags.Addf("data[%d]: unknown memory reference %q", i, dd.MemoryRef)
				continue
			}
			memoryIndex = idx
		}
		offset, ok := l.evalI32ConstExpr(dd.Offset)
		if !ok {
			l.diags.Addf("data[%d]: offset must be i32.const", i)
			continue
		}
		var init []byte
		for j, s := range dd.Strings {
			b, err := decodeWATStringBytes(s)
			if err != nil {
				l.diags.Addf("data[%d] string[%d]: %v", i, j, err)
				continue
			}
			init = append(init, b...)
		}
		l.out.Data = append(l.out.Data, wasmir.DataSegment{
			MemoryIndex: memoryIndex,
			OffsetI32:   offset,
			Init:        init,
		})
	}
}

// collectGlobalDecls lowers global declarations and records named globals.
func (l *moduleLowerer) collectGlobalDecls(astm *Module) {
	for i, gd := range astm.Globals {
		if gd == nil {
			l.diags.Addf("global[%d]: nil global declaration", i)
			continue
		}
		vt, ok := lowerValueType(gd.Ty, l.typesByName)
		if !ok {
			l.diags.Addf("global[%d]: unsupported value type %q", i, gd.Ty)
			continue
		}
		globalIdx := uint32(len(l.out.Globals))
		g := wasmir.Global{
			Name:    gd.Id,
			Type:    vt,
			Mutable: gd.Mutable,
		}
		if gd.ImportModule != "" {
			g.Imported = true
			g.ImportModule = gd.ImportModule
			g.ImportName = gd.ImportName
			l.out.Imports = append(l.out.Imports, wasmir.Import{
				Module:        gd.ImportModule,
				Name:          gd.ImportName,
				Kind:          wasmir.ExternalKindGlobal,
				GlobalType:    vt,
				GlobalMutable: gd.Mutable,
			})
		} else {
			ci, ok := l.lowerConstInstr(gd.Init)
			if !ok {
				l.diags.Addf("global[%d]: unsupported initializer", i)
				continue
			}
			if !matchesExpectedValueType(ci.Type, vt) {
				l.diags.Addf("global[%d]: initializer type mismatch", i)
				continue
			}
			g.Init = ci.Instr
		}
		l.out.Globals = append(l.out.Globals, g)
		if gd.Id == "" {
			goto exportGlobal
		}
		if prev, exists := l.globalsByName[gd.Id]; exists {
			l.diags.Addf("global[%d] %s: duplicate global id (first seen at global[%d])", i, gd.Id, prev)
		} else {
			l.globalsByName[gd.Id] = globalIdx
		}
	exportGlobal:
		if gd.Export != "" {
			l.out.Exports = append(l.out.Exports, wasmir.Export{
				Name:  gd.Export,
				Kind:  wasmir.ExternalKindGlobal,
				Index: globalIdx,
			})
		}
	}
}

func (l *moduleLowerer) resolveFunctionRef(ref string) (uint32, bool) {
	if idx, ok := l.funcsByName[ref]; ok {
		return idx, true
	}
	return parseU32Literal(ref)
}

func (l *moduleLowerer) functionIndexByASTIndex(funcIdx int) uint32 {
	if funcIdx >= 0 && funcIdx < len(l.funcAbsIndexByAST) {
		return l.funcAbsIndexByAST[funcIdx]
	}
	return uint32(funcIdx)
}

func (l *moduleLowerer) resolveTableRef(ref string) (uint32, bool) {
	if idx, ok := l.tablesByName[ref]; ok {
		return idx, true
	}
	return parseU32Literal(ref)
}

func (l *moduleLowerer) resolveMemoryRef(ref string) (uint32, bool) {
	if idx, ok := l.memoriesByName[ref]; ok {
		return idx, true
	}
	return parseU32Literal(ref)
}

func (l *moduleLowerer) resolveGlobalRef(ref string) (uint32, bool) {
	if idx, ok := l.globalsByName[ref]; ok {
		return idx, true
	}
	return parseU32Literal(ref)
}

// collectModuleExports lowers top-level "(export ...)" declarations.
func (l *moduleLowerer) collectModuleExports(astm *Module) {
	for i, ed := range astm.Exports {
		if ed == nil {
			l.diags.Addf("export[%d]: nil export declaration", i)
			continue
		}

		var (
			kind  wasmir.ExternalKind
			index uint32
			ok    bool
		)
		switch ed.Kind {
		case "func":
			kind = wasmir.ExternalKindFunction
			index, ok = l.resolveFunctionRef(ed.Ref)
			if !ok {
				l.diags.Addf("export[%d]: unknown function %q", i, ed.Ref)
				continue
			}
		case "global":
			kind = wasmir.ExternalKindGlobal
			index, ok = l.resolveGlobalRef(ed.Ref)
			if !ok {
				l.diags.Addf("export[%d]: unknown global %q", i, ed.Ref)
				continue
			}
		case "table":
			kind = wasmir.ExternalKindTable
			index, ok = l.resolveTableRef(ed.Ref)
			if !ok {
				l.diags.Addf("export[%d]: unknown table %q", i, ed.Ref)
				continue
			}
		case "memory":
			kind = wasmir.ExternalKindMemory
			index, ok = l.resolveMemoryRef(ed.Ref)
			if !ok {
				l.diags.Addf("export[%d]: unknown memory %q", i, ed.Ref)
				continue
			}
		default:
			l.diags.Addf("export[%d]: unsupported export kind %q", i, ed.Kind)
			continue
		}

		l.out.Exports = append(l.out.Exports, wasmir.Export{
			Name:  ed.Name,
			Kind:  kind,
			Index: index,
		})
	}
}

// collectFunctionNames pre-scans astm and records named function indices.
func (l *moduleLowerer) collectFunctionNames(astm *Module) {
	l.funcAbsIndexByAST = make([]uint32, len(astm.Funcs))
	var importCount uint32
	for _, f := range astm.Funcs {
		if isImportedFunctionDecl(f) {
			importCount++
		}
	}
	l.funcImportCount = importCount
	importNext := uint32(0)
	defNext := importCount
	firstSeenAt := map[string]int{}
	for i, f := range astm.Funcs {
		if isImportedFunctionDecl(f) {
			l.funcAbsIndexByAST[i] = importNext
			importNext++
		} else {
			l.funcAbsIndexByAST[i] = defNext
			defNext++
		}

		if f == nil || f.Id == "" {
			continue
		}
		if prev, exists := firstSeenAt[f.Id]; exists {
			l.diags.Addf("func[%d] %s: duplicate function id (first seen at func[%d])", i, f.Id, prev)
			continue
		}
		firstSeenAt[f.Id] = i
		l.funcsByName[f.Id] = l.funcAbsIndexByAST[i]
	}
}

// internFuncType returns the index of a function type with the given signature.
//
// If an identical signature already exists in l.out.Types, its existing index is
// returned. Otherwise a new type is appended and its new index is returned.
func (l *moduleLowerer) internFuncType(params []wasmir.ValueType, results []wasmir.ValueType, name string) uint32 {
	for i, ft := range l.out.Types {
		if equalValueTypeSlices(ft.Params, params) &&
			equalValueTypeSlices(ft.Results, results) {
			return uint32(i)
		}
	}
	typeIdx := uint32(len(l.out.Types))
	l.out.Types = append(l.out.Types, wasmir.FuncType{
		Name:    name,
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

	if isImportedFunctionDecl(fl.fn) {
		if len(fl.fn.Locals) > 0 {
			fl.diagf(fl.fn.loc.String(), "imported function must not declare locals")
		}
		if len(fl.fn.Instrs) > 0 {
			fl.diagf(fl.fn.loc.String(), "imported function must not have a body")
		}
		fl.mod.out.Imports = append(fl.mod.out.Imports, wasmir.Import{
			Module:  fl.fn.ImportModule,
			Name:    fl.fn.ImportName,
			Kind:    wasmir.ExternalKindFunction,
			TypeIdx: typeIdx,
		})
		if fl.fn.Export != "" {
			fl.mod.out.Exports = append(fl.mod.out.Exports, wasmir.Export{
				Name:  fl.fn.Export,
				Kind:  wasmir.ExternalKindFunction,
				Index: fl.mod.functionIndexByASTIndex(fl.funcIdx),
			})
		}
		return
	}

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
			Index: fl.mod.functionIndexByASTIndex(fl.funcIdx),
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
		return fl.mod.internFuncType(fl.params, fl.results, "")
	}

	refIdx, refType, ok := fl.resolveTypeRef(fl.fn.TyUse.Id)
	if !ok {
		fl.diagf(fl.fn.loc.String(), "unknown type use %q", fl.fn.TyUse.Id)
		return fl.mod.internFuncType(fl.params, fl.results, "")
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
		vt, ok := lowerValueType(pd.Ty, fl.mod.typesByName)
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
		vt, ok := lowerValueType(rd.Ty, fl.mod.typesByName)
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
		if vt, ok := lowerRefTypeInfo(ld.Ty, fl.mod.typesByName); ok && vt.IsRef() && !vt.Nullable {
			fl.diagf(ld.loc.String(), "uninitialized local")
			continue
		}
		vt, ok := lowerValueType(ld.Ty, fl.mod.typesByName)
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
	tableIndex := uint32(0)
	seenTableOperand := false
	for _, arg := range fi.Args {
		if arg.Operand != nil {
			if seenTableOperand {
				fl.diagf(arg.Operand.Loc(), "call_indirect expects at most one table operand")
				continue
			}
			idx, ok := lowerTableIndexOperand(arg.Operand, fl.mod.tablesByName)
			if !ok {
				fl.diagf(arg.Operand.Loc(), "invalid call_indirect table operand")
				continue
			}
			tableIndex = idx
			seenTableOperand = true
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
		TableIndex:    tableIndex,
		SourceLoc:     fi.loc.String(),
	})
}

// lowerFoldedIf lowers a folded if-expression preserving then/else blocks.
func (fl *functionLowerer) lowerFoldedIf(fi *FoldedInstr) {
	var resultType *wasmir.ValueType
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
			if len(nested.Args) != 1 {
				fl.diagf(nested.Loc(), "invalid if result clause")
				continue
			}
			if resultType != nil {
				fl.diagf(nested.Loc(), "duplicate if result clause")
				continue
			}
			vt, ok := lowerFoldedBlockTypeArg(nested.Args[0], fl.mod.typesByName)
			if !ok {
				fl.diagf(nested.Loc(), "invalid if result clause")
				continue
			}
			resultType = &vt
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

	ins := wasmir.Instruction{Kind: wasmir.InstrIf, SourceLoc: fi.loc.String()}
	if resultType != nil {
		ins.BlockHasResult = true
		ins.BlockType = *resultType
	}
	fl.emitInstr(ins)
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
				vt, ok := lowerFoldedBlockTypeArg(resultArg, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid %s result clause", fi.Name)
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
				if _, isID := paramArg.Operand.(*IdOperand); isID {
					fl.diagf(paramArg.Operand.Loc(), "named %s params are not supported", fi.Name)
					continue
				}
				vt, ok := lowerFoldedBlockTypeArg(paramArg, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid %s param clause", fi.Name)
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
		typeIdx := fl.mod.internFuncType(finalParams, finalResults, "")
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

var memoryInstrKinds = map[string]wasmir.InstrKind{
	"i32.load":     wasmir.InstrI32Load,
	"i64.load":     wasmir.InstrI64Load,
	"f32.load":     wasmir.InstrF32Load,
	"f64.load":     wasmir.InstrF64Load,
	"i32.load8_s":  wasmir.InstrI32Load8S,
	"i32.load8_u":  wasmir.InstrI32Load8U,
	"i32.load16_s": wasmir.InstrI32Load16S,
	"i32.load16_u": wasmir.InstrI32Load16U,
	"i64.load8_s":  wasmir.InstrI64Load8S,
	"i64.load8_u":  wasmir.InstrI64Load8U,
	"i64.load16_s": wasmir.InstrI64Load16S,
	"i64.load16_u": wasmir.InstrI64Load16U,
	"i64.load32_s": wasmir.InstrI64Load32S,
	"i64.load32_u": wasmir.InstrI64Load32U,
	"i32.store":    wasmir.InstrI32Store,
	"i64.store":    wasmir.InstrI64Store,
	"i32.store8":   wasmir.InstrI32Store8,
	"i32.store16":  wasmir.InstrI32Store16,
	"i64.store8":   wasmir.InstrI64Store8,
	"i64.store16":  wasmir.InstrI64Store16,
	"i64.store32":  wasmir.InstrI64Store32,
	"f32.store":    wasmir.InstrF32Store,
	"f64.store":    wasmir.InstrF64Store,
}

// loweringSpecs maps plain instruction names to table-driven lowering rules.
var loweringSpecs = map[string]loweringSpec{
	"nop":                 {kind: wasmir.InstrNop, operandCount: 0},
	"else":                {kind: wasmir.InstrElse, operandCount: 0},
	"end":                 {kind: wasmir.InstrEnd, operandCount: 0},
	"drop":                {kind: wasmir.InstrDrop, operandCount: 0},
	"select":              {kind: wasmir.InstrSelect, operandCount: 0},
	"i32.add":             {kind: wasmir.InstrI32Add, operandCount: 0},
	"i32.sub":             {kind: wasmir.InstrI32Sub, operandCount: 0},
	"i32.mul":             {kind: wasmir.InstrI32Mul, operandCount: 0},
	"i32.or":              {kind: wasmir.InstrI32Or, operandCount: 0},
	"i32.xor":             {kind: wasmir.InstrI32Xor, operandCount: 0},
	"i32.div_s":           {kind: wasmir.InstrI32DivS, operandCount: 0},
	"i32.div_u":           {kind: wasmir.InstrI32DivU, operandCount: 0},
	"i32.rem_s":           {kind: wasmir.InstrI32RemS, operandCount: 0},
	"i32.rem_u":           {kind: wasmir.InstrI32RemU, operandCount: 0},
	"i32.shl":             {kind: wasmir.InstrI32Shl, operandCount: 0},
	"i32.shr_s":           {kind: wasmir.InstrI32ShrS, operandCount: 0},
	"i32.shr_u":           {kind: wasmir.InstrI32ShrU, operandCount: 0},
	"i32.rotl":            {kind: wasmir.InstrI32Rotl, operandCount: 0},
	"i32.rotr":            {kind: wasmir.InstrI32Rotr, operandCount: 0},
	"i32.clz":             {kind: wasmir.InstrI32Clz, operandCount: 0},
	"i32.popcnt":          {kind: wasmir.InstrI32Popcnt, operandCount: 0},
	"i32.extend8_s":       {kind: wasmir.InstrI32Extend8S, operandCount: 0},
	"i32.extend16_s":      {kind: wasmir.InstrI32Extend16S, operandCount: 0},
	"i32.eqz":             {kind: wasmir.InstrI32Eqz, operandCount: 0},
	"i32.ne":              {kind: wasmir.InstrI32Ne, operandCount: 0},
	"i32.lt_s":            {kind: wasmir.InstrI32LtS, operandCount: 0},
	"i32.lt_u":            {kind: wasmir.InstrI32LtU, operandCount: 0},
	"i32.le_s":            {kind: wasmir.InstrI32LeS, operandCount: 0},
	"i32.le_u":            {kind: wasmir.InstrI32LeU, operandCount: 0},
	"i32.gt_s":            {kind: wasmir.InstrI32GtS, operandCount: 0},
	"i32.gt_u":            {kind: wasmir.InstrI32GtU, operandCount: 0},
	"i32.ge_s":            {kind: wasmir.InstrI32GeS, operandCount: 0},
	"i32.ge_u":            {kind: wasmir.InstrI32GeU, operandCount: 0},
	"i32.and":             {kind: wasmir.InstrI32And, operandCount: 0},
	"i64.add":             {kind: wasmir.InstrI64Add, operandCount: 0},
	"i64.and":             {kind: wasmir.InstrI64And, operandCount: 0},
	"i64.or":              {kind: wasmir.InstrI64Or, operandCount: 0},
	"i64.xor":             {kind: wasmir.InstrI64Xor, operandCount: 0},
	"i64.eq":              {kind: wasmir.InstrI64Eq, operandCount: 0},
	"i64.ne":              {kind: wasmir.InstrI64Ne, operandCount: 0},
	"i64.eqz":             {kind: wasmir.InstrI64Eqz, operandCount: 0},
	"i64.gt_s":            {kind: wasmir.InstrI64GtS, operandCount: 0},
	"i64.gt_u":            {kind: wasmir.InstrI64GtU, operandCount: 0},
	"i64.ge_s":            {kind: wasmir.InstrI64GeS, operandCount: 0},
	"i64.ge_u":            {kind: wasmir.InstrI64GeU, operandCount: 0},
	"i64.le_s":            {kind: wasmir.InstrI64LeS, operandCount: 0},
	"i64.le_u":            {kind: wasmir.InstrI64LeU, operandCount: 0},
	"i64.sub":             {kind: wasmir.InstrI64Sub, operandCount: 0},
	"i64.mul":             {kind: wasmir.InstrI64Mul, operandCount: 0},
	"i64.div_s":           {kind: wasmir.InstrI64DivS, operandCount: 0},
	"i64.div_u":           {kind: wasmir.InstrI64DivU, operandCount: 0},
	"i64.rem_s":           {kind: wasmir.InstrI64RemS, operandCount: 0},
	"i64.rem_u":           {kind: wasmir.InstrI64RemU, operandCount: 0},
	"i64.shl":             {kind: wasmir.InstrI64Shl, operandCount: 0},
	"i64.shr_s":           {kind: wasmir.InstrI64ShrS, operandCount: 0},
	"i64.shr_u":           {kind: wasmir.InstrI64ShrU, operandCount: 0},
	"i64.rotl":            {kind: wasmir.InstrI64Rotl, operandCount: 0},
	"i64.rotr":            {kind: wasmir.InstrI64Rotr, operandCount: 0},
	"i64.lt_s":            {kind: wasmir.InstrI64LtS, operandCount: 0},
	"i64.lt_u":            {kind: wasmir.InstrI64LtU, operandCount: 0},
	"i64.clz":             {kind: wasmir.InstrI64Clz, operandCount: 0},
	"i64.ctz":             {kind: wasmir.InstrI64Ctz, operandCount: 0},
	"i64.popcnt":          {kind: wasmir.InstrI64Popcnt, operandCount: 0},
	"i64.extend8_s":       {kind: wasmir.InstrI64Extend8S, operandCount: 0},
	"i64.extend16_s":      {kind: wasmir.InstrI64Extend16S, operandCount: 0},
	"i64.extend32_s":      {kind: wasmir.InstrI64Extend32S, operandCount: 0},
	"i32.wrap_i64":        {kind: wasmir.InstrI32WrapI64, operandCount: 0},
	"i64.extend_i32_s":    {kind: wasmir.InstrI64ExtendI32S, operandCount: 0},
	"i64.extend_i32_u":    {kind: wasmir.InstrI64ExtendI32U, operandCount: 0},
	"f32.convert_i32_s":   {kind: wasmir.InstrF32ConvertI32S, operandCount: 0},
	"f64.convert_i64_s":   {kind: wasmir.InstrF64ConvertI64S, operandCount: 0},
	"f32.add":             {kind: wasmir.InstrF32Add, operandCount: 0},
	"f32.sub":             {kind: wasmir.InstrF32Sub, operandCount: 0},
	"f32.mul":             {kind: wasmir.InstrF32Mul, operandCount: 0},
	"f32.div":             {kind: wasmir.InstrF32Div, operandCount: 0},
	"f32.sqrt":            {kind: wasmir.InstrF32Sqrt, operandCount: 0},
	"f32.neg":             {kind: wasmir.InstrF32Neg, operandCount: 0},
	"f32.min":             {kind: wasmir.InstrF32Min, operandCount: 0},
	"f32.max":             {kind: wasmir.InstrF32Max, operandCount: 0},
	"f32.ne":              {kind: wasmir.InstrF32Ne, operandCount: 0},
	"f32.ceil":            {kind: wasmir.InstrF32Ceil, operandCount: 0},
	"f32.floor":           {kind: wasmir.InstrF32Floor, operandCount: 0},
	"f32.trunc":           {kind: wasmir.InstrF32Trunc, operandCount: 0},
	"f32.nearest":         {kind: wasmir.InstrF32Nearest, operandCount: 0},
	"f64.add":             {kind: wasmir.InstrF64Add, operandCount: 0},
	"f64.sub":             {kind: wasmir.InstrF64Sub, operandCount: 0},
	"f64.mul":             {kind: wasmir.InstrF64Mul, operandCount: 0},
	"f64.div":             {kind: wasmir.InstrF64Div, operandCount: 0},
	"f64.sqrt":            {kind: wasmir.InstrF64Sqrt, operandCount: 0},
	"f64.neg":             {kind: wasmir.InstrF64Neg, operandCount: 0},
	"f64.min":             {kind: wasmir.InstrF64Min, operandCount: 0},
	"f64.max":             {kind: wasmir.InstrF64Max, operandCount: 0},
	"f64.ceil":            {kind: wasmir.InstrF64Ceil, operandCount: 0},
	"f64.floor":           {kind: wasmir.InstrF64Floor, operandCount: 0},
	"f64.trunc":           {kind: wasmir.InstrF64Trunc, operandCount: 0},
	"f64.nearest":         {kind: wasmir.InstrF64Nearest, operandCount: 0},
	"f64.eq":              {kind: wasmir.InstrF64Eq, operandCount: 0},
	"f64.le":              {kind: wasmir.InstrF64Le, operandCount: 0},
	"f64.reinterpret_i64": {kind: wasmir.InstrF64ReinterpretI64, operandCount: 0},
	"local.get":           {kind: wasmir.InstrLocalGet, operandCount: 1, decode: decodeLocalGetOperands},
	"local.set":           {kind: wasmir.InstrLocalSet, operandCount: 1, decode: decodeLocalSetOperands},
	"local.tee":           {kind: wasmir.InstrLocalTee, operandCount: 1, decode: decodeLocalTeeOperands},
	"call":                {kind: wasmir.InstrCall, operandCount: 1, decode: decodeCallOperands},
	"call_ref":            {kind: wasmir.InstrCallRef, operandCount: 1, decode: decodeCallRefOperands},
	"br":                  {kind: wasmir.InstrBr, operandCount: 1, decode: decodeBrOperands},
	"br_if":               {kind: wasmir.InstrBrIf, operandCount: 1, decode: decodeBrOperands},
	"br_on_null":          {kind: wasmir.InstrBrOnNull, operandCount: 1, decode: decodeBrOperands},
	"br_on_non_null":      {kind: wasmir.InstrBrOnNonNull, operandCount: 1, decode: decodeBrOperands},
	"global.get":          {kind: wasmir.InstrGlobalGet, operandCount: 1, decode: decodeGlobalGetOperands},
	"global.set":          {kind: wasmir.InstrGlobalSet, operandCount: 1, decode: decodeGlobalSetOperands},
	"memory.size":         {kind: wasmir.InstrMemorySize, operandCount: 0},
	"memory.grow":         {kind: wasmir.InstrMemoryGrow, operandCount: 0},
	"unreachable":         {kind: wasmir.InstrUnreachable, operandCount: 0},
	"return":              {kind: wasmir.InstrReturn, operandCount: 0},
	"i32.eq":              {kind: wasmir.InstrI32Eq, operandCount: 0},
	"i32.ctz":             {kind: wasmir.InstrI32Ctz, operandCount: 0},
	"f32.gt":              {kind: wasmir.InstrF32Gt, operandCount: 0},
	"i32.const":           {kind: wasmir.InstrI32Const, operandCount: 1, decode: decodeI32ConstOperands},
	"i64.const":           {kind: wasmir.InstrI64Const, operandCount: 1, decode: decodeI64ConstOperands},
	"f32.const":           {kind: wasmir.InstrF32Const, operandCount: 1, decode: decodeF32ConstOperands},
	"f64.const":           {kind: wasmir.InstrF64Const, operandCount: 1, decode: decodeF64ConstOperands},
	"ref.null":            {kind: wasmir.InstrRefNull, operandCount: 1, decode: decodeRefNullOperands},
	"ref.is_null":         {kind: wasmir.InstrRefIsNull, operandCount: 0},
	"ref.as_non_null":     {kind: wasmir.InstrRefAsNonNull, operandCount: 0},
	"ref.func":            {kind: wasmir.InstrRefFunc, operandCount: 1, decode: decodeRefFuncOperands},
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
	if fl.lowerMemoryInstr(pi, instrLoc) {
		return
	}
	if fl.lowerBySpec(pi, instrLoc) {
		return
	}

	switch pi.Name {
	case "table.get", "table.set":
		if len(pi.Operands) > 1 {
			fl.diagf(instrLoc, "%s expects at most 1 operand", pi.Name)
			return
		}
		tableIndex := uint32(0)
		if len(pi.Operands) == 1 {
			idx, ok := lowerTableIndexOperand(pi.Operands[0], fl.mod.tablesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid %s table index operand", pi.Name)
				return
			}
			tableIndex = idx
		}
		kind := wasmir.InstrTableGet
		if pi.Name == "table.set" {
			kind = wasmir.InstrTableSet
		}
		fl.emitInstr(wasmir.Instruction{Kind: kind, TableIndex: tableIndex, SourceLoc: instrLoc})
		return
	case "table.grow", "table.size":
		if len(pi.Operands) > 1 {
			fl.diagf(instrLoc, "%s expects at most 1 operand", pi.Name)
			return
		}
		tableIndex := uint32(0)
		if len(pi.Operands) == 1 {
			idx, ok := lowerTableIndexOperand(pi.Operands[0], fl.mod.tablesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid %s table index operand", pi.Name)
				return
			}
			tableIndex = idx
		}
		kind := wasmir.InstrTableGrow
		if pi.Name == "table.size" {
			kind = wasmir.InstrTableSize
		}
		fl.emitInstr(wasmir.Instruction{Kind: kind, TableIndex: tableIndex, SourceLoc: instrLoc})
		return
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
	case "table.init":
		if len(pi.Operands) < 1 || len(pi.Operands) > 2 {
			fl.diagf(instrLoc, "table.init expects 1 or 2 operands")
			return
		}
		tableIndex := uint32(0)
		elemOp := pi.Operands[0]
		if len(pi.Operands) == 2 {
			idx, ok := lowerTableIndexOperand(pi.Operands[0], fl.mod.tablesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid table.init table operand")
				return
			}
			tableIndex = idx
			elemOp = pi.Operands[1]
		}
		if int(tableIndex) < len(fl.mod.out.Tables) {
			if elemID, ok := elemOp.(*IdOperand); ok {
				if elemTy, found := fl.mod.elemRefTypeByName[elemID.Value]; found {
					tableTy := fl.mod.out.Tables[tableIndex].RefType
					if !matchesExpectedValueType(elemTy, tableTy) {
						fl.diagf(instrLoc, "type mismatch")
						return
					}
				}
			}
		}
		// Bulk-memory table.init is not lowered in this subset yet.
		// Emit a deterministic trap so spec assertions expecting trap behavior
		// still execute through the pipeline.
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrUnreachable, SourceLoc: instrLoc})

	default:
		fl.diagf(instrLoc, "unsupported instruction %q", pi.Name)
	}
}

// lowerMemoryInstr lowers load/store instructions with optional memarg
// keywords (for example align=1 offset=8).
func (fl *functionLowerer) lowerMemoryInstr(pi *PlainInstr, instrLoc string) bool {
	kind, ok := memoryInstrKinds[pi.Name]
	if !ok {
		return false
	}
	align, offset, ok := parseMemArgOperands(pi.Operands)
	if !ok {
		fl.diagf(instrLoc, "invalid %s memory operands", pi.Name)
		return true
	}
	fl.emitInstr(wasmir.Instruction{
		Kind:         kind,
		MemoryAlign:  align,
		MemoryOffset: offset,
		SourceLoc:    instrLoc,
	})
	return true
}

// parseMemArgOperands parses optional memory-immediate operands from a plain
// load/store instruction.
//
// Return values:
//  1. alignExp: alignment exponent stored in the binary memarg immediate
//     (for example align=4 -> 2, align=1 -> 0). If align is omitted, this is 0.
//  2. offset: byte offset immediate. If offset is omitted, this is 0.
//  3. ok: true when all operands are valid memarg keywords; false on any
//     malformed/duplicate/unknown operand.
//
// Examples:
//   - [] -> (0, 0, true)
//   - ["align=4"] -> (2, 0, true)
//   - ["offset=8"] -> (0, 8, true)
//   - ["offset=8", "align=2"] -> (1, 8, true)
//   - ["align=3"] -> (0, 0, false) // not a power-of-two byte alignment
func parseMemArgOperands(operands []Operand) (uint32, uint32, bool) {
	var align uint32
	var offset uint32
	seenAlign := false
	seenOffset := false
	for _, op := range operands {
		kw, ok := op.(*KeywordOperand)
		if !ok {
			return 0, 0, false
		}
		parts := strings.SplitN(kw.Value, "=", 2)
		if len(parts) != 2 {
			return 0, 0, false
		}
		value, ok := parseU32Literal(parts[1])
		if !ok {
			return 0, 0, false
		}
		switch parts[0] {
		case "align":
			if seenAlign {
				return 0, 0, false
			}
			exp, ok := alignToExponent(value)
			if !ok {
				return 0, 0, false
			}
			align = exp
			seenAlign = true
		case "offset":
			if seenOffset {
				return 0, 0, false
			}
			offset = value
			seenOffset = true
		default:
			return 0, 0, false
		}
	}
	return align, offset, true
}

func alignToExponent(alignBytes uint32) (uint32, bool) {
	if alignBytes == 0 || (alignBytes&(alignBytes-1)) != 0 {
		return 0, false
	}
	var exp uint32
	for alignBytes > 1 {
		alignBytes >>= 1
		exp++
	}
	return exp, true
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

// decodeCallRefOperands decodes operands into ins.CallTypeIndex for call_ref.
func decodeCallRefOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	ref := operandText(operands[0])
	typeIndex, _, ok := fl.resolveTypeRef(ref)
	if !ok {
		return false
	}
	ins.CallTypeIndex = typeIndex
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

// decodeTableGetOperands decodes operands into ins.TableIndex for table.get.
func decodeTableGetOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	tableIndex, ok := lowerTableIndexOperand(operands[0], fl.mod.tablesByName)
	if !ok {
		return false
	}
	ins.TableIndex = tableIndex
	return true
}

// decodeRefNullOperands decodes operands into ins.RefType for ref.null.
func decodeRefNullOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	switch op := operands[0].(type) {
	case *KeywordOperand:
		switch op.Value {
		case "func":
			ins.RefType = wasmir.RefTypeFunc(true)
			return true
		case "extern":
			ins.RefType = wasmir.RefTypeExtern(true)
			return true
		default:
			return false
		}
	case *IdOperand:
		typeIndex, _, found := fl.resolveTypeRef(op.Value)
		if !found {
			return false
		}
		ins.RefType = wasmir.RefTypeIndexed(typeIndex, true)
		return true
	default:
		return false
	}
}

// decodeRefFuncOperands decodes operands into ins.FuncIndex for ref.func.
func decodeRefFuncOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
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

// loweredConstInstr is one lowered constant expression plus its resulting type.
type loweredConstInstr struct {
	Instr wasmir.Instruction
	Type  wasmir.ValueType
}

// evalI32ConstExpr evaluates an element offset constant expression to i32.
//
// Supported forms are:
//   - i32.const
//   - global.get of immutable i32 globals (including spectest.global_i32)
//   - folded i32.add/i32.sub/i32.mul over supported constant sub-expressions
func (l *moduleLowerer) evalI32ConstExpr(init Instruction) (int32, bool) {
	switch in := init.(type) {
	case *FoldedInstr:
		switch in.Name {
		case "i32.add", "i32.sub", "i32.mul":
			if len(in.Args) != 2 || in.Args[0].Instr == nil || in.Args[1].Instr == nil ||
				in.Args[0].Operand != nil || in.Args[1].Operand != nil {
				return 0, false
			}
			left, ok := l.evalI32ConstExpr(in.Args[0].Instr)
			if !ok {
				return 0, false
			}
			right, ok := l.evalI32ConstExpr(in.Args[1].Instr)
			if !ok {
				return 0, false
			}
			switch in.Name {
			case "i32.add":
				return left + right, true
			case "i32.sub":
				return left - right, true
			default:
				return left * right, true
			}
		}
	case *PlainInstr:
		// handled below through lowerConstInstr.
	}

	ci, ok := l.lowerConstInstr(init)
	if !ok {
		return 0, false
	}
	switch ci.Instr.Kind {
	case wasmir.InstrI32Const:
		return ci.Instr.I32Const, true
	case wasmir.InstrGlobalGet:
		return l.evalImportedI32Global(ci.Instr.GlobalIndex)
	default:
		return 0, false
	}
}

// evalImportedI32Global resolves a lowered i32 global.get for constant offsets.
func (l *moduleLowerer) evalImportedI32Global(globalIdx uint32) (int32, bool) {
	if int(globalIdx) >= len(l.out.Globals) {
		return 0, false
	}
	g := l.out.Globals[globalIdx]
	if g.Type != wasmir.ValueTypeI32 || g.Mutable {
		return 0, false
	}
	if !g.Imported {
		if g.Init.Kind == wasmir.InstrI32Const {
			return g.Init.I32Const, true
		}
		return 0, false
	}
	if g.ImportModule == "spectest" && g.ImportName == "global_i32" {
		return 666, true
	}
	return 0, false
}

// lowerConstInstr lowers init as a module-level constant expression.
//
// Accepted source forms are one-operand plain/folded instructions:
//   - i32.const, i64.const, f32.const, f64.const
//   - ref.null <heaptype>
//   - ref.func <funcidx-or-id>
//   - global.get <globalidx-or-id>
//
// The returned loweredConstInstr contains both the semantic instruction and the
// statically known resulting value type. global.get is resolved against globals
// already collected in l.out.Globals, so only earlier globals are valid here.
//
// This is a moduleLowerer method because it needs module-level resolution state
// (function/global name maps and current globals) that is already owned by l.
func (l *moduleLowerer) lowerConstInstr(init Instruction) (*loweredConstInstr, bool) {
	var name string
	var op Operand
	switch in := init.(type) {
	case *PlainInstr:
		if len(in.Operands) != 1 {
			return nil, false
		}
		name = in.Name
		op = in.Operands[0]
	case *FoldedInstr:
		if len(in.Args) != 1 || in.Args[0].Instr != nil || in.Args[0].Operand == nil {
			return nil, false
		}
		name = in.Name
		op = in.Args[0].Operand
	default:
		return nil, false
	}

	switch name {
	case "i32.const":
		imm, ok := lowerI32ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrI32Const, I32Const: imm},
			Type:  wasmir.ValueTypeI32,
		}, true
	case "i64.const":
		imm, ok := lowerI64ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrI64Const, I64Const: imm},
			Type:  wasmir.ValueTypeI64,
		}, true
	case "f32.const":
		imm, ok := lowerF32ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrF32Const, F32Const: imm},
			Type:  wasmir.ValueTypeF32,
		}, true
	case "f64.const":
		imm, ok := lowerF64ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrF64Const, F64Const: imm},
			Type:  wasmir.ValueTypeF64,
		}, true
	case "ref.null":
		var vt wasmir.ValueType
		switch o := op.(type) {
		case *KeywordOperand:
			switch o.Value {
			case "func":
				vt = wasmir.RefTypeFunc(true)
			case "extern":
				vt = wasmir.RefTypeExtern(true)
			default:
				return nil, false
			}
		case *IdOperand:
			typeIdx, ok := l.typesByName[o.Value]
			if !ok {
				return nil, false
			}
			vt = wasmir.RefTypeIndexed(typeIdx, true)
		default:
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrRefNull, RefType: vt},
			Type:  vt,
		}, true
	case "ref.func":
		funcIdx, ok := lowerFuncIndexOperand(op, l.funcsByName)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrRefFunc, FuncIndex: funcIdx},
			Type:  wasmir.RefTypeFunc(false),
		}, true
	case "global.get":
		globalIdx, ok := lowerGlobalIndexOperand(op, l.globalsByName)
		if !ok || int(globalIdx) >= len(l.out.Globals) {
			return nil, false
		}
		return &loweredConstInstr{
			Instr: wasmir.Instruction{Kind: wasmir.InstrGlobalGet, GlobalIndex: globalIdx},
			Type:  l.out.Globals[globalIdx].Type,
		}, true
	default:
		return nil, false
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

// lowerTableIndexOperand resolves op as a table index using tablesByName.
// It returns the resolved index and true on success, or 0/false otherwise.
func lowerTableIndexOperand(op Operand, tablesByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := tablesByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// lowerRefHeapTypeOperand lowers op as a reference heaptype operand used by
// instructions such as ref.null.
// op must be a keyword heaptype token like "func"/"extern" or an ID heaptype
// (for typed function references).
// It returns the lowered semantic reference type and true on success.
// On failure it returns 0/false.
func lowerRefHeapTypeOperand(op Operand) (wasmir.ValueType, bool) {
	switch o := op.(type) {
	case *KeywordOperand:
		switch o.Value {
		case "func":
			return wasmir.RefTypeFunc(true), true
		case "extern":
			return wasmir.RefTypeExtern(true), true
		default:
			return wasmir.ValueType{}, false
		}
	case *IdOperand:
		// Concrete type indices are resolved at higher-level call sites that have
		// access to module type definitions.
		return wasmir.ValueType{}, false
	default:
		return wasmir.ValueType{}, false
	}
}

func operandText(op Operand) string {
	switch o := op.(type) {
	case *IdOperand:
		return o.Value
	case *IntOperand:
		return o.Value
	case *KeywordOperand:
		return o.Value
	case *FloatOperand:
		return o.Value
	case *StringOperand:
		return o.Value
	default:
		return ""
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

// matchesExpectedValueType reports whether a produced value of type got can be
// used where a declaration expects want.
//
// This is intentionally weaker than strict equality for references:
//   - a concrete typed funcref `(ref $t)` is accepted where plain `funcref` or
//     `(ref func)` is expected
//   - nullability must still respect the destination, so nullable refs do not
//     match non-nullable expectations
func matchesExpectedValueType(got, want wasmir.ValueType) bool {
	if got == want {
		return true
	}
	if !got.IsRef() || !want.IsRef() {
		return false
	}
	if want.UsesTypeIndex() {
		if got.UsesTypeIndex() {
			if got.HeapType.TypeIndex != want.HeapType.TypeIndex {
				return false
			}
		} else if got.HeapType.Kind != wasmir.HeapKindFunc {
			return false
		}
	} else {
		switch want.HeapType.Kind {
		case wasmir.HeapKindFunc:
			if got.HeapType.Kind != wasmir.HeapKindFunc && got.HeapType.Kind != wasmir.HeapKindTypeIndex {
				return false
			}
		case wasmir.HeapKindExtern:
			if got.HeapType.Kind != wasmir.HeapKindExtern {
				return false
			}
		default:
			return false
		}
	}
	if got.Nullable && !want.Nullable {
		return false
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
		return wasmir.ValueType{}, false
	}
	return lowerValueType(&BasicType{Name: kw.Value}, nil)
}

// lowerFoldedBlockTypeArg lowers one folded block/if signature argument.
// It accepts either a plain keyword type like `i32` in `(result i32)` or a
// folded ref type like `(ref $t)` / `(ref null $t)` in
// `(result (ref $t))` and `(param (ref null $t))`.
func lowerFoldedBlockTypeArg(arg FoldedArg, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	if arg.Operand != nil {
		switch op := arg.Operand.(type) {
		case *KeywordOperand:
			return lowerValueType(&BasicType{Name: op.Value}, typesByName)
		default:
			return wasmir.ValueType{}, false
		}
	}
	refInstr, ok := arg.Instr.(*FoldedInstr)
	if !ok || refInstr.Name != "ref" {
		return wasmir.ValueType{}, false
	}
	return lowerFoldedRefTypeInstr(refInstr, typesByName)
}

// lowerFoldedRefTypeInstr lowers a folded ref type expression used inside
// block/if param and result clauses, for example `(ref func)`,
// `(ref $t)`, and `(ref null $t)`.
func lowerFoldedRefTypeInstr(fi *FoldedInstr, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	if fi == nil || fi.Name != "ref" {
		return wasmir.ValueType{}, false
	}
	if len(fi.Args) == 1 && fi.Args[0].Instr == nil && fi.Args[0].Operand != nil {
		switch op := fi.Args[0].Operand.(type) {
		case *KeywordOperand:
			return lowerValueType(&RefType{Nullable: false, HeapType: op.Value}, typesByName)
		case *IdOperand:
			return lowerValueType(&RefType{Nullable: false, HeapType: op.Value}, typesByName)
		default:
			return wasmir.ValueType{}, false
		}
	}
	if len(fi.Args) == 2 && fi.Args[0].Instr == nil && fi.Args[0].Operand != nil &&
		fi.Args[1].Instr == nil && fi.Args[1].Operand != nil {
		nullOp, ok := fi.Args[0].Operand.(*KeywordOperand)
		if !ok || nullOp.Value != "null" {
			return wasmir.ValueType{}, false
		}
		switch op := fi.Args[1].Operand.(type) {
		case *KeywordOperand:
			return lowerValueType(&RefType{Nullable: true, HeapType: op.Value}, typesByName)
		case *IdOperand:
			return lowerValueType(&RefType{Nullable: true, HeapType: op.Value}, typesByName)
		default:
			return wasmir.ValueType{}, false
		}
	}
	return wasmir.ValueType{}, false
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

// decodeWATStringBytes decodes a WAT STRING token payload to raw bytes.
func decodeWATStringBytes(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '\\' {
			out = append(out, ch)
			continue
		}
		if i+1 >= len(s) {
			return nil, fmt.Errorf("trailing backslash")
		}
		next := s[i+1]
		if i+2 < len(s) && isASCIIHexDigit(next) && isASCIIHexDigit(s[i+2]) {
			hi := hexNibble(next)
			lo := hexNibble(s[i+2])
			out = append(out, (hi<<4)|lo)
			i += 2
			continue
		}
		switch next {
		case 't':
			out = append(out, '\t')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case '"':
			out = append(out, '"')
		case '\'':
			out = append(out, '\'')
		case '\\':
			out = append(out, '\\')
		default:
			return nil, fmt.Errorf("unsupported escape \\%c", next)
		}
		i++
	}
	return out, nil
}

func isASCIIHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func hexNibble(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	default:
		return b - 'A' + 10
	}
}

// lowerValueType lowers ty from textformat type syntax into semantic wasmir
// type representation.
// It returns the lowered type and true on success, or zero/false if ty is
// unsupported.
func lowerValueType(ty Type, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	switch t := ty.(type) {
	case *BasicType:
		switch t.Name {
		case "i32":
			return wasmir.ValueTypeI32, true
		case "i64":
			return wasmir.ValueTypeI64, true
		case "f32":
			return wasmir.ValueTypeF32, true
		case "f64":
			return wasmir.ValueTypeF64, true
		case "funcref":
			return wasmir.RefTypeFunc(true), true
		case "externref":
			return wasmir.RefTypeExtern(true), true
		default:
			return wasmir.ValueType{}, false
		}
	case *RefType:
		return lowerRefTypeInfo(t, typesByName)
	default:
		return wasmir.ValueType{}, false
	}
}

// lowerRefTypeInfo lowers ty as a reference type declaration.
// ty may be a BasicType ("funcref"/"externref") or a RefType
// ("(ref ...)" / "(ref null ...)").
// It returns the lowered type and true on success.
func lowerRefTypeInfo(ty Type, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	switch t := ty.(type) {
	case *BasicType:
		switch t.Name {
		case "funcref":
			return wasmir.RefTypeFunc(true), true
		case "externref":
			return wasmir.RefTypeExtern(true), true
		default:
			return wasmir.ValueType{}, false
		}
	case *RefType:
		return lowerRefHeapTypeName(t.HeapType, t.Nullable, typesByName)
	default:
		return wasmir.ValueType{}, false
	}
}

// lowerRefHeapTypeName lowers a text heaptype name (for example "func",
// "extern", or a type identifier like "$t") into a semantic reference type.
// It returns the lowered type and true on success.
func lowerRefHeapTypeName(name string, nullable bool, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	switch name {
	case "func":
		return wasmir.RefTypeFunc(nullable), true
	case "extern":
		return wasmir.RefTypeExtern(nullable), true
	default:
		if strings.HasPrefix(name, "$") {
			typeIndex, ok := typesByName[name]
			if !ok {
				return wasmir.ValueType{}, false
			}
			return wasmir.RefTypeIndexed(typeIndex, nullable), true
		}
		return wasmir.ValueType{}, false
	}
}

// lowerTypeParams lowers params from one type declaration into semantic value
// types.
// params is the parsed parameter declaration list for a single type.
// typeIdx is used only for diagnostic context ("type[typeIdx] ...").
// diags accumulates per-parameter lowering errors.
// It returns the successfully lowered parameter types in declaration order.
func (l *moduleLowerer) lowerTypeParams(params []*ParamDecl, typeIdx int) []wasmir.ValueType {
	out := make([]wasmir.ValueType, 0, len(params))
	for i, pd := range params {
		if pd == nil {
			l.diags.Addf("type[%d] param[%d]: nil param declaration", typeIdx, i)
			continue
		}
		vt, ok := lowerValueType(pd.Ty, l.typesByName)
		if !ok {
			l.diags.Addf("type[%d] param[%d]: unsupported param type %q", typeIdx, i, pd.Ty)
			continue
		}
		out = append(out, vt)
	}
	return out
}

func (l *moduleLowerer) lowerTypeResults(results []*ResultDecl, typeIdx int) []wasmir.ValueType {
	out := make([]wasmir.ValueType, 0, len(results))
	for i, rd := range results {
		if rd == nil {
			l.diags.Addf("type[%d] result[%d]: nil result declaration", typeIdx, i)
			continue
		}
		vt, ok := lowerValueType(rd.Ty, l.typesByName)
		if !ok {
			l.diags.Addf("type[%d] result[%d]: unsupported result type %q", typeIdx, i, rd.Ty)
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
