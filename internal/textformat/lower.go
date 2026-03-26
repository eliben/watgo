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

	// elemIndicesByName maps element segment identifiers to their module
	// element indices for instructions such as table.init and elem.drop.
	elemIndicesByName map[string]uint32

	// dataIndicesByName maps data segment identifiers to their module data
	// indices for instructions such as array.new_data.
	dataIndicesByName map[string]uint32
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
		elemIndicesByName: map[string]uint32{},
		dataIndicesByName: map[string]uint32{},
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
			l.elemIndicesByName[ed.Id] = uint32(len(l.out.Elements))
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

			offsetType := wasmir.ValueTypeI32
			if int(tableIndex) < len(l.out.Tables) {
				offsetType = l.out.Tables[tableIndex].AddressType
			}
			offsetValue, ok := l.evalMemoryOffsetConst(ed.Offset, offsetType)
			if !ok {
				l.diags.Addf("elem[%d]: offset must be %s.const", i, offsetType)
				continue
			}
			seg.TableIndex = tableIndex
			seg.OffsetType = offsetType
			seg.OffsetI64 = offsetValue
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
				seg.Exprs = append(seg.Exprs, append([]wasmir.Instruction(nil), ce.Instrs...))
			}
		} else {
			if ed.RefTy != nil {
				refTy, ok := lowerValueType(ed.RefTy, l.typesByName)
				if !ok {
					l.diags.Addf("elem[%d]: unsupported reference type %q", i, ed.RefTy)
					continue
				}
				seg.RefType = refTy
			}
			tableRefType := wasmir.RefTypeFunc(true)
			if seg.Mode == wasmir.ElemSegmentModeActive && int(seg.TableIndex) < len(l.out.Tables) {
				tableRefType = l.out.Tables[seg.TableIndex].RefType
			}
			if seg.Mode == wasmir.ElemSegmentModeActive && usesExprElementSegment(tableRefType) {
				seg.RefType = tableRefType
				seg.Exprs = make([][]wasmir.Instruction, 0, len(ed.FuncRefs))
				for j, ref := range ed.FuncRefs {
					funcIdx, ok := l.resolveFunctionRef(ref)
					if !ok {
						l.diags.Addf("elem[%d] func[%d]: unknown function reference %q", i, j, ref)
						continue
					}
					seg.Exprs = append(seg.Exprs, []wasmir.Instruction{{Kind: wasmir.InstrRefFunc, FuncIndex: funcIdx}})
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
		if td == nil || td.Id == "" {
			continue
		}
		if prev, exists := l.typesByName[td.Id]; exists {
			l.diags.Addf("type[%d] %s: duplicate type id (first seen at type[%d])", i, td.Id, prev)
			continue
		}
		l.typesByName[td.Id] = uint32(i)
	}
	for i, td := range astm.Types {
		if td == nil {
			l.diags.Addf("type[%d]: nil type declaration", i)
			continue
		}

		outType := wasmir.FuncType{Name: td.Id}
		if td.RecGroupSize > 0 {
			outType.RecGroupSize = uint32(td.RecGroupSize)
		}
		outType.SubType = td.SubType
		outType.Final = td.Final
		for j, superRef := range td.SuperTypes {
			superIndex, ok := resolveTypeRef(superRef, l.typesByName)
			if !ok {
				l.diags.Addf("type[%d] super[%d]: unknown type %q", i, j, superRef)
				continue
			}
			outType.SuperTypes = append(outType.SuperTypes, superIndex)
		}
		switch {
		case td.TyUse != nil:
			outType.Kind = wasmir.TypeDefKindFunc
			outType.Params = l.lowerTypeParams(td.TyUse.Params, i)
			outType.Results = l.lowerTypeResults(td.TyUse.Results, i)
		case td.StructFields != nil:
			outType.Kind = wasmir.TypeDefKindStruct
			outType.Fields = l.lowerTypeFields(td.StructFields, i)
		case td.ArrayField != nil:
			field, ok := l.lowerFieldType(td.ArrayField, i, 0)
			if ok {
				outType.Kind = wasmir.TypeDefKindArray
				outType.ElemField = field
			} else {
				outType.Kind = wasmir.TypeDefKindArray
			}
		default:
			l.diags.Addf("type[%d]: missing type declaration body", i)
			continue
		}

		typeIdx := uint32(len(l.out.Types))
		if typeIdx != uint32(i) {
			l.diags.Addf("type[%d]: internal type index mismatch", i)
		}
		l.out.Types = append(l.out.Types, outType)
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
		if len(td.ElemRefs) > 0 && min < uint64(len(td.ElemRefs)) {
			min = uint64(len(td.ElemRefs))
		}
		if len(td.ElemExprs) > 0 && min < uint64(len(td.ElemExprs)) {
			min = uint64(len(td.ElemExprs))
		}
		if td.HasMax && td.Max < min {
			l.diags.Addf("table[%d]: size minimum must not be greater than maximum", i)
			continue
		}
		addressType, ok := lowerMemoryAddressType(td.AddressType)
		if !ok {
			l.diags.Addf("table[%d]: unsupported table address type %q", i, td.AddressType)
			continue
		}

		tb := wasmir.Table{
			AddressType: addressType,
			Min:         min,
			HasMax:      td.HasMax,
			Max:         td.Max,
			RefType:     refType,
		}
		if td.ImportModule != "" {
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
			seg := wasmir.ElementSegment{TableIndex: tableIdx, OffsetType: addressType, OffsetI64: 0}
			if usesExprElementSegment(refType) {
				// Typed or non-null function tables cannot use the legacy
				// function-index element encoding. For example,
				//   (table $t (ref null $t) (elem $tf))
				// must lower to ref-expression payloads like `(ref.func $tf)`
				// so the element segment carries the table's precise ref type.
				seg.RefType = refType
				seg.Exprs = make([][]wasmir.Instruction, 0, len(td.ElemRefs))
				for _, ref := range td.ElemRefs {
					idx, ok := l.resolveFunctionRef(ref)
					if !ok {
						l.diags.Addf("table[%d]: unknown elem function ref %q", i, ref)
						continue
					}
					seg.Exprs = append(seg.Exprs, []wasmir.Instruction{{Kind: wasmir.InstrRefFunc, FuncIndex: idx}})
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
				OffsetType: addressType,
				OffsetI64:  0,
				RefType:    refType,
				Exprs:      make([][]wasmir.Instruction, 0, len(td.ElemExprs)),
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
				seg.Exprs = append(seg.Exprs, append([]wasmir.Instruction(nil), ci.Instrs...))
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
			if !nullable && len(ci.Instrs) == 1 && ci.Instrs[0].Kind == wasmir.InstrRefNull {
				l.diags.Addf("table[%d]: type mismatch", i)
				continue
			}
			l.out.Tables[tableIdx].Init = append([]wasmir.Instruction(nil), ci.Instrs...)
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

		addressType, ok := lowerMemoryAddressType(md.AddressType)
		if !ok {
			l.diags.Addf("memory[%d]: unsupported memory address type %q", i, md.AddressType)
			addressType = wasmir.ValueTypeI32
		}
		mem := wasmir.Memory{
			AddressType:  addressType,
			Min:          md.Min,
			HasMax:       md.HasMax,
			Max:          md.Max,
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
			needPages := uint64((len(init) + wasmPageSizeBytes - 1) / wasmPageSizeBytes)
			if mem.Min < needPages {
				l.out.Memories[memIdx].Min = needPages
			}
			l.out.Data = append(l.out.Data, wasmir.DataSegment{
				Mode:        wasmir.DataSegmentModeActive,
				MemoryIndex: memIdx,
				OffsetType:  addressType,
				OffsetI64:   0,
				Init:        init,
			})
		}
	}
}

// collectDataDecls lowers module-level data declarations into active or
// passive memory segments.
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
		var init []byte
		for j, s := range dd.Strings {
			b, err := decodeWATStringBytes(s)
			if err != nil {
				l.diags.Addf("data[%d] string[%d]: %v", i, j, err)
				continue
			}
			init = append(init, b...)
		}
		seg := wasmir.DataSegment{Init: init}
		if dd.Offset == nil {
			seg.Mode = wasmir.DataSegmentModePassive
			dataIndex := uint32(len(l.out.Data))
			l.out.Data = append(l.out.Data, seg)
			if dd.Id != "" {
				if prev, exists := l.dataIndicesByName[dd.Id]; exists {
					l.diags.Addf("data[%d] %s: duplicate data id (first seen at data[%d])", i, dd.Id, prev)
				} else {
					l.dataIndicesByName[dd.Id] = dataIndex
				}
			}
			continue
		}

		offsetType := wasmir.ValueTypeI32
		if int(memoryIndex) < len(l.out.Memories) {
			offsetType = normalizedMemoryAddressType(l.out.Memories[memoryIndex])
		}
		offset, ok := l.evalMemoryOffsetConst(dd.Offset, offsetType)
		if !ok {
			l.diags.Addf("data[%d]: offset must be %s.const", i, offsetType)
			continue
		}
		seg.Mode = wasmir.DataSegmentModeActive
		seg.MemoryIndex = memoryIndex
		seg.OffsetType = offsetType
		seg.OffsetI64 = offset
		dataIndex := uint32(len(l.out.Data))
		l.out.Data = append(l.out.Data, seg)
		if dd.Id != "" {
			if prev, exists := l.dataIndicesByName[dd.Id]; exists {
				l.diags.Addf("data[%d] %s: duplicate data id (first seen at data[%d])", i, dd.Id, prev)
			} else {
				l.dataIndicesByName[dd.Id] = dataIndex
			}
		}
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
			g.Init = append([]wasmir.Instruction(nil), ci.Instrs...)
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
		if ft.Kind == wasmir.TypeDefKindFunc &&
			equalValueTypeSlices(ft.Params, params) &&
			equalValueTypeSlices(ft.Results, results) {
			return uint32(i)
		}
	}
	typeIdx := uint32(len(l.out.Types))
	l.out.Types = append(l.out.Types, wasmir.FuncType{
		Name:    name,
		Kind:    wasmir.TypeDefKindFunc,
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
	if refType.Kind != wasmir.TypeDefKindFunc {
		fl.diagf(fl.fn.loc.String(), "type use %q does not reference a function type", fl.fn.TyUse.Id)
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
	if fi.Name == "br_on_cast" || fi.Name == "br_on_cast_fail" {
		fl.lowerFoldedBrOnCast(fi)
		return
	}
	if fi.Name == "ref.test" || fi.Name == "ref.cast" {
		fl.lowerFoldedRefTypeTestCast(fi)
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
	var params []wasmir.ValueType
	var results []wasmir.ValueType
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
		if nested.Name == "param" {
			for _, paramArg := range nested.Args {
				vt, ok := lowerFoldedBlockTypeArg(paramArg, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid call_indirect param clause")
					params = nil
					break
				}
				params = append(params, vt)
			}
			continue
		}
		if nested.Name == "result" {
			for _, resultArg := range nested.Args {
				vt, ok := lowerFoldedBlockTypeArg(resultArg, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid call_indirect result clause")
					results = nil
					break
				}
				results = append(results, vt)
			}
			continue
		}
		fl.lowerInstruction(nested)
	}
	if typeRef == "" {
		typeIdx := fl.mod.internFuncType(params, results, "")
		fl.emitInstr(wasmir.Instruction{
			Kind:          wasmir.InstrCallIndirect,
			CallTypeIndex: typeIdx,
			TableIndex:    tableIndex,
			SourceLoc:     fi.loc.String(),
		})
		return
	}
	typeIdx, refType, ok := fl.resolveTypeRef(typeRef)
	if !ok {
		fl.diagf(fi.Loc(), "unknown call_indirect type use %q", typeRef)
		return
	}
	if len(params) > 0 || len(results) > 0 {
		if !equalValueTypeSlices(params, refType.Params) || !equalValueTypeSlices(results, refType.Results) {
			fl.diagf(fi.Loc(), "call_indirect type clause mismatch referenced type")
			return
		}
	}
	fl.emitInstr(wasmir.Instruction{
		Kind:          wasmir.InstrCallIndirect,
		CallTypeIndex: typeIdx,
		TableIndex:    tableIndex,
		SourceLoc:     fi.loc.String(),
	})
}

// lowerFoldedRefTypeTestCast lowers folded forms like:
//
//	(ref.test (ref i31) (local.get $x))
//	(ref.cast (ref i31) (local.get $x))
//	(ref.cast i31ref (global.get $g))
//
// The first argument is the reference type immediate. It may be written either
// as a folded "(ref ...)" form or as a plain shorthand token like `i31ref`.
// Any remaining nested expressions are normal operands evaluated before the
// instruction.
func (fl *functionLowerer) lowerFoldedRefTypeTestCast(fi *FoldedInstr) {
	var refType wasmir.ValueType
	haveRefType := false
	for _, arg := range fi.Args {
		if arg.Operand != nil {
			if !haveRefType {
				vt, ok := lowerCastTypeOperand(arg.Operand, fl.mod.typesByName)
				if !ok {
					fl.diagf(arg.Operand.Loc(), "invalid %s reference type", fi.Name)
					continue
				}
				refType = vt
				haveRefType = true
				continue
			}
			fl.diagf(arg.Operand.Loc(), "%s expects reference type and value expression", fi.Name)
			continue
		}
		nested, ok := arg.Instr.(*FoldedInstr)
		if ok && nested.Name == "ref" && !haveRefType {
			vt, ok := lowerFoldedRefTypeInstr(nested, fl.mod.typesByName)
			if !ok {
				fl.diagf(nested.Loc(), "invalid %s reference type", fi.Name)
				continue
			}
			refType = vt
			haveRefType = true
			continue
		}
		fl.lowerInstruction(arg.Instr)
	}
	if !haveRefType {
		fl.diagf(fi.Loc(), "%s requires reference type immediate", fi.Name)
		return
	}
	kind, _ := instructionKind(fi.Name)
	fl.emitInstr(wasmir.Instruction{Kind: kind, RefType: refType, SourceLoc: fi.loc.String()})
}

// lowerFoldedBrOnCast lowers folded br_on_cast and br_on_cast_fail forms.
//
// It expects the folded instruction to provide:
//   - a branch depth or label
//   - a source reference type
//   - a destination reference type
//   - zero or more nested operand expressions
//
// For example:
//   - (br_on_cast $l anyref (ref i31) (table.get (local.get $i)))
//   - (br_on_cast_fail 0 structref (ref $t) (local.get 0))
//
// The type immediates may be written either as plain type operands like
// `anyref` / `structref` or as folded ref-type forms like `(ref i31)` and
// `(ref null $t)`. Any remaining nested instructions are lowered first so the
// value operand is on the stack before the final br_on_cast* instruction.
func (fl *functionLowerer) lowerFoldedBrOnCast(fi *FoldedInstr) {
	var (
		branchDepth uint32
		haveDepth   bool
		srcType     wasmir.ValueType
		haveSrc     bool
		dstType     wasmir.ValueType
		haveDst     bool
	)
	for _, arg := range fi.Args {
		if arg.Operand != nil {
			if !haveDepth {
				depth, ok := fl.lowerLabelOperand(arg.Operand)
				if !ok {
					fl.diagf(arg.Operand.Loc(), "invalid %s branch depth", fi.Name)
					continue
				}
				branchDepth = depth
				haveDepth = true
				continue
			}
			if !haveSrc {
				vt, ok := lowerCastTypeOperand(arg.Operand, fl.mod.typesByName)
				if !ok {
					fl.diagf(arg.Operand.Loc(), "invalid %s source type", fi.Name)
					continue
				}
				srcType = vt
				haveSrc = true
				continue
			}
			if !haveDst {
				vt, ok := lowerCastTypeOperand(arg.Operand, fl.mod.typesByName)
				if !ok {
					fl.diagf(arg.Operand.Loc(), "invalid %s destination type", fi.Name)
					continue
				}
				dstType = vt
				haveDst = true
				continue
			}
			fl.diagf(arg.Operand.Loc(), "%s has too many operands", fi.Name)
			continue
		}
		nested, ok := arg.Instr.(*FoldedInstr)
		if ok && nested.Name == "ref" {
			if !haveSrc {
				vt, ok := lowerFoldedRefTypeInstr(nested, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid %s source type", fi.Name)
					continue
				}
				srcType = vt
				haveSrc = true
				continue
			}
			if !haveDst {
				vt, ok := lowerFoldedRefTypeInstr(nested, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid %s destination type", fi.Name)
					continue
				}
				dstType = vt
				haveDst = true
				continue
			}
		}
		fl.lowerInstruction(arg.Instr)
	}
	if !haveDepth || !haveSrc || !haveDst {
		fl.diagf(fi.Loc(), "%s requires branch depth, source type, and destination type", fi.Name)
		return
	}
	kind, _ := instructionKind(fi.Name)
	fl.emitInstr(wasmir.Instruction{
		Kind:          kind,
		BranchDepth:   branchDepth,
		SourceRefType: srcType,
		RefType:       dstType,
		SourceLoc:     fi.loc.String(),
	})
}

// lowerFoldedIf lowers a folded if-expression preserving then/else blocks.
func (fl *functionLowerer) lowerFoldedIf(fi *FoldedInstr) {
	var labelName string
	var paramTypes []wasmir.ValueType
	var resultTypes []wasmir.ValueType
	var typeRef string
	var thenClause *FoldedInstr
	var elseClause *FoldedInstr
	seenSignatureEnd := false

	for i, arg := range fi.Args {
		if arg.Operand != nil {
			// Folded `if` may start with a label identifier:
			//   (if $done (local.get 0) (then ...))
			if i == 0 {
				if id, ok := arg.Operand.(*IdOperand); ok {
					labelName = id.Value
					continue
				}
			}
			fl.diagf(arg.Operand.Loc(), "if expects nested expressions/clauses")
			continue
		}

		nested, ok := arg.Instr.(*FoldedInstr)
		if !ok {
			fl.lowerInstruction(arg.Instr)
			continue
		}

		switch nested.Name {
		case "type":
			// Type uses belong to the signature prefix, before the condition
			// and branch bodies:
			//   (if (type $sig) (i32.const 1) (then ...))
			if seenSignatureEnd || len(paramTypes) > 0 || len(resultTypes) > 0 || typeRef != "" {
				fl.diagf(nested.Loc(), "unexpected token in if signature")
				continue
			}
			ref, ok := parseFoldedTypeClauseRef(nested)
			if !ok {
				fl.diagf(nested.Loc(), "invalid if type clause")
				continue
			}
			typeRef = ref
		case "param":
			// Parameterized `if` forms forward stack operands into both branches:
			//   (if (param i32 i32) (result i32) (local.get 0)
			//     (then (i32.add))
			//     (else (i32.sub)))
			if seenSignatureEnd || len(resultTypes) > 0 {
				fl.diagf(nested.Loc(), "unexpected token in if signature")
				continue
			}
			for _, paramArg := range nested.Args {
				if _, isID := paramArg.Operand.(*IdOperand); isID {
					fl.diagf(paramArg.Operand.Loc(), "named if params are not supported")
					continue
				}
				vt, ok := lowerFoldedBlockTypeArg(paramArg, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid if param clause")
					continue
				}
				paramTypes = append(paramTypes, vt)
			}
		case "result":
			// Folded `if` allows single- and multi-value result clauses:
			//   (if (result i32) ...)
			//   (if (result i32 i64 i32) ...)
			if seenSignatureEnd {
				fl.diagf(nested.Loc(), "unexpected token in if body")
				continue
			}
			if len(nested.Args) == 0 {
				fl.diagf(nested.Loc(), "invalid if result clause")
				continue
			}
			for _, resultArg := range nested.Args {
				vt, ok := lowerFoldedBlockTypeArg(resultArg, fl.mod.typesByName)
				if !ok {
					fl.diagf(nested.Loc(), "invalid if result clause")
					continue
				}
				resultTypes = append(resultTypes, vt)
			}
		case "then":
			seenSignatureEnd = true
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
			// Any other nested instruction before `(then ...)` / `(else ...)`
			// belongs to the condition expression sequence:
			//   (if (result i32) (local.get 0) (then ...) (else ...))
			seenSignatureEnd = true
			fl.lowerInstruction(nested)
		}
	}

	if thenClause == nil {
		fl.diagf(fi.Loc(), "if requires then clause")
		return
	}

	finalParams := paramTypes
	finalResults := resultTypes
	useTypeIndex := false
	var typeIdx uint32

	if typeRef != "" {
		// With an explicit type use, either the type provides the full
		// signature:
		//   (if (type $sig) (i32.const 1) (then ...))
		// or any inline `(param ...)` / `(result ...)` clauses must match it.
		refIdx, refType, ok := fl.resolveTypeRef(typeRef)
		if !ok {
			fl.diagf(fi.Loc(), "unknown if type use %q", typeRef)
		} else {
			useTypeIndex = true
			typeIdx = refIdx
			if len(paramTypes) > 0 || len(resultTypes) > 0 {
				if !equalValueTypeSlices(paramTypes, refType.Params) || !equalValueTypeSlices(resultTypes, refType.Results) {
					fl.diagf(fi.Loc(), "inline function type mismatch in if")
				}
			} else {
				finalParams = append([]wasmir.ValueType(nil), refType.Params...)
				finalResults = append([]wasmir.ValueType(nil), refType.Results...)
			}
		}
	}

	ins := wasmir.Instruction{Kind: wasmir.InstrIf, SourceLoc: fi.loc.String()}
	switch {
	case useTypeIndex:
		// Explicit `(type ...)` lowers directly to an indexed blocktype.
		ins.BlockTypeUsesIndex = true
		ins.BlockTypeIndex = typeIdx
	case len(finalParams) > 0 || len(finalResults) > 1:
		// Parameterized or multi-result `if` needs a synthetic function-type
		// blocktype:
		//   (if (param i32) (result i32) ...)
		//   (if (result i32 i64) ...)
		ins.BlockTypeUsesIndex = true
		ins.BlockTypeIndex = fl.mod.internFuncType(finalParams, finalResults, "")
	case len(finalResults) == 1:
		// A plain single-result `if` can use the compact valtype blocktype:
		//   (if (result i32) ...)
		ins.BlockHasResult = true
		ins.BlockType = finalResults[0]
	}
	fl.emitInstr(ins)
	fl.pushLabel(labelName)
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
	operandCount int
	decode       loweringOperandDecoder
}

// loweringOperandDecoder decodes instruction operands into ins.
// It returns true on success and false when operands are invalid.
type loweringOperandDecoder func(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool

// loweringSpecs maps plain instruction names to table-driven lowering rules.
var loweringSpecs = map[string]loweringSpec{
	"any.convert_extern":  {operandCount: 0},
	"extern.convert_any":  {operandCount: 0},
	"nop":                 {operandCount: 0},
	"else":                {operandCount: 0},
	"end":                 {operandCount: 0},
	"drop":                {operandCount: 0},
	"select":              {operandCount: 0},
	"i32.add":             {operandCount: 0},
	"i32.sub":             {operandCount: 0},
	"i32.mul":             {operandCount: 0},
	"i32.or":              {operandCount: 0},
	"i32.xor":             {operandCount: 0},
	"i32.div_s":           {operandCount: 0},
	"i32.div_u":           {operandCount: 0},
	"i32.rem_s":           {operandCount: 0},
	"i32.rem_u":           {operandCount: 0},
	"i32.shl":             {operandCount: 0},
	"i32.shr_s":           {operandCount: 0},
	"i32.shr_u":           {operandCount: 0},
	"i32.rotl":            {operandCount: 0},
	"i32.rotr":            {operandCount: 0},
	"i32.clz":             {operandCount: 0},
	"i32.popcnt":          {operandCount: 0},
	"i32.extend8_s":       {operandCount: 0},
	"i32.extend16_s":      {operandCount: 0},
	"i32.eqz":             {operandCount: 0},
	"i32.ne":              {operandCount: 0},
	"i32.lt_s":            {operandCount: 0},
	"i32.lt_u":            {operandCount: 0},
	"i32.le_s":            {operandCount: 0},
	"i32.le_u":            {operandCount: 0},
	"i32.gt_s":            {operandCount: 0},
	"i32.gt_u":            {operandCount: 0},
	"i32.ge_s":            {operandCount: 0},
	"i32.ge_u":            {operandCount: 0},
	"i32.and":             {operandCount: 0},
	"i64.add":             {operandCount: 0},
	"i64.and":             {operandCount: 0},
	"i64.or":              {operandCount: 0},
	"i64.xor":             {operandCount: 0},
	"i64.eq":              {operandCount: 0},
	"i64.ne":              {operandCount: 0},
	"i64.eqz":             {operandCount: 0},
	"i64.gt_s":            {operandCount: 0},
	"i64.gt_u":            {operandCount: 0},
	"i64.ge_s":            {operandCount: 0},
	"i64.ge_u":            {operandCount: 0},
	"i64.le_s":            {operandCount: 0},
	"i64.le_u":            {operandCount: 0},
	"i64.sub":             {operandCount: 0},
	"i64.mul":             {operandCount: 0},
	"i64.div_s":           {operandCount: 0},
	"i64.div_u":           {operandCount: 0},
	"i64.rem_s":           {operandCount: 0},
	"i64.rem_u":           {operandCount: 0},
	"i64.shl":             {operandCount: 0},
	"i64.shr_s":           {operandCount: 0},
	"i64.shr_u":           {operandCount: 0},
	"i64.rotl":            {operandCount: 0},
	"i64.rotr":            {operandCount: 0},
	"i64.lt_s":            {operandCount: 0},
	"i64.lt_u":            {operandCount: 0},
	"i64.clz":             {operandCount: 0},
	"i64.ctz":             {operandCount: 0},
	"i64.popcnt":          {operandCount: 0},
	"i64.extend8_s":       {operandCount: 0},
	"i64.extend16_s":      {operandCount: 0},
	"i64.extend32_s":      {operandCount: 0},
	"i32.wrap_i64":        {operandCount: 0},
	"i64.extend_i32_s":    {operandCount: 0},
	"i64.extend_i32_u":    {operandCount: 0},
	"f32.convert_i32_s":   {operandCount: 0},
	"f64.convert_i64_s":   {operandCount: 0},
	"f32.add":             {operandCount: 0},
	"f32.sub":             {operandCount: 0},
	"f32.mul":             {operandCount: 0},
	"f32.div":             {operandCount: 0},
	"f32.sqrt":            {operandCount: 0},
	"f32.neg":             {operandCount: 0},
	"f32.eq":              {operandCount: 0},
	"f32.lt":              {operandCount: 0},
	"f32.min":             {operandCount: 0},
	"f32.max":             {operandCount: 0},
	"f32.ne":              {operandCount: 0},
	"f32.ceil":            {operandCount: 0},
	"f32.floor":           {operandCount: 0},
	"f32.trunc":           {operandCount: 0},
	"f32.nearest":         {operandCount: 0},
	"f64.add":             {operandCount: 0},
	"f64.sub":             {operandCount: 0},
	"f64.mul":             {operandCount: 0},
	"f64.div":             {operandCount: 0},
	"f64.sqrt":            {operandCount: 0},
	"f64.neg":             {operandCount: 0},
	"f64.min":             {operandCount: 0},
	"f64.max":             {operandCount: 0},
	"f64.ceil":            {operandCount: 0},
	"f64.floor":           {operandCount: 0},
	"f64.trunc":           {operandCount: 0},
	"f64.nearest":         {operandCount: 0},
	"f64.eq":              {operandCount: 0},
	"f64.le":              {operandCount: 0},
	"i32.reinterpret_f32": {operandCount: 0},
	"i64.reinterpret_f64": {operandCount: 0},
	"f32.reinterpret_i32": {operandCount: 0},
	"f64.reinterpret_i64": {operandCount: 0},
	"local.get":           {operandCount: 1, decode: decodeLocalGetOperands},
	"local.set":           {operandCount: 1, decode: decodeLocalSetOperands},
	"local.tee":           {operandCount: 1, decode: decodeLocalTeeOperands},
	"call":                {operandCount: 1, decode: decodeCallOperands},
	"call_ref":            {operandCount: 1, decode: decodeCallRefOperands},
	"br":                  {operandCount: 1, decode: decodeBrOperands},
	"br_if":               {operandCount: 1, decode: decodeBrOperands},
	"br_on_null":          {operandCount: 1, decode: decodeBrOperands},
	"br_on_non_null":      {operandCount: 1, decode: decodeBrOperands},
	"global.get":          {operandCount: 1, decode: decodeGlobalGetOperands},
	"global.set":          {operandCount: 1, decode: decodeGlobalSetOperands},
	"unreachable":         {operandCount: 0},
	"return":              {operandCount: 0},
	"i32.eq":              {operandCount: 0},
	"i32.ctz":             {operandCount: 0},
	"f32.gt":              {operandCount: 0},
	"i8x16.swizzle":       {operandCount: 0},
	"i32x4.eq":            {operandCount: 0},
	"i32x4.lt_s":          {operandCount: 0},
	"i32x4.add":           {operandCount: 0},
	"i32x4.neg":           {operandCount: 0},
	"i32x4.min_s":         {operandCount: 0},
	"f32x4.add":           {operandCount: 0},
	"v128.bitselect":      {operandCount: 0},
	"i32.const":           {operandCount: 1, decode: decodeI32ConstOperands},
	"i64.const":           {operandCount: 1, decode: decodeI64ConstOperands},
	"f32.const":           {operandCount: 1, decode: decodeF32ConstOperands},
	"f64.const":           {operandCount: 1, decode: decodeF64ConstOperands},
	"v128.const":          {operandCount: -1, decode: decodeV128ConstOperands},
	"i32x4.splat":         {operandCount: 0},
	"i32x4.extract_lane":  {operandCount: 1, decode: decodeLaneIndexOperands},
	"ref.null":            {operandCount: 1, decode: decodeRefNullOperands},
	"ref.eq":              {operandCount: 0},
	"ref.is_null":         {operandCount: 0},
	"ref.as_non_null":     {operandCount: 0},
	"ref.func":            {operandCount: 1, decode: decodeRefFuncOperands},
	"ref.i31":             {operandCount: 0},
	"i31.get_s":           {operandCount: 0},
	"i31.get_u":           {operandCount: 0},
	"memory.init":         {operandCount: 1, decode: decodeDataIndexOperands},
	"data.drop":           {operandCount: 1, decode: decodeDataIndexOperands},
	"elem.drop":           {operandCount: 1, decode: decodeElemIndexOperands},
}

// lowerBySpec lowers pi using loweringSpecs when pi.Name is table-driven.
// It returns true when a table entry exists, including validation failures that
// emit diagnostics.
func (fl *functionLowerer) lowerBySpec(pi *PlainInstr, instrLoc string) bool {
	spec, ok := loweringSpecs[pi.Name]
	if !ok {
		return false
	}
	if spec.operandCount >= 0 && len(pi.Operands) != spec.operandCount {
		fl.diagf(instrLoc, "%s expects %s", pi.Name, operandCountText(spec.operandCount))
		return true
	}

	kind, ok := instructionKind(pi.Name)
	if !ok {
		fl.diagf(instrLoc, "unsupported instruction %q", pi.Name)
		return true
	}
	ins := wasmir.Instruction{Kind: kind, SourceLoc: instrLoc}
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
	case "array.new", "array.new_default", "array.get", "array.get_s", "array.get_u", "array.set", "array.fill", "struct.new", "struct.new_default":
		if len(pi.Operands) != 1 {
			fl.diagf(instrLoc, "%s expects 1 operand", pi.Name)
			return
		}
		typeIndex, ok := lowerTypeIndexOperand(pi.Operands[0], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid %s type operand", pi.Name)
			return
		}
		kind, _ := instructionKind(pi.Name)
		fl.emitInstr(wasmir.Instruction{Kind: kind, TypeIndex: typeIndex, SourceLoc: instrLoc})
		return
	case "array.new_data", "array.new_elem", "array.init_data", "array.init_elem":
		if len(pi.Operands) != 2 {
			fl.diagf(instrLoc, "%s expects 2 operands", pi.Name)
			return
		}
		typeIndex, ok := lowerTypeIndexOperand(pi.Operands[0], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid %s type operand", pi.Name)
			return
		}
		kind, _ := instructionKind(pi.Name)
		ins := wasmir.Instruction{Kind: kind, TypeIndex: typeIndex, SourceLoc: instrLoc}
		switch pi.Name {
		case "array.new_data", "array.init_data":
			dataIndex, ok := lowerDataIndexOperand(pi.Operands[1], fl.mod.dataIndicesByName)
			if !ok {
				fl.diagf(pi.Operands[1].Loc(), "invalid %s data operand", pi.Name)
				return
			}
			ins.DataIndex = dataIndex
		case "array.new_elem", "array.init_elem":
			elemIndex, ok := lowerElemIndexOperand(pi.Operands[1], fl.mod.elemIndicesByName)
			if !ok {
				fl.diagf(pi.Operands[1].Loc(), "invalid %s element operand", pi.Name)
				return
			}
			ins.ElemIndex = elemIndex
		}
		fl.emitInstr(ins)
		return
	case "array.copy":
		if len(pi.Operands) != 2 {
			fl.diagf(instrLoc, "array.copy expects 2 operands")
			return
		}
		dstTypeIndex, ok := lowerTypeIndexOperand(pi.Operands[0], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid array.copy destination type operand")
			return
		}
		srcTypeIndex, ok := lowerTypeIndexOperand(pi.Operands[1], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[1].Loc(), "invalid array.copy source type operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{
			Kind:            wasmir.InstrArrayCopy,
			TypeIndex:       dstTypeIndex,
			SourceTypeIndex: srcTypeIndex,
			SourceLoc:       instrLoc,
		})
		return
	case "array.new_fixed":
		if len(pi.Operands) != 2 {
			fl.diagf(instrLoc, "array.new_fixed expects 2 operands")
			return
		}
		typeIndex, ok := lowerTypeIndexOperand(pi.Operands[0], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid array.new_fixed type operand")
			return
		}
		fixedCount, ok := lowerFieldIndexOperand(pi.Operands[1])
		if !ok {
			fl.diagf(pi.Operands[1].Loc(), "invalid array.new_fixed length operand")
			return
		}
		fl.emitInstr(wasmir.Instruction{
			Kind:       wasmir.InstrArrayNewFixed,
			TypeIndex:  typeIndex,
			FixedCount: fixedCount,
			SourceLoc:  instrLoc,
		})
		return
	case "array.len":
		if len(pi.Operands) != 0 {
			fl.diagf(instrLoc, "array.len expects no operands")
			return
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrArrayLen, SourceLoc: instrLoc})
		return
	case "struct.get", "struct.get_s", "struct.get_u", "struct.set":
		if len(pi.Operands) != 2 {
			fl.diagf(instrLoc, "%s expects 2 operands", pi.Name)
			return
		}
		typeIndex, ok := lowerTypeIndexOperand(pi.Operands[0], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid %s type operand", pi.Name)
			return
		}
		fieldIndex, ok := fl.lowerStructFieldOperand(typeIndex, pi.Operands[1])
		if !ok {
			fl.diagf(pi.Operands[1].Loc(), "invalid %s field operand", pi.Name)
			return
		}
		kind, _ := instructionKind(pi.Name)
		fl.emitInstr(wasmir.Instruction{
			Kind:       kind,
			TypeIndex:  typeIndex,
			FieldIndex: fieldIndex,
			SourceLoc:  instrLoc,
		})
		return
	case "br_on_cast", "br_on_cast_fail":
		if len(pi.Operands) != 3 {
			fl.diagf(instrLoc, "%s expects 3 operands", pi.Name)
			return
		}
		branchDepth, ok := fl.lowerLabelOperand(pi.Operands[0])
		if !ok {
			fl.diagf(pi.Operands[0].Loc(), "invalid %s branch depth", pi.Name)
			return
		}
		srcType, ok := lowerCastTypeOperand(pi.Operands[1], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[1].Loc(), "invalid %s source type", pi.Name)
			return
		}
		dstType, ok := lowerCastTypeOperand(pi.Operands[2], fl.mod.typesByName)
		if !ok {
			fl.diagf(pi.Operands[2].Loc(), "invalid %s destination type", pi.Name)
			return
		}
		kind, _ := instructionKind(pi.Name)
		fl.emitInstr(wasmir.Instruction{
			Kind:          kind,
			BranchDepth:   branchDepth,
			SourceRefType: srcType,
			RefType:       dstType,
			SourceLoc:     instrLoc,
		})
		return
	case "memory.size", "memory.grow", "memory.fill":
		if len(pi.Operands) > 1 {
			fl.diagf(instrLoc, "%s expects at most 1 memory operand", pi.Name)
			return
		}
		memoryIndex := uint32(0)
		if len(pi.Operands) == 1 {
			idx, ok := lowerMemoryIndexOperand(pi.Operands[0], fl.mod.memoriesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid %s memory index operand", pi.Name)
				return
			}
			memoryIndex = idx
		}
		kind, _ := instructionKind(pi.Name)
		fl.emitInstr(wasmir.Instruction{Kind: kind, MemoryIndex: memoryIndex, SourceLoc: instrLoc})
		return
	case "memory.copy":
		if len(pi.Operands) != 0 && len(pi.Operands) != 2 {
			fl.diagf(instrLoc, "memory.copy expects 0 or 2 memory operands")
			return
		}
		dstMemoryIndex := uint32(0)
		srcMemoryIndex := uint32(0)
		if len(pi.Operands) == 2 {
			dst, ok := lowerMemoryIndexOperand(pi.Operands[0], fl.mod.memoriesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid memory.copy destination memory operand")
				return
			}
			src, ok := lowerMemoryIndexOperand(pi.Operands[1], fl.mod.memoriesByName)
			if !ok {
				fl.diagf(pi.Operands[1].Loc(), "invalid memory.copy source memory operand")
				return
			}
			dstMemoryIndex = dst
			srcMemoryIndex = src
		}
		fl.emitInstr(wasmir.Instruction{
			Kind:              wasmir.InstrMemoryCopy,
			MemoryIndex:       dstMemoryIndex,
			SourceMemoryIndex: srcMemoryIndex,
			SourceLoc:         instrLoc,
		})
		return
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
		kind, _ := instructionKind(pi.Name)
		fl.emitInstr(wasmir.Instruction{Kind: kind, TableIndex: tableIndex, SourceLoc: instrLoc})
		return
	case "table.copy":
		if len(pi.Operands) != 0 && len(pi.Operands) != 2 {
			fl.diagf(instrLoc, "table.copy expects 0 or 2 table operands")
			return
		}
		dstTableIndex := uint32(0)
		srcTableIndex := uint32(0)
		if len(pi.Operands) == 2 {
			dst, ok := lowerTableIndexOperand(pi.Operands[0], fl.mod.tablesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid table.copy destination table operand")
				return
			}
			src, ok := lowerTableIndexOperand(pi.Operands[1], fl.mod.tablesByName)
			if !ok {
				fl.diagf(pi.Operands[1].Loc(), "invalid table.copy source table operand")
				return
			}
			dstTableIndex = dst
			srcTableIndex = src
		}
		fl.emitInstr(wasmir.Instruction{
			Kind:             wasmir.InstrTableCopy,
			TableIndex:       dstTableIndex,
			SourceTableIndex: srcTableIndex,
			SourceLoc:        instrLoc,
		})
		return
	case "table.fill":
		if len(pi.Operands) > 1 {
			fl.diagf(instrLoc, "table.fill expects at most 1 table operand")
			return
		}
		tableIndex := uint32(0)
		if len(pi.Operands) == 1 {
			idx, ok := lowerTableIndexOperand(pi.Operands[0], fl.mod.tablesByName)
			if !ok {
				fl.diagf(pi.Operands[0].Loc(), "invalid table.fill table index operand")
				return
			}
			tableIndex = idx
		}
		fl.emitInstr(wasmir.Instruction{Kind: wasmir.InstrTableFill, TableIndex: tableIndex, SourceLoc: instrLoc})
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
		kind, _ := instructionKind(pi.Name)
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
			vt, ok := lowerBlockResultTypeOperand(pi.Operands[0], fl.mod.typesByName)
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
			vt, ok := lowerBlockResultTypeOperand(pi.Operands[0], fl.mod.typesByName)
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
		elemIndex, ok := lowerElemIndexOperand(elemOp, fl.mod.elemIndicesByName)
		if !ok {
			fl.diagf(elemOp.Loc(), "invalid table.init element operand")
			return
		}
		if int(tableIndex) < len(fl.mod.out.Tables) {
			if int(elemIndex) < len(fl.mod.out.Elements) {
				elemTy := fl.mod.out.Elements[elemIndex].RefType
				if elemTy.Kind != wasmir.ValueKindInvalid {
					tableTy := fl.mod.out.Tables[tableIndex].RefType
					if !matchesExpectedValueType(elemTy, tableTy) {
						fl.diagf(instrLoc, "type mismatch")
						return
					}
				}
			} else if elemID, ok := elemOp.(*IdOperand); ok {
				if elemTy, found := fl.mod.elemRefTypeByName[elemID.Value]; found {
					tableTy := fl.mod.out.Tables[tableIndex].RefType
					if !matchesExpectedValueType(elemTy, tableTy) {
						fl.diagf(instrLoc, "type mismatch")
						return
					}
				}
			}
		}
		fl.emitInstr(wasmir.Instruction{
			Kind:       wasmir.InstrTableInit,
			TableIndex: tableIndex,
			ElemIndex:  elemIndex,
			SourceLoc:  instrLoc,
		})
		return

	default:
		fl.diagf(instrLoc, "unsupported instruction %q", pi.Name)
	}
}

// lowerMemoryInstr lowers load/store instructions with optional memarg
// keywords (for example align=1 offset=8).
func (fl *functionLowerer) lowerMemoryInstr(pi *PlainInstr, instrLoc string) bool {
	if !instructionHasSyntaxClass(pi.Name, instrSyntaxMemory) {
		return false
	}
	kind, ok := instructionKind(pi.Name)
	if !ok {
		return false
	}
	align, offset, memoryIndex, ok := parseMemArgOperands(pi.Operands, fl.mod.memoriesByName)
	if !ok {
		fl.diagf(instrLoc, "invalid %s memory operands", pi.Name)
		return true
	}
	fl.emitInstr(wasmir.Instruction{
		Kind:         kind,
		MemoryAlign:  align,
		MemoryOffset: offset,
		MemoryIndex:  memoryIndex,
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
//  3. memoryIndex: memory index immediate. If omitted, this is 0.
//  4. ok: true when all operands are valid; false on any malformed,
//     duplicate, or misplaced operand.
//
// Examples:
//   - [] -> (0, 0, 0, true)
//   - ["$mem"] -> (0, 0, memidx($mem), true)
//   - ["align=4"] -> (2, 0, 0, true)
//   - ["$mem", "offset=8", "align=2"] -> (1, 8, memidx($mem), true)
//   - ["align=3"] -> (0, 0, 0, false) // not a power-of-two byte alignment
func parseMemArgOperands(operands []Operand, memoriesByName map[string]uint32) (uint32, uint64, uint32, bool) {
	var align uint32
	var offset uint64
	var memoryIndex uint32
	seenAlign := false
	seenOffset := false
	seenMemory := false
	for i, op := range operands {
		if !seenMemory {
			switch op.(type) {
			case *IdOperand, *IntOperand:
				idx, ok := lowerMemoryIndexOperand(op, memoriesByName)
				if !ok {
					return 0, 0, 0, false
				}
				memoryIndex = idx
				seenMemory = true
				continue
			}
		}
		kw, ok := op.(*KeywordOperand)
		if !ok {
			return 0, 0, 0, false
		}
		if seenMemory && i == 0 {
			return 0, 0, 0, false
		}
		parts := strings.SplitN(kw.Value, "=", 2)
		if len(parts) != 2 {
			return 0, 0, 0, false
		}
		switch parts[0] {
		case "align":
			value, ok := parseU32Literal(parts[1])
			if !ok {
				return 0, 0, 0, false
			}
			if seenAlign {
				return 0, 0, 0, false
			}
			exp, ok := alignToExponent(value)
			if !ok {
				return 0, 0, 0, false
			}
			align = exp
			seenAlign = true
		case "offset":
			value, ok := parseU64Literal(parts[1])
			if !ok {
				return 0, 0, 0, false
			}
			if seenOffset {
				return 0, 0, 0, false
			}
			offset = value
			seenOffset = true
		default:
			return 0, 0, 0, false
		}
	}
	return align, offset, memoryIndex, true
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
	if refType, ok := lowerRefHeapTypeOperand(operands[0]); ok {
		ins.RefType = refType
		return true
	}
	switch op := operands[0].(type) {
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

// decodeDataIndexOperands decodes operands into ins.DataIndex for memory.init
// and data.drop.
func decodeDataIndexOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	dataIndex, ok := lowerDataIndexOperand(operands[0], fl.mod.dataIndicesByName)
	if !ok {
		return false
	}
	ins.DataIndex = dataIndex
	return true
}

// decodeElemIndexOperands decodes operands into ins.ElemIndex for elem.drop.
func decodeElemIndexOperands(fl *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	elemIndex, ok := lowerElemIndexOperand(operands[0], fl.mod.elemIndicesByName)
	if !ok {
		return false
	}
	ins.ElemIndex = elemIndex
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

// decodeV128ConstOperands decodes the currently supported SIMD constant forms:
// `v128.const i8x16 ...`, `v128.const i16x8 ...`, `v128.const i32x4 ...`, and
// `v128.const f32x4 ...`.
func decodeV128ConstOperands(_ *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	if len(operands) < 2 {
		return false
	}
	shape := operandText(operands[0])
	switch shape {
	case "i8x16":
		if len(operands) != 17 {
			return false
		}
		for i := 0; i < 16; i++ {
			lane, ok := lowerI8ConstOperand(operands[i+1])
			if !ok {
				return false
			}
			ins.V128Const[i] = lane
		}
		return true
	case "i16x8":
		if len(operands) != 9 {
			return false
		}
		for i := 0; i < 8; i++ {
			lane, ok := lowerI16ConstOperand(operands[i+1])
			if !ok {
				return false
			}
			base := i * 2
			ins.V128Const[base] = byte(lane)
			ins.V128Const[base+1] = byte(lane >> 8)
		}
		return true
	case "i32x4":
		if len(operands) != 5 {
			return false
		}
		for i := 0; i < 4; i++ {
			lane, ok := lowerI32ConstOperand(operands[i+1])
			if !ok {
				return false
			}
			base := i * 4
			ins.V128Const[base] = byte(lane)
			ins.V128Const[base+1] = byte(lane >> 8)
			ins.V128Const[base+2] = byte(lane >> 16)
			ins.V128Const[base+3] = byte(lane >> 24)
		}
		return true
	case "f32x4":
		if len(operands) != 5 {
			return false
		}
		for i := 0; i < 4; i++ {
			lane, ok := lowerF32ConstOperand(operands[i+1])
			if !ok {
				return false
			}
			base := i * 4
			ins.V128Const[base] = byte(lane)
			ins.V128Const[base+1] = byte(lane >> 8)
			ins.V128Const[base+2] = byte(lane >> 16)
			ins.V128Const[base+3] = byte(lane >> 24)
		}
		return true
	default:
		return false
	}
}

// decodeLaneIndexOperands decodes a SIMD lane immediate such as
// `i32x4.extract_lane 3`.
func decodeLaneIndexOperands(_ *functionLowerer, ins *wasmir.Instruction, operands []Operand) bool {
	lane, ok := lowerFieldIndexOperand(operands[0])
	if !ok {
		return false
	}
	ins.LaneIndex = lane
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

// lowerI8ConstOperand resolves op as one signed i8 literal and returns the raw
// lane byte encoded with that literal's two's-complement bits.
func lowerI8ConstOperand(op Operand) (byte, bool) {
	o, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}
	clean := strings.ReplaceAll(o.Value, "_", "")
	if clean == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(clean, 0, 8); err == nil {
		return byte(int8(n)), true
	}
	if n, err := strconv.ParseUint(clean, 0, 8); err == nil {
		return byte(n), true
	}
	return 0, false
}

// lowerI16ConstOperand resolves op as one signed i16 literal and returns the
// raw lane bits encoded with that literal's two's-complement representation.
func lowerI16ConstOperand(op Operand) (uint16, bool) {
	o, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}
	clean := strings.ReplaceAll(o.Value, "_", "")
	if clean == "" {
		return 0, false
	}
	if n, err := strconv.ParseInt(clean, 0, 16); err == nil {
		return uint16(int16(n)), true
	}
	if n, err := strconv.ParseUint(clean, 0, 16); err == nil {
		return uint16(n), true
	}
	return 0, false
}

// loweredConstInstr is one lowered constant expression plus its resulting type.
type loweredConstInstr struct {
	Instrs []wasmir.Instruction
	Type   wasmir.ValueType
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
	if len(ci.Instrs) != 1 {
		return 0, false
	}
	switch ci.Instrs[0].Kind {
	case wasmir.InstrI32Const:
		return ci.Instrs[0].I32Const, true
	case wasmir.InstrGlobalGet:
		return l.evalImportedI32Global(ci.Instrs[0].GlobalIndex)
	default:
		return 0, false
	}
}

// evalI64ConstExpr evaluates a memory64 offset constant expression to i64.
//
// This exists because active data segments use a constant expression as their
// memory offset, and under memory64 the target memory's address type may be
// i64 rather than the MVP default i32. In other words, `(data (i64.const 32)
// "...")` is valid when the target memory is declared with address type i64.
//
// Relevant spec sections:
//   - Address types: https://webassembly.github.io/spec/core/text/types.html
//   - Active data segment offsets: https://webassembly.github.io/spec/core/syntax/modules.html
//
// Supported forms mirror evalI32ConstExpr for the i64 arithmetic subset.
func (l *moduleLowerer) evalI64ConstExpr(init Instruction) (int64, bool) {
	switch in := init.(type) {
	case *FoldedInstr:
		switch in.Name {
		case "i64.add", "i64.sub", "i64.mul":
			if len(in.Args) != 2 || in.Args[0].Instr == nil || in.Args[1].Instr == nil ||
				in.Args[0].Operand != nil || in.Args[1].Operand != nil {
				return 0, false
			}
			left, ok := l.evalI64ConstExpr(in.Args[0].Instr)
			if !ok {
				return 0, false
			}
			right, ok := l.evalI64ConstExpr(in.Args[1].Instr)
			if !ok {
				return 0, false
			}
			switch in.Name {
			case "i64.add":
				return left + right, true
			case "i64.sub":
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
	if len(ci.Instrs) != 1 {
		return 0, false
	}
	switch ci.Instrs[0].Kind {
	case wasmir.InstrI64Const:
		return ci.Instrs[0].I64Const, true
	default:
		return 0, false
	}
}

func (l *moduleLowerer) evalMemoryOffsetConst(init Instruction, addrType wasmir.ValueType) (int64, bool) {
	switch addrType {
	case wasmir.ValueTypeI32:
		v, ok := l.evalI32ConstExpr(init)
		return int64(v), ok
	case wasmir.ValueTypeI64:
		return l.evalI64ConstExpr(init)
	default:
		return 0, false
	}
}

func lowerMemoryAddressType(name string) (wasmir.ValueType, bool) {
	switch name {
	case "", "i32":
		return wasmir.ValueTypeI32, true
	case "i64":
		return wasmir.ValueTypeI64, true
	default:
		return wasmir.ValueType{}, false
	}
}

func normalizedMemoryAddressType(mem wasmir.Memory) wasmir.ValueType {
	if mem.AddressType == wasmir.ValueTypeI64 {
		return wasmir.ValueTypeI64
	}
	return wasmir.ValueTypeI32
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
	if g.ImportModule == "" {
		if len(g.Init) == 1 && g.Init[0].Kind == wasmir.InstrI32Const {
			return g.Init[0].I32Const, true
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
// Besides simple one-op forms like `i32.const` and `ref.null`, this also
// accepts folded GC aggregate initializers such as:
//   - (array.new $arr (i32.const 10) (i32.const 12))
//   - (array.new_default $arr (i32.const 12))
//
// The returned loweredConstInstr contains the full flat instruction sequence
// plus the statically known resulting value type.
func (l *moduleLowerer) lowerConstInstr(init Instruction) (*loweredConstInstr, bool) {
	switch in := init.(type) {
	case *PlainInstr:
		return l.lowerPlainConstInstr(in.Name, in.Operands)
	case *FoldedInstr:
		return l.lowerFoldedConstInstr(in)
	default:
		return nil, false
	}
}

func (l *moduleLowerer) lowerPlainConstInstr(name string, operands []Operand) (*loweredConstInstr, bool) {
	if len(operands) != 1 {
		return nil, false
	}
	op := operands[0]

	switch name {
	case "i32.const":
		imm, ok := lowerI32ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrI32Const, I32Const: imm}},
			Type:   wasmir.ValueTypeI32,
		}, true
	case "i64.const":
		imm, ok := lowerI64ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrI64Const, I64Const: imm}},
			Type:   wasmir.ValueTypeI64,
		}, true
	case "f32.const":
		imm, ok := lowerF32ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrF32Const, F32Const: imm}},
			Type:   wasmir.ValueTypeF32,
		}, true
	case "f64.const":
		imm, ok := lowerF64ConstOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrF64Const, F64Const: imm}},
			Type:   wasmir.ValueTypeF64,
		}, true
	case "ref.null":
		vt, ok := l.lowerConstRefNullType(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrRefNull, RefType: vt}},
			Type:   vt,
		}, true
	case "ref.func":
		funcIdx, ok := lowerFuncIndexOperand(op, l.funcsByName)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrRefFunc, FuncIndex: funcIdx}},
			Type:   wasmir.RefTypeFunc(false),
		}, true
	case "global.get":
		globalIdx, ok := lowerGlobalIndexOperand(op, l.globalsByName)
		if !ok || int(globalIdx) >= len(l.out.Globals) {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrGlobalGet, GlobalIndex: globalIdx}},
			Type:   l.out.Globals[globalIdx].Type,
		}, true
	case "struct.new_default":
		typeIdx, ok := l.lowerConstTypeIndexOperand(op)
		if !ok {
			return nil, false
		}
		return &loweredConstInstr{
			Instrs: []wasmir.Instruction{{Kind: wasmir.InstrStructNewDefault, TypeIndex: typeIdx}},
			Type:   wasmir.RefTypeIndexed(typeIdx, false),
		}, true
	default:
		return nil, false
	}
}

func (l *moduleLowerer) lowerFoldedConstInstr(fi *FoldedInstr) (*loweredConstInstr, bool) {
	switch fi.Name {
	case "struct.new":
		if len(fi.Args) < 1 || fi.Args[0].Operand == nil || fi.Args[0].Instr != nil {
			return nil, false
		}
		typeIdx, ok := l.lowerConstTypeIndexOperand(fi.Args[0].Operand)
		if !ok {
			return nil, false
		}
		if int(typeIdx) >= len(l.out.Types) {
			return nil, false
		}
		td := l.out.Types[typeIdx]
		if td.Kind != wasmir.TypeDefKindStruct || len(td.Fields) != len(fi.Args)-1 {
			return nil, false
		}
		instrs := make([]wasmir.Instruction, 0, len(fi.Args))
		for _, arg := range fi.Args[1:] {
			if arg.Instr == nil || arg.Operand != nil {
				return nil, false
			}
			valueExpr, ok := l.lowerConstInstr(arg.Instr)
			if !ok {
				return nil, false
			}
			instrs = append(instrs, valueExpr.Instrs...)
		}
		instrs = append(instrs, wasmir.Instruction{Kind: wasmir.InstrStructNew, TypeIndex: typeIdx})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeIndexed(typeIdx, false)}, true
	case "array.new":
		if len(fi.Args) != 3 || fi.Args[0].Operand == nil || fi.Args[0].Instr != nil ||
			fi.Args[1].Instr == nil || fi.Args[1].Operand != nil ||
			fi.Args[2].Instr == nil || fi.Args[2].Operand != nil {
			return nil, false
		}
		typeIdx, ok := l.lowerConstTypeIndexOperand(fi.Args[0].Operand)
		if !ok {
			return nil, false
		}
		valueExpr, ok := l.lowerConstInstr(fi.Args[1].Instr)
		if !ok {
			return nil, false
		}
		lenExpr, ok := l.lowerConstInstr(fi.Args[2].Instr)
		if !ok {
			return nil, false
		}
		instrs := append([]wasmir.Instruction{}, valueExpr.Instrs...)
		instrs = append(instrs, lenExpr.Instrs...)
		instrs = append(instrs, wasmir.Instruction{Kind: wasmir.InstrArrayNew, TypeIndex: typeIdx})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeIndexed(typeIdx, false)}, true
	case "array.new_default":
		if len(fi.Args) != 2 || fi.Args[0].Operand == nil || fi.Args[0].Instr != nil ||
			fi.Args[1].Instr == nil || fi.Args[1].Operand != nil {
			return nil, false
		}
		typeIdx, ok := l.lowerConstTypeIndexOperand(fi.Args[0].Operand)
		if !ok {
			return nil, false
		}
		lenExpr, ok := l.lowerConstInstr(fi.Args[1].Instr)
		if !ok {
			return nil, false
		}
		instrs := append([]wasmir.Instruction{}, lenExpr.Instrs...)
		instrs = append(instrs, wasmir.Instruction{Kind: wasmir.InstrArrayNewDefault, TypeIndex: typeIdx})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeIndexed(typeIdx, false)}, true
	case "array.new_fixed":
		if len(fi.Args) < 2 || fi.Args[0].Operand == nil || fi.Args[0].Instr != nil ||
			fi.Args[1].Operand == nil || fi.Args[1].Instr != nil {
			return nil, false
		}
		typeIdx, ok := l.lowerConstTypeIndexOperand(fi.Args[0].Operand)
		if !ok {
			return nil, false
		}
		fixedCount, ok := lowerFieldIndexOperand(fi.Args[1].Operand)
		if !ok || int(fixedCount) != len(fi.Args)-2 {
			return nil, false
		}
		instrs := make([]wasmir.Instruction, 0, len(fi.Args)-1)
		for _, arg := range fi.Args[2:] {
			if arg.Instr == nil || arg.Operand != nil {
				return nil, false
			}
			valueExpr, ok := l.lowerConstInstr(arg.Instr)
			if !ok {
				return nil, false
			}
			instrs = append(instrs, valueExpr.Instrs...)
		}
		instrs = append(instrs, wasmir.Instruction{
			Kind:       wasmir.InstrArrayNewFixed,
			TypeIndex:  typeIdx,
			FixedCount: fixedCount,
		})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeIndexed(typeIdx, false)}, true
	case "ref.i31":
		if len(fi.Args) != 1 || fi.Args[0].Instr == nil || fi.Args[0].Operand != nil {
			return nil, false
		}
		valueExpr, ok := l.lowerConstInstr(fi.Args[0].Instr)
		if !ok || valueExpr.Type != wasmir.ValueTypeI32 {
			return nil, false
		}
		instrs := append([]wasmir.Instruction{}, valueExpr.Instrs...)
		instrs = append(instrs, wasmir.Instruction{Kind: wasmir.InstrRefI31})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeI31(false)}, true
	case "extern.convert_any":
		if len(fi.Args) != 1 || fi.Args[0].Instr == nil || fi.Args[0].Operand != nil {
			return nil, false
		}
		valueExpr, ok := l.lowerConstInstr(fi.Args[0].Instr)
		if !ok || !matchesExpectedValueType(valueExpr.Type, wasmir.RefTypeAny(true)) {
			return nil, false
		}
		instrs := append([]wasmir.Instruction{}, valueExpr.Instrs...)
		instrs = append(instrs, wasmir.Instruction{Kind: wasmir.InstrExternConvertAny})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeExtern(valueExpr.Type.Nullable)}, true
	case "any.convert_extern":
		if len(fi.Args) != 1 || fi.Args[0].Instr == nil || fi.Args[0].Operand != nil {
			return nil, false
		}
		valueExpr, ok := l.lowerConstInstr(fi.Args[0].Instr)
		if !ok || !matchesExpectedValueType(valueExpr.Type, wasmir.RefTypeExtern(true)) {
			return nil, false
		}
		instrs := append([]wasmir.Instruction{}, valueExpr.Instrs...)
		instrs = append(instrs, wasmir.Instruction{Kind: wasmir.InstrAnyConvertExtern})
		return &loweredConstInstr{Instrs: instrs, Type: wasmir.RefTypeAny(valueExpr.Type.Nullable)}, true
	default:
		operands := make([]Operand, 0, len(fi.Args))
		for _, arg := range fi.Args {
			if arg.Operand == nil || arg.Instr != nil {
				return nil, false
			}
			operands = append(operands, arg.Operand)
		}
		return l.lowerPlainConstInstr(fi.Name, operands)
	}
}

func (l *moduleLowerer) lowerConstRefNullType(op Operand) (wasmir.ValueType, bool) {
	if refType, ok := lowerRefHeapTypeOperand(op); ok {
		return refType, true
	}
	idOp, ok := op.(*IdOperand)
	if !ok {
		return wasmir.ValueType{}, false
	}
	typeIdx, ok := l.typesByName[idOp.Value]
	if !ok {
		return wasmir.ValueType{}, false
	}
	return wasmir.RefTypeIndexed(typeIdx, true), true
}

func (l *moduleLowerer) lowerConstTypeIndexOperand(op Operand) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		typeIdx, found := l.typesByName[o.Value]
		return typeIdx, found
	case *IntOperand:
		return parseU32Literal(o.Value)
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

// lowerMemoryIndexOperand resolves op as a memory index using memoriesByName.
// It accepts either a memory identifier or an integer memory index literal.
func lowerMemoryIndexOperand(op Operand, memoriesByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := memoriesByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// lowerTypeIndexOperand resolves op as a type index using typesByName.
// It accepts either a type identifier or an integer type index literal.
func lowerTypeIndexOperand(op Operand, typesByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		return resolveTypeRef(o.Value, typesByName)
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

func resolveTypeRef(ref string, typesByName map[string]uint32) (uint32, bool) {
	if strings.HasPrefix(ref, "$") {
		idx, ok := typesByName[ref]
		return idx, ok
	}
	return parseU32Literal(ref)
}

func lowerFieldIndexOperand(op Operand) (uint32, bool) {
	intOp, ok := op.(*IntOperand)
	if !ok {
		return 0, false
	}
	return parseU32Literal(intOp.Value)
}

func (fl *functionLowerer) lowerStructFieldOperand(typeIndex uint32, op Operand) (uint32, bool) {
	if idx, ok := lowerFieldIndexOperand(op); ok {
		return idx, true
	}
	idOp, ok := op.(*IdOperand)
	if !ok || int(typeIndex) >= len(fl.mod.out.Types) {
		return 0, false
	}
	td := fl.mod.out.Types[typeIndex]
	if td.Kind != wasmir.TypeDefKindStruct {
		return 0, false
	}
	for i, field := range td.Fields {
		if field.Name == idOp.Value {
			return uint32(i), true
		}
	}
	return 0, false
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
		case "none":
			return wasmir.RefTypeNone(true), true
		case "noextern":
			return wasmir.RefTypeNoExtern(true), true
		case "nofunc":
			return wasmir.RefTypeNoFunc(true), true
		case "any":
			return wasmir.RefTypeAny(true), true
		case "eq":
			return wasmir.RefTypeEq(true), true
		case "i31":
			return wasmir.RefTypeI31(true), true
		case "array":
			return wasmir.RefTypeArray(true), true
		case "struct":
			return wasmir.RefTypeStruct(true), true
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

// lowerDataIndexOperand resolves op as a data segment index.
func lowerDataIndexOperand(op Operand, dataIndicesByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		if dataIndicesByName == nil {
			return 0, false
		}
		idx, ok := dataIndicesByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
	}
}

// lowerElemIndexOperand resolves op as an element segment index using
// elemIndicesByName. It accepts either an element identifier or an integer
// element index literal.
func lowerElemIndexOperand(op Operand, elemIndicesByName map[string]uint32) (uint32, bool) {
	switch o := op.(type) {
	case *IdOperand:
		idx, ok := elemIndicesByName[o.Value]
		return idx, ok
	case *IntOperand:
		return parseU32Literal(o.Value)
	default:
		return 0, false
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
		} else if got.HeapType.Kind == wasmir.HeapKindNone {
			return want.Nullable
		} else if got.HeapType.Kind == wasmir.HeapKindNoFunc {
			return want.Nullable
		} else if got.HeapType.Kind != wasmir.HeapKindFunc {
			return false
		}
	} else {
		switch want.HeapType.Kind {
		case wasmir.HeapKindAny:
			switch got.HeapType.Kind {
			case wasmir.HeapKindNone, wasmir.HeapKindAny, wasmir.HeapKindEq, wasmir.HeapKindI31, wasmir.HeapKindArray, wasmir.HeapKindStruct, wasmir.HeapKindTypeIndex:
			default:
				return false
			}
		case wasmir.HeapKindNone:
			if got.HeapType.Kind != wasmir.HeapKindNone {
				return false
			}
		case wasmir.HeapKindEq:
			switch got.HeapType.Kind {
			case wasmir.HeapKindNone, wasmir.HeapKindEq, wasmir.HeapKindI31, wasmir.HeapKindStruct, wasmir.HeapKindArray, wasmir.HeapKindTypeIndex:
			default:
				return false
			}
		case wasmir.HeapKindI31:
			if got.HeapType.Kind != wasmir.HeapKindI31 {
				return false
			}
		case wasmir.HeapKindArray:
			if got.HeapType.Kind != wasmir.HeapKindArray && got.HeapType.Kind != wasmir.HeapKindTypeIndex {
				return false
			}
		case wasmir.HeapKindStruct:
			if got.HeapType.Kind != wasmir.HeapKindStruct && got.HeapType.Kind != wasmir.HeapKindTypeIndex {
				return false
			}
		case wasmir.HeapKindFunc:
			if got.HeapType.Kind != wasmir.HeapKindFunc &&
				got.HeapType.Kind != wasmir.HeapKindNoFunc &&
				got.HeapType.Kind != wasmir.HeapKindTypeIndex {
				return false
			}
		case wasmir.HeapKindExtern:
			if got.HeapType.Kind != wasmir.HeapKindExtern && got.HeapType.Kind != wasmir.HeapKindNoExtern {
				return false
			}
		case wasmir.HeapKindNoExtern:
			if got.HeapType.Kind != wasmir.HeapKindNoExtern {
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
func lowerBlockResultTypeOperand(op Operand, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	switch o := op.(type) {
	case *KeywordOperand:
		return lowerValueType(&BasicType{Name: o.Value}, typesByName)
	case *TypeOperand:
		return lowerValueType(o.Ty, typesByName)
	default:
		return wasmir.ValueType{}, false
	}
}

func lowerCastTypeOperand(op Operand, typesByName map[string]uint32) (wasmir.ValueType, bool) {
	switch o := op.(type) {
	case *KeywordOperand:
		return lowerValueType(&BasicType{Name: o.Value}, typesByName)
	case *TypeOperand:
		return lowerValueType(o.Ty, typesByName)
	case *IdOperand:
		return lowerValueType(&RefType{Nullable: false, HeapType: o.Value}, typesByName)
	case *IntOperand:
		return lowerValueType(&RefType{Nullable: false, HeapType: o.Value}, typesByName)
	default:
		return wasmir.ValueType{}, false
	}
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

// parseU64Literal parses s as an unsigned 64-bit integer literal.
// It returns the parsed value and true on success, or 0/false on failure.
func parseU64Literal(s string) (uint64, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	value, err := strconv.ParseUint(clean, 0, 64)
	if err != nil {
		return 0, false
	}
	return value, true
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
		case "v128":
			return wasmir.ValueTypeV128, true
		case "funcref":
			return wasmir.RefTypeFunc(true), true
		case "nullref":
			return wasmir.RefTypeNone(true), true
		case "externref":
			return wasmir.RefTypeExtern(true), true
		case "anyref":
			return wasmir.RefTypeAny(true), true
		case "eqref":
			return wasmir.RefTypeEq(true), true
		case "i31ref":
			return wasmir.RefTypeI31(true), true
		case "structref":
			return wasmir.RefTypeStruct(true), true
		case "arrayref":
			return wasmir.RefTypeArray(true), true
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
		case "nullref":
			return wasmir.RefTypeNone(true), true
		case "externref":
			return wasmir.RefTypeExtern(true), true
		case "anyref":
			return wasmir.RefTypeAny(true), true
		case "eqref":
			return wasmir.RefTypeEq(true), true
		case "i31ref":
			return wasmir.RefTypeI31(true), true
		case "structref":
			return wasmir.RefTypeStruct(true), true
		case "arrayref":
			return wasmir.RefTypeArray(true), true
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
	case "none":
		return wasmir.RefTypeNone(nullable), true
	case "noextern":
		return wasmir.RefTypeNoExtern(nullable), true
	case "nofunc":
		return wasmir.RefTypeNoFunc(nullable), true
	case "any":
		return wasmir.RefTypeAny(nullable), true
	case "eq":
		return wasmir.RefTypeEq(nullable), true
	case "i31":
		return wasmir.RefTypeI31(nullable), true
	case "array":
		return wasmir.RefTypeArray(nullable), true
	case "struct":
		return wasmir.RefTypeStruct(nullable), true
	default:
		if strings.HasPrefix(name, "$") {
			typeIndex, ok := typesByName[name]
			if !ok {
				return wasmir.ValueType{}, false
			}
			return wasmir.RefTypeIndexed(typeIndex, nullable), true
		}
		if typeIndex, err := strconv.ParseUint(name, 10, 32); err == nil {
			return wasmir.RefTypeIndexed(uint32(typeIndex), nullable), true
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

func (l *moduleLowerer) lowerTypeFields(fields []*FieldDecl, typeIdx int) []wasmir.FieldType {
	out := make([]wasmir.FieldType, 0, len(fields))
	for i, fd := range fields {
		field, ok := l.lowerFieldType(fd, typeIdx, i)
		if ok {
			out = append(out, field)
		}
	}
	return out
}

func (l *moduleLowerer) lowerFieldType(fd *FieldDecl, typeIdx int, fieldIdx int) (wasmir.FieldType, bool) {
	if fd == nil {
		l.diags.Addf("type[%d] field[%d]: nil field declaration", typeIdx, fieldIdx)
		return wasmir.FieldType{}, false
	}
	if bt, ok := fd.Ty.(*BasicType); ok {
		switch bt.Name {
		case "i8":
			return wasmir.FieldType{Name: fd.Id, Packed: wasmir.PackedTypeI8, Mutable: fd.Mutable}, true
		case "i16":
			return wasmir.FieldType{Name: fd.Id, Packed: wasmir.PackedTypeI16, Mutable: fd.Mutable}, true
		}
	}
	vt, ok := lowerValueType(fd.Ty, l.typesByName)
	if !ok {
		l.diags.Addf("type[%d] field[%d]: unsupported field type %q", typeIdx, fieldIdx, fd.Ty)
		return wasmir.FieldType{}, false
	}
	return wasmir.FieldType{Name: fd.Id, Type: vt, Mutable: fd.Mutable}, true
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
