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
			for j := range groupSize {
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
		// appendInstrText renders into the buffer's own spare capacity, so it
		// must not write to p.buf while holding that slice.
		text, err := p.appendInstrText(p.buf.AvailableBuffer(), ins, fn)
		if err != nil {
			return err
		}
		p.buf.Write(text)
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
	text, err := p.appendInstrText(nil, ins, fn)
	if err != nil {
		return "", err
	}
	return string(text), nil
}

// appendInstrText formats a single instruction in linear WAT syntax. On error
// the contents of the returned slice are unspecified.
func (p *modulePrinter) appendInstrText(dst []byte, ins wasmir.Instruction, fn *wasmir.Function) ([]byte, error) {
	def, ok := instrdef.LookupInstructionByKind(ins.Kind)
	if !ok {
		return dst, fmt.Errorf("unsupported instruction kind %d", ins.Kind)
	}
	name := def.TextName
	switch ins.Kind {
	case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf:
		return p.appendBlockType(append(dst, name...), ins), nil
	case wasmir.InstrTryTable:
		return p.appendTryTable(dst, name, ins)
	case wasmir.InstrElse, wasmir.InstrEnd:
		return append(dst, name...), nil
	case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
		return p.appendLocalRef(appendNameSpace(dst, name), fn, ins.LocalIndex), nil
	case wasmir.InstrCall, wasmir.InstrReturnCall, wasmir.InstrRefFunc:
		return p.appendFuncRef(appendNameSpace(dst, name), ins.FuncIndex), nil
	case wasmir.InstrCallRef, wasmir.InstrReturnCallRef:
		return p.appendTypeRef(appendNameSpace(dst, name), ins.CallTypeIndex), nil
	case wasmir.InstrThrow:
		return p.appendTagRef(appendNameSpace(dst, name), ins.TagIndex), nil
	case wasmir.InstrBr, wasmir.InstrBrIf, wasmir.InstrBrOnNull, wasmir.InstrBrOnNonNull:
		return appendNameU(dst, name, uint64(ins.BranchDepth)), nil
	case wasmir.InstrBrTable:
		dst = append(dst, name...)
		for _, depth := range ins.BranchTable {
			dst = appendSpaceU(dst, uint64(depth))
		}
		return appendSpaceU(dst, uint64(ins.BranchDefault)), nil
	case wasmir.InstrGlobalGet, wasmir.InstrGlobalSet:
		return p.appendGlobalRef(appendNameSpace(dst, name), ins.GlobalIndex), nil
	case wasmir.InstrRefNull:
		return append(appendNameSpace(dst, name), p.heapTypeText(ins.RefType.HeapType)...), nil
	case wasmir.InstrRefTest, wasmir.InstrRefCast:
		return append(appendNameSpace(dst, name), p.valueTypeText(ins.RefType)...), nil
	case wasmir.InstrBrOnCast, wasmir.InstrBrOnCastFail:
		dst = appendNameU(dst, name, uint64(ins.BranchDepth))
		dst = append(append(dst, ' '), p.valueTypeText(ins.SourceRefType)...)
		return append(append(dst, ' '), p.valueTypeText(ins.RefType)...), nil
	case wasmir.InstrSelect:
		dst = append(dst, name...)
		if ins.SelectType == nil {
			return dst, nil
		}
		dst = append(dst, " (result "...)
		return append(append(dst, p.valueTypeText(*ins.SelectType)...), ')'), nil
	case wasmir.InstrCallIndirect, wasmir.InstrReturnCallIndirect:
		dst = append(dst, name...)
		if ins.TableIndex != 0 {
			dst = appendSpaceU(dst, uint64(ins.TableIndex))
		}
		return p.appendTypeUse(dst, ins.CallTypeIndex), nil
	case wasmir.InstrI32Const:
		return strconv.AppendInt(appendNameSpace(dst, name), int64(ins.I32Const), 10), nil
	case wasmir.InstrI64Const:
		return strconv.AppendInt(appendNameSpace(dst, name), ins.I64Const, 10), nil
	case wasmir.InstrF32Const:
		return append(appendNameSpace(dst, name), formatF32(ins.F32Const)...), nil
	case wasmir.InstrF64Const:
		return append(appendNameSpace(dst, name), formatF64(ins.F64Const)...), nil
	case wasmir.InstrV128Const:
		dst = append(dst, name...)
		dst = append(dst, " i8x16 "...)
		return append(dst, formatV128(ins.V128Const)...), nil
	case wasmir.InstrMemorySize, wasmir.InstrMemoryGrow, wasmir.InstrMemoryFill:
		dst = append(dst, name...)
		if ins.MemoryIndex == 0 {
			return dst, nil
		}
		return appendSpaceU(dst, uint64(ins.MemoryIndex)), nil
	case wasmir.InstrMemoryCopy:
		dst = append(dst, name...)
		if ins.MemoryIndex == 0 && ins.SourceMemoryIndex == 0 {
			return dst, nil
		}
		dst = appendSpaceU(dst, uint64(ins.MemoryIndex))
		return appendSpaceU(dst, uint64(ins.SourceMemoryIndex)), nil
	case wasmir.InstrMemoryInit:
		dst = append(dst, name...)
		if ins.MemoryIndex != 0 {
			dst = appendSpaceU(dst, uint64(ins.MemoryIndex))
		}
		return appendSpaceU(dst, uint64(ins.DataIndex)), nil
	case wasmir.InstrDataDrop:
		return appendNameU(dst, name, uint64(ins.DataIndex)), nil
	case wasmir.InstrTableGet, wasmir.InstrTableSet, wasmir.InstrTableGrow, wasmir.InstrTableSize, wasmir.InstrTableFill:
		dst = append(dst, name...)
		if ins.TableIndex == 0 {
			return dst, nil
		}
		return appendSpaceU(dst, uint64(ins.TableIndex)), nil
	case wasmir.InstrTableCopy:
		dst = append(dst, name...)
		if ins.TableIndex == 0 && ins.SourceTableIndex == 0 {
			return dst, nil
		}
		dst = appendSpaceU(dst, uint64(ins.TableIndex))
		return appendSpaceU(dst, uint64(ins.SourceTableIndex)), nil
	case wasmir.InstrTableInit:
		dst = append(dst, name...)
		if ins.TableIndex != 0 {
			dst = appendSpaceU(dst, uint64(ins.TableIndex))
		}
		return appendSpaceU(dst, uint64(ins.ElemIndex)), nil
	case wasmir.InstrElemDrop:
		return appendNameU(dst, name, uint64(ins.ElemIndex)), nil
	}

	switch ins.Kind {
	case wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane,
		wasmir.InstrV128Store8Lane, wasmir.InstrV128Store16Lane, wasmir.InstrV128Store32Lane, wasmir.InstrV128Store64Lane:
		return appendSpaceU(appendMemoryInstr(dst, name, ins), uint64(ins.LaneIndex)), nil
	case wasmir.InstrStructNew, wasmir.InstrStructNewDefault, wasmir.InstrArrayNew,
		wasmir.InstrArrayNewDefault, wasmir.InstrArrayGet, wasmir.InstrArrayGetS, wasmir.InstrArrayGetU,
		wasmir.InstrArraySet, wasmir.InstrArrayFill:
		return p.appendTypeRef(appendNameSpace(dst, name), ins.TypeIndex), nil
	case wasmir.InstrStructGet, wasmir.InstrStructGetS, wasmir.InstrStructGetU, wasmir.InstrStructSet:
		dst = p.appendTypeRef(appendNameSpace(dst, name), ins.TypeIndex)
		return p.appendFieldRef(append(dst, ' '), ins.TypeIndex, ins.FieldIndex), nil
	case wasmir.InstrArrayNewData, wasmir.InstrArrayInitData:
		dst = p.appendTypeRef(appendNameSpace(dst, name), ins.TypeIndex)
		return appendSpaceU(dst, uint64(ins.DataIndex)), nil
	case wasmir.InstrArrayNewElem, wasmir.InstrArrayInitElem:
		dst = p.appendTypeRef(appendNameSpace(dst, name), ins.TypeIndex)
		return appendSpaceU(dst, uint64(ins.ElemIndex)), nil
	case wasmir.InstrArrayNewFixed:
		dst = p.appendTypeRef(appendNameSpace(dst, name), ins.TypeIndex)
		return appendSpaceU(dst, uint64(ins.FixedCount)), nil
	case wasmir.InstrArrayCopy:
		dst = p.appendTypeRef(appendNameSpace(dst, name), ins.TypeIndex)
		return p.appendTypeRef(append(dst, ' '), ins.SourceTypeIndex), nil
	case wasmir.InstrI8x16Shuffle:
		return append(appendNameSpace(dst, name), formatShuffleLanes(ins.ShuffleLanes)...), nil
	case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU, wasmir.InstrI8x16ReplaceLane,
		wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU, wasmir.InstrI16x8ReplaceLane,
		wasmir.InstrI32x4ExtractLane, wasmir.InstrI32x4ReplaceLane, wasmir.InstrI64x2ExtractLane,
		wasmir.InstrI64x2ReplaceLane, wasmir.InstrF32x4ExtractLane, wasmir.InstrF32x4ReplaceLane,
		wasmir.InstrF64x2ExtractLane, wasmir.InstrF64x2ReplaceLane:
		return appendNameU(dst, name, uint64(ins.LaneIndex)), nil
	}

	switch def.Text.SyntaxClass {
	case instrdef.InstrSyntaxMemory:
		return appendMemoryInstr(dst, name, ins), nil
	case instrdef.InstrSyntaxPlain:
		return append(dst, name...), nil
	}
	return dst, fmt.Errorf("printing %s is not implemented yet", name)
}

// appendTryTable formats a flat try_table header including its catch clauses.
func (p *modulePrinter) appendTryTable(dst []byte, name string, ins wasmir.Instruction) ([]byte, error) {
	dst = p.appendBlockType(append(dst, name...), ins)
	for _, catch := range ins.TryTableCatches {
		label := uint64(catch.LabelDepth)
		dst = append(dst, ' ')
		switch catch.Kind {
		case wasmir.TryTableCatchKindTag:
			dst = append(dst, "(catch "...)
			dst = p.appendTagRef(dst, catch.TagIndex)
		case wasmir.TryTableCatchKindTagRef:
			dst = append(dst, "(catch_ref "...)
			dst = p.appendTagRef(dst, catch.TagIndex)
		case wasmir.TryTableCatchKindAll:
			dst = append(dst, "(catch_all"...)
		case wasmir.TryTableCatchKindAllRef:
			dst = append(dst, "(catch_all_ref"...)
		default:
			return dst, fmt.Errorf("unsupported try_table catch kind %d", catch.Kind)
		}
		dst = appendSpaceU(dst, label)
		dst = append(dst, ')')
	}
	return dst, nil
}

// appendBlockType appends the optional block type annotation for structured
// control instructions.
func (p *modulePrinter) appendBlockType(dst []byte, ins wasmir.Instruction) []byte {
	if ins.BlockTypeUsesIndex {
		return p.appendTypeUse(dst, ins.BlockTypeIndex)
	}
	if ins.BlockType == nil {
		return dst
	}
	dst = append(dst, " (result "...)
	return append(append(dst, p.valueTypeText(*ins.BlockType)...), ')')
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
	for range level {
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
	return string(p.appendTypeUse(nil, typeIdx))
}

// appendTypeUse appends a type use.
func (p *modulePrinter) appendTypeUse(dst []byte, typeIdx uint32) []byte {
	dst = append(dst, " (type "...)
	return append(p.appendTypeRef(dst, typeIdx), ')')
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
	return string(p.appendTypeRef(nil, typeIdx))
}

func (p *modulePrinter) appendTypeRef(dst []byte, typeIdx uint32) []byte {
	if p.m != nil && int(typeIdx) < len(p.m.Types) && p.m.Types[typeIdx].Name != "" {
		return append(dst, formatID(p.m.Types[typeIdx].Name)...)
	}
	if p.opts.NameUnnamed {
		return append(dst, syntheticName("type", typeIdx)...)
	}
	return strconv.AppendUint(dst, uint64(typeIdx), 10)
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
	return string(p.appendFuncRef(nil, funcIdx))
}

func (p *modulePrinter) appendFuncRef(dst []byte, funcIdx uint32) []byte {
	importedFuncs := p.importedFunctionCount()
	if funcIdx >= importedFuncs {
		definedIdx := funcIdx - importedFuncs
		if p.m != nil && int(definedIdx) < len(p.m.Funcs) && p.m.Funcs[definedIdx].Name != "" {
			return append(dst, formatID(p.m.Funcs[definedIdx].Name)...)
		}
	}
	if p.opts.NameUnnamed {
		return append(dst, syntheticName("func", funcIdx)...)
	}
	return strconv.AppendUint(dst, uint64(funcIdx), 10)
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
	return string(p.appendGlobalRef(nil, globalIdx))
}

func (p *modulePrinter) appendGlobalRef(dst []byte, globalIdx uint32) []byte {
	if p.m != nil && int(globalIdx) < len(p.m.Globals) && p.m.Globals[globalIdx].Name != "" {
		return append(dst, formatID(p.m.Globals[globalIdx].Name)...)
	}
	if p.opts.NameUnnamed {
		return append(dst, syntheticName("global", globalIdx)...)
	}
	return strconv.AppendUint(dst, uint64(globalIdx), 10)
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
	return string(p.appendTagRef(nil, tagIdx))
}

func (p *modulePrinter) appendTagRef(dst []byte, tagIdx uint32) []byte {
	importedTags := p.importedTagCount()
	if tagIdx >= importedTags {
		definedIdx := tagIdx - importedTags
		if p.m != nil && int(definedIdx) < len(p.m.Tags) && p.m.Tags[definedIdx].Name != "" {
			return append(dst, formatID(p.m.Tags[definedIdx].Name)...)
		}
	}
	if p.opts.NameUnnamed {
		return append(dst, syntheticName("tag", tagIdx)...)
	}
	return strconv.AppendUint(dst, uint64(tagIdx), 10)
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
	return string(p.appendFieldRef(nil, typeIdx, fieldIdx))
}

func (p *modulePrinter) appendFieldRef(dst []byte, typeIdx uint32, fieldIdx uint32) []byte {
	if p.m != nil && int(typeIdx) < len(p.m.Types) {
		td := p.m.Types[typeIdx]
		if int(fieldIdx) < len(td.Fields) && td.Fields[fieldIdx].Name != "" {
			return append(dst, formatID(td.Fields[fieldIdx].Name)...)
		}
	}
	if p.opts.NameUnnamed {
		return append(dst, syntheticName("field", fieldIdx)...)
	}
	return strconv.AppendUint(dst, uint64(fieldIdx), 10)
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
		text, err := p.instructionText(ins, nil)
		if err != nil {
			return "", err
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " "), nil
}

// appendMemoryInstr appends a memory instruction with its optional memory
// index, offset, and alignment immediates.
func appendMemoryInstr(dst []byte, name string, ins wasmir.Instruction) []byte {
	dst = append(dst, name...)
	if ins.MemoryIndex != 0 {
		dst = appendSpaceU(dst, uint64(ins.MemoryIndex))
	}
	if ins.MemoryOffset != 0 {
		dst = append(dst, " offset="...)
		dst = strconv.AppendUint(dst, ins.MemoryOffset, 10)
	}
	if ins.MemoryAlign != 0 {
		dst = append(dst, " align="...)
		dst = strconv.AppendUint(dst, uint64(1)<<ins.MemoryAlign, 10)
	}
	return dst
}

// appendNameSpace appends an instruction name and the space before its first
// immediate.
func appendNameSpace(dst []byte, name string) []byte {
	return append(append(dst, name...), ' ')
}

// appendNameU appends "name value".
func appendNameU(dst []byte, name string, v uint64) []byte {
	return strconv.AppendUint(appendNameSpace(dst, name), v, 10)
}

// appendSpaceU appends " value".
func appendSpaceU(dst []byte, v uint64) []byte {
	return strconv.AppendUint(append(dst, ' '), v, 10)
}

// localRefText resolves a local or parameter index to a printed identifier when
// a name is available, or falls back to the numeric index.
func (p *modulePrinter) appendLocalRef(dst []byte, fn *wasmir.Function, index uint32) []byte {
	if fn == nil {
		return strconv.AppendUint(dst, uint64(index), 10)
	}
	paramCount := uint32(0)
	if p.m != nil && int(fn.TypeIdx) < len(p.m.Types) && p.m.Types[fn.TypeIdx].Kind == wasmir.TypeDefKindFunc {
		paramCount = uint32(len(p.m.Types[fn.TypeIdx].Params))
	}
	if index < paramCount && int(index) < len(fn.ParamNames) && fn.ParamNames[index] != "" {
		return append(dst, formatID(fn.ParamNames[index])...)
	}
	if index >= paramCount {
		localIdx := index - paramCount
		if int(localIdx) < len(fn.LocalNames) && fn.LocalNames[localIdx] != "" {
			return append(dst, formatID(fn.LocalNames[localIdx])...)
		}
	}
	if p.opts.NameUnnamed {
		return append(dst, syntheticName("local", index)...)
	}
	return strconv.AppendUint(dst, uint64(index), 10)
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
