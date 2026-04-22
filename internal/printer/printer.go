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
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}
	p := modulePrinter{m: m}
	if err := p.printModule(); err != nil {
		return nil, err
	}
	return p.buf.Bytes(), nil
}

type modulePrinter struct {
	m   *wasmir.Module
	buf bytes.Buffer
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
		td := p.m.Types[i]
		if td.RecGroupSize != 0 || td.SubType {
			return fmt.Errorf("printing recursive/subtype type definitions is not implemented yet")
		}
		p.writeIndent(1)
		p.buf.WriteString("(type")
		if td.Name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(formatID(td.Name))
		}
		p.buf.WriteByte(' ')
		switch td.Kind {
		case wasmir.TypeDefKindFunc:
			p.buf.WriteString("(func")
			writeParamDecls(&p.buf, nil, td.Params)
			writeResultDecls(&p.buf, td.Results)
			p.buf.WriteString("))\n")
		case wasmir.TypeDefKindStruct:
			p.buf.WriteString("(struct")
			for _, field := range td.Fields {
				p.buf.WriteByte(' ')
				p.buf.WriteString("(field")
				if field.Name != "" {
					p.buf.WriteByte(' ')
					p.buf.WriteString(formatID(field.Name))
				}
				p.buf.WriteByte(' ')
				p.buf.WriteString(fieldTypeText(field))
				p.buf.WriteByte(')')
			}
			p.buf.WriteString("))\n")
		case wasmir.TypeDefKindArray:
			p.buf.WriteString("(array ")
			p.buf.WriteString(fieldTypeText(td.ElemField))
			p.buf.WriteString("))\n")
		default:
			return fmt.Errorf("unsupported type kind %d", td.Kind)
		}
	}
	return nil
}

func (p *modulePrinter) printImports() error {
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
			p.buf.WriteString(typeUseText(imp.TypeIdx))
			writeParamDecls(&p.buf, nil, td.Params)
			writeResultDecls(&p.buf, td.Results)
			p.buf.WriteString("))\n")
		case wasmir.ExternalKindTable:
			p.buf.WriteString("(table")
			writeTableType(&p.buf, imp.Table)
			p.buf.WriteString("))\n")
		case wasmir.ExternalKindMemory:
			p.buf.WriteString("(memory")
			writeMemoryType(&p.buf, imp.Memory)
			p.buf.WriteString("))\n")
		case wasmir.ExternalKindGlobal:
			p.buf.WriteString("(global ")
			p.buf.WriteString(globalTypeText(imp.GlobalType, imp.GlobalMutable))
			p.buf.WriteString("))\n")
		case wasmir.ExternalKindTag:
			p.buf.WriteString("(tag")
			p.buf.WriteString(typeUseText(imp.TypeIdx))
			p.buf.WriteString("))\n")
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
		writeTableType(&p.buf, table)
		if len(table.Init) > 0 {
			expr, err := formatConstExpr(table.Init)
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
		writeMemoryType(&p.buf, mem)
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printDefinedGlobals() error {
	for _, g := range p.m.Globals {
		if g.ImportModule != "" {
			continue
		}
		init, err := formatConstExpr(g.Init)
		if err != nil {
			return fmt.Errorf("global init: %w", err)
		}
		p.writeIndent(1)
		p.buf.WriteString("(global")
		if g.Name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(formatID(g.Name))
		}
		p.buf.WriteByte(' ')
		p.buf.WriteString(globalTypeText(g.Type, g.Mutable))
		p.buf.WriteByte(' ')
		p.buf.WriteString(init)
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printDefinedTags() error {
	for _, tag := range p.m.Tags {
		if tag.ImportModule != "" {
			continue
		}
		p.writeIndent(1)
		p.buf.WriteString("(tag")
		if tag.Name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(formatID(tag.Name))
		}
		p.buf.WriteString(typeUseText(tag.TypeIdx))
		p.buf.WriteString(")\n")
	}
	return nil
}

func (p *modulePrinter) printFuncs() error {
	for _, fn := range p.m.Funcs {
		td, err := p.funcType(fn.TypeIdx)
		if err != nil {
			return err
		}
		p.writeIndent(1)
		p.buf.WriteString("(func")
		if fn.Name != "" {
			p.buf.WriteByte(' ')
			p.buf.WriteString(formatID(fn.Name))
		}
		p.buf.WriteString(typeUseText(fn.TypeIdx))
		writeParamDecls(&p.buf, fn.ParamNames, td.Params)
		writeResultDecls(&p.buf, td.Results)
		writeLocalDecls(&p.buf, fn.LocalNames, fn.Locals)
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
		case wasmir.ExternalKindTable:
			p.buf.WriteString("table ")
		case wasmir.ExternalKindMemory:
			p.buf.WriteString("memory ")
		case wasmir.ExternalKindGlobal:
			p.buf.WriteString("global ")
		case wasmir.ExternalKindTag:
			p.buf.WriteString("tag ")
		default:
			return fmt.Errorf("unsupported export kind %d", exp.Kind)
		}
		p.buf.WriteString(strconv.FormatUint(uint64(exp.Index), 10))
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
	p.buf.WriteString(strconv.FormatUint(uint64(*p.m.StartFuncIndex), 10))
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
			offset, err := formatConstExpr(elemOffsetExprOrSynthetic(elem))
			if err != nil {
				return fmt.Errorf("elem offset: %w", err)
			}
			p.buf.WriteByte(' ')
			p.buf.WriteString(offset)
		default:
			return fmt.Errorf("unsupported element mode %d", elem.Mode)
		}

		if len(elem.Exprs) > 0 {
			p.buf.WriteByte(' ')
			p.buf.WriteString(valueTypeText(elem.RefType))
			for _, expr := range elem.Exprs {
				text, err := formatElemItemExpr(expr)
				if err != nil {
					return err
				}
				p.buf.WriteByte(' ')
				p.buf.WriteString("(item ")
				p.buf.WriteString(text)
				p.buf.WriteByte(')')
			}
		} else {
			p.buf.WriteString(" func")
			for _, idx := range elem.FuncIndices {
				p.buf.WriteByte(' ')
				p.buf.WriteString(strconv.FormatUint(uint64(idx), 10))
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
			offset, err := formatConstExpr(dataOffsetExprOrSynthetic(seg))
			if err != nil {
				return fmt.Errorf("data offset: %w", err)
			}
			p.buf.WriteByte(' ')
			p.buf.WriteString(offset)
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
		case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrIf:
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
		return name + blockTypeText(ins), nil
	case wasmir.InstrElse, wasmir.InstrEnd:
		return name, nil
	case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
		return fmt.Sprintf("%s %s", name, localRefText(p.m, fn, ins.LocalIndex)), nil
	case wasmir.InstrCall, wasmir.InstrReturnCall:
		return fmt.Sprintf("%s %d", name, ins.FuncIndex), nil
	case wasmir.InstrCallRef, wasmir.InstrReturnCallRef:
		return fmt.Sprintf("%s %d", name, ins.TypeIndex), nil
	case wasmir.InstrThrow:
		return fmt.Sprintf("%s %d", name, ins.TagIndex), nil
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
		return fmt.Sprintf("%s %d", name, ins.GlobalIndex), nil
	case wasmir.InstrRefFunc:
		return fmt.Sprintf("%s %d", name, ins.FuncIndex), nil
	case wasmir.InstrRefNull, wasmir.InstrRefTest, wasmir.InstrRefCast:
		return fmt.Sprintf("%s %s", name, valueTypeText(ins.RefType)), nil
	case wasmir.InstrBrOnCast, wasmir.InstrBrOnCastFail:
		return fmt.Sprintf("%s %d %s %s", name, ins.BranchDepth, valueTypeText(ins.SourceRefType), valueTypeText(ins.RefType)), nil
	case wasmir.InstrSelect:
		if ins.SelectType == nil {
			return name, nil
		}
		return fmt.Sprintf("%s (result %s)", name, valueTypeText(*ins.SelectType)), nil
	case wasmir.InstrCallIndirect, wasmir.InstrReturnCallIndirect:
		var b strings.Builder
		b.WriteString(name)
		if ins.TableIndex != 0 {
			b.WriteByte(' ')
			b.WriteString(strconv.FormatUint(uint64(ins.TableIndex), 10))
		}
		b.WriteString(fmt.Sprintf(" (type %d)", ins.CallTypeIndex))
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

	if def.Text.SyntaxClass == instrdef.InstrSyntaxMemory {
		return memoryInstrText(name, ins), nil
	}

	switch ins.Kind {
	case wasmir.InstrStructNew, wasmir.InstrStructNewDefault, wasmir.InstrArrayNew,
		wasmir.InstrArrayNewDefault, wasmir.InstrArrayGet, wasmir.InstrArrayGetS, wasmir.InstrArrayGetU,
		wasmir.InstrArraySet, wasmir.InstrArrayFill:
		return fmt.Sprintf("%s %d", name, ins.TypeIndex), nil
	case wasmir.InstrStructGet, wasmir.InstrStructGetS, wasmir.InstrStructGetU, wasmir.InstrStructSet:
		return fmt.Sprintf("%s %d %d", name, ins.TypeIndex, ins.FieldIndex), nil
	case wasmir.InstrArrayNewData, wasmir.InstrArrayInitData:
		return fmt.Sprintf("%s %d %d", name, ins.TypeIndex, ins.DataIndex), nil
	case wasmir.InstrArrayNewElem, wasmir.InstrArrayInitElem:
		return fmt.Sprintf("%s %d %d", name, ins.TypeIndex, ins.ElemIndex), nil
	case wasmir.InstrArrayNewFixed:
		return fmt.Sprintf("%s %d %d", name, ins.TypeIndex, ins.FixedCount), nil
	case wasmir.InstrArrayCopy:
		return fmt.Sprintf("%s %d %d", name, ins.TypeIndex, ins.SourceTypeIndex), nil
	case wasmir.InstrV128Load8Lane, wasmir.InstrV128Load16Lane, wasmir.InstrV128Load32Lane, wasmir.InstrV128Load64Lane,
		wasmir.InstrV128Store8Lane, wasmir.InstrV128Store16Lane, wasmir.InstrV128Store32Lane, wasmir.InstrV128Store64Lane:
		return memoryInstrText(name, ins) + " " + strconv.FormatUint(uint64(ins.LaneIndex), 10), nil
	case wasmir.InstrI8x16Shuffle:
		return fmt.Sprintf("%s %s", name, formatShuffleLanes(ins.ShuffleLanes)), nil
	case wasmir.InstrI8x16ExtractLaneS, wasmir.InstrI8x16ExtractLaneU, wasmir.InstrI8x16ReplaceLane,
		wasmir.InstrI16x8ExtractLaneS, wasmir.InstrI16x8ExtractLaneU, wasmir.InstrI16x8ReplaceLane,
		wasmir.InstrI32x4ExtractLane, wasmir.InstrI32x4ReplaceLane, wasmir.InstrI64x2ExtractLane,
		wasmir.InstrI64x2ReplaceLane, wasmir.InstrF32x4ExtractLane, wasmir.InstrF32x4ReplaceLane,
		wasmir.InstrF64x2ExtractLane, wasmir.InstrF64x2ReplaceLane:
		return fmt.Sprintf("%s %d", name, ins.LaneIndex), nil
	case wasmir.InstrTryTable:
		return "", fmt.Errorf("printing try_table is not implemented yet")
	}

	if def.Text.SyntaxClass == instrdef.InstrSyntaxPlain {
		return name, nil
	}
	return "", fmt.Errorf("printing %s is not implemented yet", name)
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
		p.buf.WriteString("  ")
	}
}

// writeParamDecls appends parameter declarations to buf, including names when
// available.
func writeParamDecls(buf *bytes.Buffer, names []string, params []wasmir.ValueType) {
	for i, vt := range params {
		buf.WriteString(" (param")
		if i < len(names) && names[i] != "" {
			buf.WriteByte(' ')
			buf.WriteString(formatID(names[i]))
		}
		buf.WriteByte(' ')
		buf.WriteString(valueTypeText(vt))
		buf.WriteByte(')')
	}
}

// writeResultDecls appends result declarations to buf.
func writeResultDecls(buf *bytes.Buffer, results []wasmir.ValueType) {
	for _, vt := range results {
		buf.WriteString(" (result ")
		buf.WriteString(valueTypeText(vt))
		buf.WriteByte(')')
	}
}

// writeLocalDecls appends local declarations to buf, including names when
// available.
func writeLocalDecls(buf *bytes.Buffer, names []string, locals []wasmir.ValueType) {
	for i, vt := range locals {
		buf.WriteString(" (local")
		if i < len(names) && names[i] != "" {
			buf.WriteByte(' ')
			buf.WriteString(formatID(names[i]))
		}
		buf.WriteByte(' ')
		buf.WriteString(valueTypeText(vt))
		buf.WriteByte(')')
	}
}

// writeTableType appends the textual form of a table type to buf.
func writeTableType(buf *bytes.Buffer, table wasmir.Table) {
	if table.AddressType == wasmir.ValueTypeI64 {
		buf.WriteString(" i64")
	}
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatUint(table.Min, 10))
	if table.Max != nil {
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatUint(*table.Max, 10))
	}
	buf.WriteByte(' ')
	buf.WriteString(valueTypeText(table.RefType))
}

// writeMemoryType appends the textual form of a memory type to buf.
func writeMemoryType(buf *bytes.Buffer, mem wasmir.Memory) {
	if mem.AddressType == wasmir.ValueTypeI64 {
		buf.WriteString(" i64")
	}
	buf.WriteByte(' ')
	buf.WriteString(strconv.FormatUint(mem.Min, 10))
	if mem.Max != nil {
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatUint(*mem.Max, 10))
	}
}

// globalTypeText returns the WAT spelling of a global type, including
// mutability.
func globalTypeText(vt wasmir.ValueType, mutable bool) string {
	if !mutable {
		return valueTypeText(vt)
	}
	return "(mut " + valueTypeText(vt) + ")"
}

// fieldTypeText returns the WAT spelling of a struct or array field type.
func fieldTypeText(ft wasmir.FieldType) string {
	var storage string
	switch ft.Packed {
	case wasmir.PackedTypeI8:
		storage = "i8"
	case wasmir.PackedTypeI16:
		storage = "i16"
	default:
		storage = valueTypeText(ft.Type)
	}
	if !ft.Mutable {
		return storage
	}
	return "(mut " + storage + ")"
}

// typeUseText formats a type use.
func typeUseText(typeIdx uint32) string {
	return fmt.Sprintf(" (type %d)", typeIdx)
}

// blockTypeText formats the optional block type annotation for structured
// control instructions.
func blockTypeText(ins wasmir.Instruction) string {
	if ins.BlockTypeUsesIndex {
		return fmt.Sprintf(" (type %d)", ins.BlockTypeIndex)
	}
	if ins.BlockType == nil {
		return ""
	}
	return " (result " + valueTypeText(*ins.BlockType) + ")"
}

// valueTypeText returns the textual name of a value type.
func valueTypeText(vt wasmir.ValueType) string {
	return vt.String()
}

// formatID normalizes an identifier into the `$name` form used in WAT.
func formatID(name string) string {
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "$") {
		return name
	}
	return "$" + name
}

// formatConstExpr formats a constant expression in parenthesized WAT form.
// For now only a single non-final instruction is supported.
func formatConstExpr(expr []wasmir.Instruction) (string, error) {
	if len(expr) == 0 {
		return "", fmt.Errorf("empty const expression")
	}
	if expr[len(expr)-1].Kind == wasmir.InstrEnd {
		expr = expr[:len(expr)-1]
	}
	if len(expr) != 1 {
		return "", fmt.Errorf("multi-instruction const expressions are not implemented yet")
	}
	text, err := instructionTextNoContext(expr[0])
	if err != nil {
		return "", err
	}
	return "(" + text + ")", nil
}

// formatElemItemExpr formats a single element-segment item expression.
func formatElemItemExpr(expr []wasmir.Instruction) (string, error) {
	if len(expr) == 0 {
		return "", fmt.Errorf("empty elem item expression")
	}
	if expr[len(expr)-1].Kind == wasmir.InstrEnd {
		expr = expr[:len(expr)-1]
	}
	if len(expr) != 1 {
		return "", fmt.Errorf("multi-instruction elem item expressions are not implemented yet")
	}
	return instructionTextNoContext(expr[0])
}

// instructionTextNoContext formats an instruction that does not need module or
// function context for index-to-name resolution.
func instructionTextNoContext(ins wasmir.Instruction) (string, error) {
	p := modulePrinter{}
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
func localRefText(m *wasmir.Module, fn *wasmir.Function, index uint32) string {
	if fn == nil {
		return strconv.FormatUint(uint64(index), 10)
	}
	paramCount := uint32(0)
	if m != nil && int(fn.TypeIdx) < len(m.Types) && m.Types[fn.TypeIdx].Kind == wasmir.TypeDefKindFunc {
		paramCount = uint32(len(m.Types[fn.TypeIdx].Params))
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
	return strconv.FormatUint(uint64(index), 10)
}

// formatF32 formats an f32 constant from its raw IEEE-754 bits.
func formatF32(bits uint32) string {
	v := float64(math.Float32frombits(bits))
	return formatFloat(v, 32)
}

// formatF64 formats an f64 constant from its raw IEEE-754 bits.
func formatF64(bits uint64) string {
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
