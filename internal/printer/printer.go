package printer

import (
	"bytes"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

// PrintModule renders m as WebAssembly text format.
//
// This printer currently targets a readable canonical form for core modules.
// It may reject IR features whose text emission is not implemented yet.
func PrintModule(m *wasmir.Module) ([]byte, error) {
	return PrintModuleWithOptions(m, DefaultOptions())
}

// Options configures WAT printing.
type Options struct {
	// IndentText is repeated once per indentation level.
	IndentText string

	// NameUnnamed synthesizes names for otherwise unnamed index-space entries.
	NameUnnamed bool

	// Skeleton elides function bodies and data/element payloads with "...".
	Skeleton bool
}

// DefaultOptions returns the printer's default formatting options.
func DefaultOptions() Options {
	return Options{IndentText: "  "}
}

// PrintModuleWithOptions renders m as WebAssembly text format using opts.
func PrintModuleWithOptions(m *wasmir.Module, opts Options) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}
	p := modulePrinter{m: m, opts: opts}
	if err := p.printModule(); err != nil {
		return nil, err
	}
	return p.buf.Bytes(), nil
}

type modulePrinter struct {
	m    *wasmir.Module
	opts Options
	buf  bytes.Buffer
}

// printModule is the entry point into module printing.
func (p *modulePrinter) printModule() error {
	p.buf.WriteString("(module")
	if p.m.Name != "" {
		p.buf.WriteByte(' ')
		p.buf.WriteString(formatID(p.m.Name))
	}
	p.buf.WriteByte('\n')

	if err := p.printTypes(); err != nil {
		return err
	}
	if err := p.printImports(); err != nil {
		return err
	}
	if err := p.printDefinedTables(); err != nil {
		return err
	}
	if err := p.printDefinedMemories(); err != nil {
		return err
	}
	if err := p.printDefinedGlobals(); err != nil {
		return err
	}
	if err := p.printDefinedTags(); err != nil {
		return err
	}
	if err := p.printFuncs(); err != nil {
		return err
	}
	if err := p.printExports(); err != nil {
		return err
	}
	if err := p.printStart(); err != nil {
		return err
	}
	if err := p.printElements(); err != nil {
		return err
	}
	if err := p.printData(); err != nil {
		return err
	}

	p.buf.WriteString(")\n")
	return nil
}

func (p *modulePrinter) printTypes() error {
	for i := 0; i < len(p.m.Types); i++ {
		groupSize := int(p.m.Types[i].RecGroupSize)
		if groupSize > 0 {
			if i+groupSize > len(p.m.Types) {
				return fmt.Errorf("recursive type group at %d has invalid size %d", i, groupSize)
			}
			// Module.Types is flattened, but the text format groups recursive
			// types under one wrapper:
			//   (rec
			//     (type $a (sub (struct ...)))
			//     (type $b (sub $a (struct ...))))
			p.writeIndent(1)
			p.buf.WriteString("(rec\n")
			for j := 0; j < groupSize; j++ {
				if err := p.printTypeDef(i+j, 2); err != nil {
					return err
				}
			}
			p.writeIndent(1)
			p.buf.WriteString(")\n")
			i += groupSize - 1
			continue
		}
		if err := p.printTypeDef(i, 1); err != nil {
			return err
		}
	}
	return nil
}

// printTypeDef emits one `(type ...)` declaration at the requested indentation.
func (p *modulePrinter) printTypeDef(typeIdx int, indent int) error {
	td := p.m.Types[typeIdx]
	p.writeIndent(indent)
	p.buf.WriteString("(type")
	if name := p.typeDeclName(typeIdx); name != "" {
		p.buf.WriteByte(' ')
		p.buf.WriteString(name)
	}
	p.buf.WriteByte(' ')
	if td.SubType {
		// Subtype metadata wraps the ordinary composite type body:
		//   (type $child (sub final $base (struct (field i32))))
		p.buf.WriteString("(sub")
		if td.Final {
			p.buf.WriteString(" final")
		}
		for _, super := range td.SuperTypes {
			p.buf.WriteByte(' ')
			p.buf.WriteString(p.typeRefText(super))
		}
		p.buf.WriteByte(' ')
		if err := p.writeTypeBody(typeIdx, td); err != nil {
			return err
		}
		p.buf.WriteString("))\n")
		return nil
	}
	if err := p.writeTypeBody(typeIdx, td); err != nil {
		return err
	}
	p.buf.WriteString(")\n")
	return nil
}

// writeTypeBody appends a function, struct, or array type body. typeIdx is the
// Module.Types index of td and is used when synthesizing struct field names.
func (p *modulePrinter) writeTypeBody(typeIdx int, td wasmir.TypeDef) error {
	switch td.Kind {
	case wasmir.TypeDefKindFunc:
		p.buf.WriteString("(func")
		p.writeParamDecls(nil, td.Params, false)
		p.writeResultDecls(td.Results)
		p.buf.WriteByte(')')
	case wasmir.TypeDefKindStruct:
		p.buf.WriteString("(struct")
		for i, field := range td.Fields {
			p.buf.WriteByte(' ')
			p.buf.WriteString("(field")
			if name := p.fieldDeclName(typeIdx, i); name != "" {
				p.buf.WriteByte(' ')
				p.buf.WriteString(name)
			}
			p.buf.WriteByte(' ')
			p.buf.WriteString(p.fieldTypeText(field))
			p.buf.WriteByte(')')
		}
		p.buf.WriteByte(')')
	case wasmir.TypeDefKindArray:
		p.buf.WriteString("(array ")
		p.buf.WriteString(p.fieldTypeText(td.ElemField))
		p.buf.WriteByte(')')
	default:
		return fmt.Errorf("unsupported type kind %d", td.Kind)
	}
	return nil
}

func (p *modulePrinter) printImports() error {
	var funcIdx, globalIdx, tagIdx uint32
	for _, imp := range p.m.Imports {
		p.writeIndent(1)
		p.buf.WriteString("(import ")
		p.buf.WriteString(quoteString([]byte(imp.Module)))
		p.buf.WriteByte(' ')
		p.buf.WriteString(quoteString([]byte(imp.Name)))
		p.buf.WriteByte(' ')
		switch imp.Kind {
		case wasmir.ExternalKindFunction:
			td, err := p.funcType(imp.TypeIdx)
			if err != nil {
				return err
			}
			p.buf.WriteString("(func")
			if name := p.funcDeclName(funcIdx); name != "" {
				p.buf.WriteByte(' ')
				p.buf.WriteString(name)
			}
			p.buf.WriteString(p.typeUseText(imp.TypeIdx))
			p.writeParamDecls(nil, td.Params, false)
			p.writeResultDecls(td.Results)
			p.buf.WriteString("))\n")
			funcIdx++
		case wasmir.ExternalKindTable:
			p.buf.WriteString("(table")
			p.writeTableType(imp.Table)
			p.buf.WriteString("))\n")
		case wasmir.ExternalKindMemory:
			p.buf.WriteString("(memory")
			p.writeMemoryType(imp.Memory)
			p.buf.WriteString("))\n")
		case wasmir.ExternalKindGlobal:
			p.buf.WriteString("(global")
			if name := p.globalDeclName(globalIdx); name != "" {
				p.buf.WriteByte(' ')
				p.buf.WriteString(name)
			}
			p.buf.WriteByte(' ')
			p.buf.WriteString(p.globalTypeText(imp.GlobalType, imp.GlobalMutable))
			p.buf.WriteString("))\n")
			globalIdx++
		case wasmir.ExternalKindTag:
			p.buf.WriteString("(tag")
			if name := p.tagDeclName(tagIdx); name != "" {
				p.buf.WriteByte(' ')
				p.buf.WriteString(name)
			}
			p.buf.WriteString(p.typeUseText(imp.TypeIdx))
			p.buf.WriteString("))\n")
			tagIdx++
		default:
			return fmt.Errorf("unsupported import kind %d", imp.Kind)
		}
	}
	return nil
}

func (p *modulePrinter) printDefinedTables() error {
	for _, table := range p.m.Tables {
		if table.ImportModule != "" {
			continue
		}
		p.writeIndent(1)
		p.buf.WriteString("(table")
		p.writeTableType(table)
		if len(table.Init) > 0 {
			expr, err := p.formatConstExpr(table.Init)
			if err != nil {
				return fmt.Errorf("table init: %w", err)
			}
			p.buf.WriteByte(' ')
			p.buf.WriteString(expr)
		}
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printDefinedMemories() error {
	for _, mem := range p.m.Memories {
		if mem.ImportModule != "" {
			continue
		}
		p.writeIndent(1)
		p.buf.WriteString("(memory")
		p.writeMemoryType(mem)
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printDefinedGlobals() error {
	for i, g := range p.m.Globals {
		if g.ImportModule != "" {
			continue
		}
		init, err := p.formatConstExpr(g.Init)
		if err != nil {
			return fmt.Errorf("global init: %w", err)
		}
		p.writeIndent(1)
		p.buf.WriteString("(global")
		if name := p.globalDeclName(uint32(i)); name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(name)
		}
		p.buf.WriteByte(' ')
		p.buf.WriteString(p.globalTypeText(g.Type, g.Mutable))
		p.buf.WriteByte(' ')
		p.buf.WriteString(init)
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printDefinedTags() error {
	importedTags := p.importedTagCount()
	for i, tag := range p.m.Tags {
		if tag.ImportModule != "" {
			continue
		}
		p.writeIndent(1)
		p.buf.WriteString("(tag")
		if name := p.tagDeclName(importedTags + uint32(i)); name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(name)
		}
		p.buf.WriteString(p.typeUseText(tag.TypeIdx))
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printFuncs() error {
	importedFuncs := p.importedFunctionCount()
	for i, fn := range p.m.Funcs {
		td, err := p.funcType(fn.TypeIdx)
		if err != nil {
			return err
		}
		p.writeIndent(1)
		p.buf.WriteString("(func")
		if name := p.funcDeclName(importedFuncs + uint32(i)); name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(name)
		}
		p.buf.WriteString(p.typeUseText(fn.TypeIdx))
		p.writeParamDecls(fn.ParamNames, td.Params, true)
		p.writeResultDecls(td.Results)
		if p.opts.Skeleton {
			p.buf.WriteString(" ...)\n")
			continue
		}
		p.writeLocalDecls(fn.LocalNames, fn.Locals, uint32(len(td.Params)))
		body := fn.Body
		if len(body) > 0 && body[len(body)-1].Kind == wasmir.InstrEnd {
			body = body[:len(body)-1]
		}
		if len(body) == 0 {
			p.buf.WriteString(")\n")
			continue
		}
		p.buf.WriteByte('\n')
		if err := p.printBody(body, &fn); err != nil {
			return err
		}
		p.writeIndent(1)
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printExports() error {
	for _, exp := range p.m.Exports {
		p.writeIndent(1)
		p.buf.WriteString("(export ")
		p.buf.WriteString(quoteString([]byte(exp.Name)))
		p.buf.WriteString(" (")
		switch exp.Kind {
		case wasmir.ExternalKindFunction:
			p.buf.WriteString("func ")
			p.buf.WriteString(p.funcRefText(exp.Index))
		case wasmir.ExternalKindTable:
			p.buf.WriteString("table ")
			p.buf.WriteString(strconv.FormatUint(uint64(exp.Index), 10))
		case wasmir.ExternalKindMemory:
			p.buf.WriteString("memory ")
			p.buf.WriteString(strconv.FormatUint(uint64(exp.Index), 10))
		case wasmir.ExternalKindGlobal:
			p.buf.WriteString("global ")
			p.buf.WriteString(p.globalRefText(exp.Index))
		case wasmir.ExternalKindTag:
			p.buf.WriteString("tag ")
			p.buf.WriteString(p.tagRefText(exp.Index))
		default:
			return fmt.Errorf("unsupported export kind %d", exp.Kind)
		}
		p.buf.WriteString("))\n")
	}
	return nil
}

func (p *modulePrinter) printStart() error {
	if p.m.StartFuncIndex == nil {
		return nil
	}
	p.writeIndent(1)
	p.buf.WriteString("(start ")
	p.buf.WriteString(p.funcRefText(*p.m.StartFuncIndex))
	p.buf.WriteString(")\n")
	return nil
}

func (p *modulePrinter) printElements() error {
	for _, elem := range p.m.Elements {
		p.writeIndent(1)
		p.buf.WriteString("(elem")
		switch elem.Mode {
		case wasmir.ElemSegmentModeDeclarative:
			p.buf.WriteString(" declare")
		case wasmir.ElemSegmentModePassive:
		case wasmir.ElemSegmentModeActive:
			p.buf.WriteString(" (table ")
			p.buf.WriteString(strconv.FormatUint(uint64(elem.TableIndex), 10))
			p.buf.WriteByte(')')
			offset, err := p.formatConstExpr(elemOffsetExprOrSynthetic(elem))
			if err != nil {
				return fmt.Errorf("elem offset: %w", err)
			}
			p.buf.WriteString(" (offset ")
			p.buf.WriteString(offset)
			p.buf.WriteByte(')')
		default:
			return fmt.Errorf("unsupported element mode %d", elem.Mode)
		}

		if p.opts.Skeleton && (len(elem.Exprs) > 0 || len(elem.FuncIndices) > 0) {
			p.buf.WriteString(" ...)\n")
			continue
		}
		if len(elem.Exprs) > 0 {
			p.buf.WriteByte(' ')
			p.buf.WriteString(p.valueTypeText(elem.RefType))
			for _, expr := range elem.Exprs {
				text, err := p.formatElemItemExpr(expr)
				if err != nil {
					return err
				}
				p.buf.WriteByte(' ')
				p.buf.WriteString("(item ")
				p.buf.WriteString(text)
				p.buf.WriteByte(')')
			}
		} else if len(elem.FuncIndices) == 0 {
			// Empty segments must keep their explicit element type, e.g.
			// `(elem funcref)`, because the `func` shorthand denotes the legacy
			// function-index payload form rather than a typed empty payload.
			p.buf.WriteByte(' ')
			p.buf.WriteString(p.valueTypeText(elem.RefType))
		} else {
			p.buf.WriteString(" func")
			for _, idx := range elem.FuncIndices {
				p.buf.WriteByte(' ')
				p.buf.WriteString(p.funcRefText(idx))
			}
		}
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printData() error {
	for _, seg := range p.m.Data {
		p.writeIndent(1)
		p.buf.WriteString("(data")
		if seg.Mode == wasmir.DataSegmentModeActive {
			if seg.MemoryIndex != 0 {
				p.buf.WriteString(" (memory ")
				p.buf.WriteString(strconv.FormatUint(uint64(seg.MemoryIndex), 10))
				p.buf.WriteByte(')')
			}
			offset, err := p.formatConstExpr(dataOffsetExprOrSynthetic(seg))
			if err != nil {
				return fmt.Errorf("data offset: %w", err)
			}
			p.buf.WriteString(" (offset ")
			p.buf.WriteString(offset)
			p.buf.WriteByte(')')
		}
		if p.opts.Skeleton {
			p.buf.WriteString(" ...)\n")
			continue
		}
		p.buf.WriteByte(' ')
		p.buf.WriteString(quoteString(seg.Init))
		p.buf.WriteString(")\n")
	}
	return nil
}

// printBody emits a function body as one instruction per line with indentation
// driven by structured control instructions.
func (p *modulePrinter) printBody(body []wasmir.Instruction, fn *wasmir.Function) error {
	indent := 2
	for _, ins := range body {
		switch ins.Kind {
		case wasmir.InstrElse, wasmir.InstrEnd:
			indent--
			if indent < 2 {
				indent = 2
			}
		}
		p.writeIndent(indent)
		text, err := p.instructionText(ins, fn)
		if err != nil {
			return err
		}
		p.buf.WriteString(text)
		p.buf.WriteByte('\n')
		switch ins.Kind {
		case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf, wasmir.InstrTryTable:
			indent++
		case wasmir.InstrElse:
			indent++
		}
	}
	return nil
}

// instructionText formats a single instruction in linear WAT syntax.
func (p *modulePrinter) instructionText(ins wasmir.Instruction, fn *wasmir.Function) (string, error) {
	def, ok := instrdef.LookupInstructionByKind(ins.Kind)
	if !ok {
		return "", fmt.Errorf("unsupported instruction kind %d", ins.Kind)
	}
	name := def.TextName
	switch ins.Kind {
	case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf:
		return name + p.blockTypeText(ins), nil
	case wasmir.InstrTryTable:
		return p.tryTableText(name, ins)
	case wasmir.InstrElse, wasmir.InstrEnd:
		return name, nil
	case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
		return fmt.Sprintf("%s %s", name, p.localRefText(fn, ins.LocalIndex)), nil
	case wasmir.InstrCall, wasmir.InstrReturnCall:
		return fmt.Sprintf("%s %s", name, p.funcRefText(ins.FuncIndex)), nil
	case wasmir.InstrCallRef, wasmir.InstrReturnCallRef:
		return fmt.Sprintf("%s %s", name, p.typeRefText(ins.CallTypeIndex)), nil
	case wasmir.InstrThrow:
		return fmt.Sprintf("%s %s", name, p.tagRefText(ins.TagIndex)), nil
	case wasmir.InstrBr, wasmir.InstrBrIf, wasmir.InstrBrOnNull, wasmir.InstrBrOnNonNull:
		return fmt.Sprintf("%s %d", name, ins.BranchDepth), nil
	case wasmir.InstrBrTable:
		var b strings.Builder
		b.WriteString(name)
		for _, depth := range ins.BranchTable {
			b.WriteByte(' ')
			b.WriteString(strconv.FormatUint(uint64(depth), 10))
		}
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(uint64(ins.BranchDefault), 10))
		return b.String(), nil
	case wasmir.InstrGlobalGet, wasmir.InstrGlobalSet:
		return fmt.Sprintf("%s %s", name, p.globalRefText(ins.GlobalIndex)), nil
	case wasmir.InstrRefFunc:
		return fmt.Sprintf("%s %s", name, p.funcRefText(ins.FuncIndex)), nil
	case wasmir.InstrRefNull:
		return fmt.Sprintf("%s %s", name, p.heapTypeText(ins.RefType.HeapType)), nil
	case wasmir.InstrRefTest, wasmir.InstrRefCast:
		return fmt.Sprintf("%s %s", name, p.valueTypeText(ins.RefType)), nil
	case wasmir.InstrBrOnCast, wasmir.InstrBrOnCastFail:
		return fmt.Sprintf("%s %d %s %s", name, ins.BranchDepth, p.valueTypeText(ins.SourceRefType), p.valueTypeText(ins.RefType)), nil
	case wasmir.InstrSelect:
		if ins.SelectType == nil {
			return name, nil
		}
		return fmt.Sprintf("%s (result %s)", name, p.valueTypeText(*ins.SelectType)), nil
	case wasmir.InstrCallIndirect, wasmir.InstrReturnCallIndirect:
		var b strings.Builder
		b.WriteString(name)
		if ins.TableIndex != 0 {
			b.WriteByte(' ')
			b.WriteString(strconv.FormatUint(uint64(ins.TableIndex), 10))
		}
		b.WriteString(p.typeUseText(ins.CallTypeIndex))
		return b.String(), nil
	case wasmir.InstrI32Const:
		return fmt.Sprintf("%s %d", name, ins.I32Const), nil
	case wasmir.InstrI64Const:
		return fmt.Sprintf("%s %d", name, ins.I64Const), nil
	case wasmir.InstrF32Const:
		return fmt.Sprintf("%s %s", name, formatF32(ins.F32Const)), nil
	case wasmir.InstrF64Const:
		return fmt.Sprintf("%s %s", name, formatF64(ins.F64Const)), nil
	case wasmir.InstrV128Const:
		return fmt.Sprintf("%s i8x16 %s", name, formatV128(ins.V128Const)), nil
	case wasmir.InstrMemorySize, wasmir.InstrMemoryGrow, wasmir.InstrMemoryFill:
		if ins.MemoryIndex == 0 {
			return name, nil
		}
		return fmt.Sprintf("%s %d", name, ins.MemoryIndex), nil
	case wasmir.InstrMemoryCopy:
		if ins.MemoryIndex == 0 && ins.SourceMemoryIndex == 0 {
			return name, nil
		}
		return fmt.Sprintf("%s %d %d", name, ins.MemoryIndex, ins.SourceMemoryIndex), nil
	case wasmir.InstrMemoryInit:
		if ins.MemoryIndex == 0 {
			return fmt.Sprintf("%s %d", name, ins.DataIndex), nil
		}
		return fmt.Sprintf("%s %d %d", name, ins.MemoryIndex, ins.DataIndex), nil
	case wasmir.InstrDataDrop:
		return fmt.Sprintf("%s %d", name, ins.DataIndex), nil
	case wasmir.InstrTableGet, wasmir.InstrTableSet, wasmir.InstrTableGrow, wasmir.InstrTableSize, wasmir.InstrTableFill:
		if ins.TableIndex == 0 {
			return name, nil
		}
		return fmt.Sprintf("%s %d", name, ins.TableIndex), nil
	case wasmir.InstrTableCopy:
		if ins.TableIndex == 0 && ins.SourceTableIndex == 0 {
			return name, nil
		}
		return fmt.Sprintf("%s %d %d", name, ins.TableIndex, ins.SourceTableIndex), nil
	case wasmir.InstrTableInit:
		if ins.TableIndex == 0 {
			return fmt.Sprintf("%s %d", name, ins.ElemIndex), nil
		}
		return fmt.Sprintf("%s %d %d", name, ins.TableIndex, ins.ElemIndex), nil
	case wasmir.InstrElemDrop:
		return fmt.Sprintf("%s %d", name, ins.ElemIndex), nil
	}

	switch ins.Kind {
	case wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane,
		wasmir.InstrV128Store8Lane, wasmir.InstrV128Store16Lane, wasmir.InstrV128Store32Lane, wasmir.InstrV128Store64Lane:
		return memoryInstrText(name, ins) + " " + strconv.FormatUint(uint64(ins.LaneIndex), 10), nil
	case wasmir.InstrStructNew, wasmir.InstrStructNewDefault, wasmir.InstrArrayNew,
		wasmir.InstrArrayNewDefault, wasmir.InstrArrayGet, wasmir.InstrArrayGetS, wasmir.InstrArrayGetU,
		wasmir.InstrArraySet, wasmir.InstrArrayFill:
		return fmt.Sprintf("%s %s", name, p.typeRefText(ins.TypeIndex)), nil
	case wasmir.InstrStructGet, wasmir.InstrStructGetS, wasmir.InstrStructGetU, wasmir.InstrStructSet:
		return fmt.Sprintf("%s %s %s", name, p.typeRefText(ins.TypeIndex), p.fieldRefText(ins.TypeIndex, ins.FieldIndex)), nil
	case wasmir.InstrArrayNewData, wasmir.InstrArrayInitData:
		return fmt.Sprintf("%s %s %d", name, p.typeRefText(ins.TypeIndex), ins.DataIndex), nil
	case wasmir.InstrArrayNewElem, wasmir.InstrArrayInitElem:
		return fmt.Sprintf("%s %s %d", name, p.typeRefText(ins.TypeIndex), ins.ElemIndex), nil
	case wasmir.InstrArrayNewFixed:
		return fmt.Sprintf("%s %s %d", name, p.typeRefText(ins.TypeIndex), ins.FixedCount), nil
	case wasmir.InstrArrayCopy:
		return fmt.Sprintf("%s %s %s", name, p.typeRefText(ins.TypeIndex), p.typeRefText(ins.SourceTypeIndex)), nil
	case wasmir.InstrI8x16Shuffle:
		return fmt.Sprintf("%s %s", name, formatShuffleLanes(ins.ShuffleLanes)), nil
	case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU, wasmir.InstrI8x16ReplaceLane,
		wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU, wasmir.InstrI16x8ReplaceLane,
		wasmir.InstrI32x4ExtractLane, wasmir.InstrI32x4ReplaceLane, wasmir.InstrI64x2ExtractLane,
		wasmir.InstrI64x2ReplaceLane, wasmir.InstrF32x4ExtractLane, wasmir.InstrF32x4ReplaceLane,
		wasmir.InstrF64x2ExtractLane, wasmir.InstrF64x2ReplaceLane:
		return fmt.Sprintf("%s %d", name, ins.LaneIndex), nil
	}

	if def.Text.SyntaxClass == instrdef.InstrSyntaxMemory {
		return memoryInstrText(name, ins), nil
	}

	if def.Text.SyntaxClass == instrdef.InstrSyntaxPlain {
		return name, nil
	}
	return "", fmt.Errorf("printing %s is not implemented yet", name)
}

// tryTableText formats a flat try_table header including its catch clauses.
func (p *modulePrinter) tryTableText(name string, ins wasmir.Instruction) (string, error) {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString(p.blockTypeText(ins))
	for _, catch := range ins.TryTableCatches {
		catchText, err := p.tryTableCatchText(catch)
		if err != nil {
			return "", err
		}
		b.WriteByte(' ')
		b.WriteString(catchText)
	}
	return b.String(), nil
}

// tryTableCatchText formats one try_table catch clause.
func (p *modulePrinter) tryTableCatchText(catch wasmir.TryTableCatch) (string, error) {
	label := strconv.FormatUint(uint64(catch.LabelDepth), 10)
	switch catch.Kind {
	case wasmir.TryTableCatchKindTag:
		return fmt.Sprintf("(catch %s %s)", p.tagRefText(catch.TagIndex), label), nil
	case wasmir.TryTableCatchKindTagRef:
		return fmt.Sprintf("(catch_ref %s %s)", p.tagRefText(catch.TagIndex), label), nil
	case wasmir.TryTableCatchKindAll:
		return fmt.Sprintf("(catch_all %s)", label), nil
	case wasmir.TryTableCatchKindAllRef:
		return fmt.Sprintf("(catch_all_ref %s)", label), nil
	default:
		return "", fmt.Errorf("unsupported try_table catch kind %d", catch.Kind)
	}
}

// funcType resolves a function type index and verifies that it names a func
// type definition.
func (p *modulePrinter) funcType(typeIdx uint32) (wasmir.TypeDef, error) {
	if int(typeIdx) >= len(p.m.Types) {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d out of range", typeIdx)
	}
	td := p.m.Types[typeIdx]
	if td.Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d is not a function type", typeIdx)
	}
	return td, nil
}

// writeIndent writes one indentation unit per level.
func (p *modulePrinter) writeIndent(level int) {
	for i := 0; i < level; i++ {
		p.buf.WriteString(p.opts.IndentText)
	}
}

// writeParamDecls appends parameter declarations to the printer buffer,
// including names when available. synthesize controls whether unnamed
// parameters receive synthetic local names.
func (p *modulePrinter) writeParamDecls(names []string, params []wasmir.ValueType, synthesize bool) {
	for i, vt := range params {
		p.buf.WriteString(" (param")
		if name := p.localDeclName(names, i, uint32(i), synthesize); name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(name)
		}
		p.buf.WriteByte(' ')
		p.buf.WriteString(p.valueTypeText(vt))
		p.buf.WriteByte(')')
	}
}

// writeResultDecls appends result declarations to the printer buffer.
func (p *modulePrinter) writeResultDecls(results []wasmir.ValueType) {
	for _, vt := range results {
		p.buf.WriteString(" (result ")
		p.buf.WriteString(p.valueTypeText(vt))
		p.buf.WriteByte(')')
	}
}

// writeLocalDecls appends local declarations to the printer buffer, including
// names when available. paramCount is used to compute each local's index in the
// combined parameter/local index space.
func (p *modulePrinter) writeLocalDecls(names []string, locals []wasmir.ValueType, paramCount uint32) {
	for i, vt := range locals {
		p.buf.WriteString(" (local")
		if name := p.localDeclName(names, i, paramCount+uint32(i), true); name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(name)
		}
		p.buf.WriteByte(' ')
		p.buf.WriteString(p.valueTypeText(vt))
		p.buf.WriteByte(')')
	}
}

// writeTableType appends the textual form of a table type to the printer
// buffer.
func (p *modulePrinter) writeTableType(table wasmir.Table) {
	if table.AddressType == wasmir.ValueTypeI64 {
		p.buf.WriteString(" i64")
	}
	p.buf.WriteByte(' ')
	p.buf.WriteString(strconv.FormatUint(table.Min, 10))
	if table.Max != nil {
		p.buf.WriteByte(' ')
		p.buf.WriteString(strconv.FormatUint(*table.Max, 10))
	}
	p.buf.WriteByte(' ')
	p.buf.WriteString(p.valueTypeText(table.RefType))
}

// writeMemoryType appends the textual form of a memory type to the printer
// buffer.
func (p *modulePrinter) writeMemoryType(mem wasmir.Memory) {
	if mem.AddressType == wasmir.ValueTypeI64 {
		p.buf.WriteString(" i64")
	}
	p.buf.WriteByte(' ')
	p.buf.WriteString(strconv.FormatUint(mem.Min, 10))
	if mem.Max != nil {
		p.buf.WriteByte(' ')
		p.buf.WriteString(strconv.FormatUint(*mem.Max, 10))
	}
}

// globalTypeText returns the WAT spelling of a global type, including
// mutability.
func (p *modulePrinter) globalTypeText(vt wasmir.ValueType, mutable bool) string {
	if !mutable {
		return p.valueTypeText(vt)
	}
	return "(mut " + p.valueTypeText(vt) + ")"
}

// fieldTypeText returns the WAT spelling of a struct or array field type.
func (p *modulePrinter) fieldTypeText(ft wasmir.FieldType) string {
	var storage string
	switch ft.Packed {
	case wasmir.PackedTypeI8:
		storage = "i8"
	case wasmir.PackedTypeI16:
		storage = "i16"
	default:
		storage = p.valueTypeText(ft.Type)
	}
	if !ft.Mutable {
		return storage
	}
	return "(mut " + storage + ")"
}

// typeUseText formats a type use.
func (p *modulePrinter) typeUseText(typeIdx uint32) string {
	return " (type " + p.typeRefText(typeIdx) + ")"
}

// blockTypeText formats the optional block type annotation for structured
// control instructions.
func (p *modulePrinter) blockTypeText(ins wasmir.Instruction) string {
	if ins.BlockTypeUsesIndex {
		return p.typeUseText(ins.BlockTypeIndex)
	}
	if ins.BlockType == nil {
		return ""
	}
	return " (result " + p.valueTypeText(*ins.BlockType) + ")"
}

// valueTypeText returns the textual name of a value type.
func (p *modulePrinter) valueTypeText(vt wasmir.ValueType) string {
	switch vt.Kind {
	case wasmir.ValueKindI32, wasmir.ValueKindI64, wasmir.ValueKindF32, wasmir.ValueKindF64, wasmir.ValueKindV128:
		return vt.String()
	case wasmir.ValueKindRef:
		switch vt.HeapType.Kind {
		case wasmir.HeapKindFunc:
			if vt.Nullable {
				return "funcref"
			}
		case wasmir.HeapKindExtern:
			if vt.Nullable {
				return "externref"
			}
		case wasmir.HeapKindNone:
			if vt.Nullable {
				return "nullref"
			}
		case wasmir.HeapKindNoExtern:
			if vt.Nullable {
				return "nullexternref"
			}
		case wasmir.HeapKindNoFunc:
			if vt.Nullable {
				return "nullfuncref"
			}
		case wasmir.HeapKindExn:
			if vt.Nullable {
				return "exnref"
			}
		case wasmir.HeapKindNoExn:
			if vt.Nullable {
				return "nullexnref"
			}
		case wasmir.HeapKindAny:
			if vt.Nullable {
				return "anyref"
			}
		case wasmir.HeapKindEq:
			if vt.Nullable {
				return "eqref"
			}
		case wasmir.HeapKindI31:
			if vt.Nullable {
				return "i31ref"
			}
		case wasmir.HeapKindArray:
			if vt.Nullable {
				return "arrayref"
			}
		case wasmir.HeapKindStruct:
			if vt.Nullable {
				return "structref"
			}
		}
		if vt.Nullable {
			return "(ref null " + p.heapTypeText(vt.HeapType) + ")"
		}
		return "(ref " + p.heapTypeText(vt.HeapType) + ")"
	default:
		return vt.String()
	}
}

// heapTypeText returns the textual spelling of a heap type, using type names
// when available for indexed heap types.
func (p *modulePrinter) heapTypeText(ht wasmir.HeapType) string {
	switch ht.Kind {
	case wasmir.HeapKindFunc:
		return "func"
	case wasmir.HeapKindExtern:
		return "extern"
	case wasmir.HeapKindNone:
		return "none"
	case wasmir.HeapKindNoExtern:
		return "noextern"
	case wasmir.HeapKindNoFunc:
		return "nofunc"
	case wasmir.HeapKindExn:
		return "exn"
	case wasmir.HeapKindNoExn:
		return "noexn"
	case wasmir.HeapKindAny:
		return "any"
	case wasmir.HeapKindEq:
		return "eq"
	case wasmir.HeapKindI31:
		return "i31"
	case wasmir.HeapKindArray:
		return "array"
	case wasmir.HeapKindStruct:
		return "struct"
	case wasmir.HeapKindTypeIndex:
		return p.typeRefText(ht.TypeIndex)
	default:
		return fmt.Sprintf("heaptype(kind=%d)", ht.Kind)
	}
}

// typeRefText formats a type reference using the type's name when available,
// or its numeric index otherwise.
func (p *modulePrinter) typeRefText(typeIdx uint32) string {
	if p.m != nil && int(typeIdx) < len(p.m.Types) && p.m.Types[typeIdx].Name != "" {
		return formatID(p.m.Types[typeIdx].Name)
	}
	if p.opts.NameUnnamed {
		return syntheticName("type", typeIdx)
	}
	return strconv.FormatUint(uint64(typeIdx), 10)
}

// typeDeclName returns the optional printed name for a type declaration.
func (p *modulePrinter) typeDeclName(typeIdx int) string {
	if p.m != nil && typeIdx < len(p.m.Types) && p.m.Types[typeIdx].Name != "" {
		return formatID(p.m.Types[typeIdx].Name)
	}
	if p.opts.NameUnnamed {
		return syntheticName("type", uint32(typeIdx))
	}
	return ""
}

// funcRefText formats a function reference using the function's name when it
// is available on a defined function, or the numeric index otherwise.
func (p *modulePrinter) funcRefText(funcIdx uint32) string {
	importedFuncs := p.importedFunctionCount()
	if funcIdx >= importedFuncs {
		definedIdx := funcIdx - importedFuncs
		if p.m != nil && int(definedIdx) < len(p.m.Funcs) && p.m.Funcs[definedIdx].Name != "" {
			return formatID(p.m.Funcs[definedIdx].Name)
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("func", funcIdx)
	}
	return strconv.FormatUint(uint64(funcIdx), 10)
}

// funcDeclName returns the optional printed name for a function declaration.
func (p *modulePrinter) funcDeclName(funcIdx uint32) string {
	importedFuncs := p.importedFunctionCount()
	if funcIdx >= importedFuncs {
		definedIdx := funcIdx - importedFuncs
		if p.m != nil && int(definedIdx) < len(p.m.Funcs) && p.m.Funcs[definedIdx].Name != "" {
			return formatID(p.m.Funcs[definedIdx].Name)
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("func", funcIdx)
	}
	return ""
}

// globalRefText formats a global reference using the global's name when it is
// available, or the numeric index otherwise.
func (p *modulePrinter) globalRefText(globalIdx uint32) string {
	if p.m != nil && int(globalIdx) < len(p.m.Globals) && p.m.Globals[globalIdx].Name != "" {
		return formatID(p.m.Globals[globalIdx].Name)
	}
	if p.opts.NameUnnamed {
		return syntheticName("global", globalIdx)
	}
	return strconv.FormatUint(uint64(globalIdx), 10)
}

// globalDeclName returns the optional printed name for a global declaration.
func (p *modulePrinter) globalDeclName(globalIdx uint32) string {
	if p.m != nil && int(globalIdx) < len(p.m.Globals) && p.m.Globals[globalIdx].Name != "" {
		return formatID(p.m.Globals[globalIdx].Name)
	}
	if p.opts.NameUnnamed {
		return syntheticName("global", globalIdx)
	}
	return ""
}

// tagRefText formats a tag reference using the tag's name when it is
// available on a defined tag, or the numeric index otherwise.
func (p *modulePrinter) tagRefText(tagIdx uint32) string {
	importedTags := p.importedTagCount()
	if tagIdx >= importedTags {
		definedIdx := tagIdx - importedTags
		if p.m != nil && int(definedIdx) < len(p.m.Tags) && p.m.Tags[definedIdx].Name != "" {
			return formatID(p.m.Tags[definedIdx].Name)
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("tag", tagIdx)
	}
	return strconv.FormatUint(uint64(tagIdx), 10)
}

// tagDeclName returns the optional printed name for a tag declaration.
func (p *modulePrinter) tagDeclName(tagIdx uint32) string {
	importedTags := p.importedTagCount()
	if tagIdx >= importedTags {
		definedIdx := tagIdx - importedTags
		if p.m != nil && int(definedIdx) < len(p.m.Tags) && p.m.Tags[definedIdx].Name != "" {
			return formatID(p.m.Tags[definedIdx].Name)
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("tag", tagIdx)
	}
	return ""
}

// fieldRefText formats a struct field reference using the field's name when it
// is available, or the numeric field index otherwise.
func (p *modulePrinter) fieldRefText(typeIdx uint32, fieldIdx uint32) string {
	if p.m != nil && int(typeIdx) < len(p.m.Types) {
		td := p.m.Types[typeIdx]
		if int(fieldIdx) < len(td.Fields) && td.Fields[fieldIdx].Name != "" {
			return formatID(td.Fields[fieldIdx].Name)
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("field", fieldIdx)
	}
	return strconv.FormatUint(uint64(fieldIdx), 10)
}

// fieldDeclName returns the optional printed name for a struct field declaration.
func (p *modulePrinter) fieldDeclName(typeIdx int, fieldIdx int) string {
	if p.m != nil && typeIdx < len(p.m.Types) {
		td := p.m.Types[typeIdx]
		if fieldIdx < len(td.Fields) && td.Fields[fieldIdx].Name != "" {
			return formatID(td.Fields[fieldIdx].Name)
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("field", uint32(fieldIdx))
	}
	return ""
}

// localDeclName returns the optional printed name for a parameter or local
// declaration. localIdx is the function-local index space where parameters come
// first.
func (p *modulePrinter) localDeclName(names []string, sliceIdx int, localIdx uint32, synthesize bool) string {
	if sliceIdx < len(names) && names[sliceIdx] != "" {
		return formatID(names[sliceIdx])
	}
	if synthesize && p.opts.NameUnnamed {
		return syntheticName("local", localIdx)
	}
	return ""
}

func (p *modulePrinter) importedFunctionCount() uint32 {
	if p.m == nil {
		return 0
	}
	var n uint32
	for _, imp := range p.m.Imports {
		if imp.Kind == wasmir.ExternalKindFunction {
			n++
		}
	}
	return n
}

func (p *modulePrinter) importedTagCount() uint32 {
	if p.m == nil {
		return 0
	}
	var n uint32
	for _, imp := range p.m.Imports {
		if imp.Kind == wasmir.ExternalKindTag {
			n++
		}
	}
	return n
}

// formatID prints name as a WAT identifier, using $"..." syntax when the
// decoded identifier text cannot appear as a plain `$name` token.
//
// For example:
//
//	$fg            -> $fg
//	$ random \n x  -> $" random \n x"
//	$           -> $""
func formatID(name string) string {
	if name == "" {
		return ""
	}
	if !strings.HasPrefix(name, "$") {
		name = "$" + name
	}
	if isPlainWATID(name[1:]) {
		return name
	}
	return "$" + quoteString([]byte(name[1:]))
}

// isPlainWATID reports whether s can be printed directly after '$' without
// switching to the quoted $"..." identifier form.
func isPlainWATID(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if '0' <= r && r <= '9' || 'A' <= r && r <= 'Z' || 'a' <= r && r <= 'z' {
			continue
		}
		switch r {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '/':
			continue
		case ':', '<', '=', '>', '?', '@', '\\', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

// formatConstExpr formats a constant expression as a flat WAT instruction
// sequence, matching wasm-tools' default print style.
func (p *modulePrinter) formatConstExpr(expr []wasmir.Instruction) (string, error) {
	if len(expr) == 0 {
		return "", fmt.Errorf("empty const expression")
	}
	if expr[len(expr)-1].Kind == wasmir.InstrEnd {
		expr = expr[:len(expr)-1]
	}
	if len(expr) == 0 {
		return "", fmt.Errorf("empty const expression")
	}
	return p.formatConstExprInstructions(expr)
}

// formatElemItemExpr formats a single element-segment item expression.
func (p *modulePrinter) formatElemItemExpr(expr []wasmir.Instruction) (string, error) {
	if len(expr) == 0 {
		return "", fmt.Errorf("empty elem item expression")
	}
	if expr[len(expr)-1].Kind == wasmir.InstrEnd {
		expr = expr[:len(expr)-1]
	}
	if len(expr) == 0 {
		return "", fmt.Errorf("empty elem item expression")
	}
	return p.formatConstExprInstructions(expr)
}

// formatConstExprInstructions prints a constant expression as a space-separated
// flat instruction sequence.
func (p *modulePrinter) formatConstExprInstructions(expr []wasmir.Instruction) (string, error) {
	parts := make([]string, 0, len(expr))
	for _, ins := range expr {
		text, err := p.instructionTextNoContext(ins)
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " "), nil
}

// instructionTextNoContext formats an instruction outside a function body, so
// only module-level name resolution is available.
func (p *modulePrinter) instructionTextNoContext(ins wasmir.Instruction) (string, error) {
	return p.instructionText(ins, nil)
}

// memoryInstrText formats a memory instruction including optional memory index,
// offset, and alignment immediates.
func memoryInstrText(name string, ins wasmir.Instruction) string {
	var b strings.Builder
	b.WriteString(name)
	if ins.MemoryIndex != 0 {
		b.WriteByte(' ')
		b.WriteString(strconv.FormatUint(uint64(ins.MemoryIndex), 10))
	}
	if ins.MemoryOffset != 0 {
		b.WriteString(" offset=")
		b.WriteString(strconv.FormatUint(ins.MemoryOffset, 10))
	}
	if ins.MemoryAlign != 0 {
		b.WriteString(" align=")
		b.WriteString(strconv.FormatUint(uint64(1)<<ins.MemoryAlign, 10))
	}
	return b.String()
}

// localRefText resolves a local or parameter index to a printed identifier when
// a name is available, or falls back to the numeric index.
func (p *modulePrinter) localRefText(fn *wasmir.Function, index uint32) string {
	if fn == nil {
		return strconv.FormatUint(uint64(index), 10)
	}
	paramCount := uint32(0)
	if p.m != nil && int(fn.TypeIdx) < len(p.m.Types) && p.m.Types[fn.TypeIdx].Kind == wasmir.TypeDefKindFunc {
		paramCount = uint32(len(p.m.Types[fn.TypeIdx].Params))
	}
	if index < paramCount && int(index) < len(fn.ParamNames) && fn.ParamNames[index] != "" {
		return formatID(fn.ParamNames[index])
	}
	if index >= paramCount {
		localIdx := index - paramCount
		if int(localIdx) < len(fn.LocalNames) && fn.LocalNames[localIdx] != "" {
			return formatID(fn.LocalNames[localIdx])
		}
	}
	if p.opts.NameUnnamed {
		return syntheticName("local", index)
	}
	return strconv.FormatUint(uint64(index), 10)
}

func syntheticName(namespace string, idx uint32) string {
	return fmt.Sprintf("$#%s%d", namespace, idx)
}

// formatF32 formats an f32 constant from its raw IEEE-754 bits.
func formatF32(bits uint32) string {
	if bits&0x7f800000 == 0x7f800000 && bits&0x007fffff != 0 {
		payload := bits & 0x007fffff
		if bits&0x80000000 != 0 {
			return fmt.Sprintf("-nan:0x%x", payload)
		}
		return fmt.Sprintf("nan:0x%x", payload)
	}
	v := float64(math.Float32frombits(bits))
	return formatFloat(v, 32)
}

// formatF64 formats an f64 constant from its raw IEEE-754 bits.
func formatF64(bits uint64) string {
	if bits&0x7ff0000000000000 == 0x7ff0000000000000 && bits&0x000fffffffffffff != 0 {
		payload := bits & 0x000fffffffffffff
		if bits&0x8000000000000000 != 0 {
			return fmt.Sprintf("-nan:0x%x", payload)
		}
		return fmt.Sprintf("nan:0x%x", payload)
	}
	return formatFloat(math.Float64frombits(bits), 64)
}

// formatFloat formats a floating-point constant using WAT spellings for NaN and
// infinities.
func formatFloat(v float64, bitSize int) string {
	switch {
	case math.IsNaN(v):
		return "nan"
	case math.IsInf(v, 1):
		return "inf"
	case math.IsInf(v, -1):
		return "-inf"
	default:
		return strconv.FormatFloat(v, 'g', -1, bitSize)
	}
}

// formatV128 formats a v128 immediate as sixteen decimal byte lanes.
func formatV128(bytes16 [16]byte) string {
	parts := make([]string, 0, 16)
	for _, b := range bytes16 {
		parts = append(parts, strconv.FormatUint(uint64(b), 10))
	}
	return strings.Join(parts, " ")
}

// formatShuffleLanes formats the lane immediates used by i8x16.shuffle.
func formatShuffleLanes(lanes [16]byte) string {
	return formatV128(lanes)
}

// quoteString formats data bytes as a quoted WAT string literal.
func quoteString(data []byte) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range data {
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c >= 0x20 && c <= 0x7e {
				b.WriteByte(c)
				continue
			}
			fmt.Fprintf(&b, "\\%02x", c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// elemOffsetExprOrSynthetic returns the explicit element offset expression when
// present, or synthesizes one from the decoded legacy offset fields.
func elemOffsetExprOrSynthetic(seg wasmir.ElementSegment) []wasmir.Instruction {
	if len(seg.OffsetExpr) > 0 {
		return seg.OffsetExpr
	}
	switch seg.OffsetType {
	case wasmir.ValueTypeI64:
		return []wasmir.Instruction{
			{Kind: wasmir.InstrI64Const, I64Const: seg.OffsetI64},
			{Kind: wasmir.InstrEnd},
		}
	default:
		return []wasmir.Instruction{
			{Kind: wasmir.InstrI32Const, I32Const: int32(seg.OffsetI64)},
			{Kind: wasmir.InstrEnd},
		}
	}
}

// dataOffsetExprOrSynthetic returns the explicit data offset expression when
// present, or synthesizes one from the decoded legacy offset fields.
func dataOffsetExprOrSynthetic(seg wasmir.DataSegment) []wasmir.Instruction {
	if len(seg.OffsetExpr) > 0 {
		return seg.OffsetExpr
	}
	switch seg.OffsetType {
	case wasmir.ValueTypeI64:
		return []wasmir.Instruction{
			{Kind: wasmir.InstrI64Const, I64Const: seg.OffsetI64},
			{Kind: wasmir.InstrEnd},
		}
	default:
		return []wasmir.Instruction{
			{Kind: wasmir.InstrI32Const, I32Const: int32(seg.OffsetI64)},
			{Kind: wasmir.InstrEnd},
		}
	}
}
