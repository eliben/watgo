package textformat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/instrdef"
)

type Parser struct {
	errs diag.ErrorList
}

// Parsing flow for text-format modules:
//  1. A lexer tokenizes the input buffer.
//  2. ParseTopLevelSExprs builds generic s-expression trees from tokens.
//  3. ParseModuleSExpr/ParseModule convert these s-expressions to typed AST.
//
// This split keeps s-expression parsing reusable by tests and script harnesses
// that need non-module top-level forms.

// ParseTopLevelSExprs parses all top-level s-expressions in buf.
func ParseTopLevelSExprs(buf string) ([]*SExpr, error) {
	lex := newLexer(buf)
	return sexprifyAll(lex)
}

// sexprifyAll parses all top-level s-expressions from lex until EOF.
func sexprifyAll(lex *lexer) ([]*SExpr, error) {
	var out []*SExpr
	for {
		tok := lex.nextToken()
		if tok.name == EOF {
			return out, nil
		}
		if tok.name != LPAREN {
			return nil, fmt.Errorf("at %s: %v: expected '('", tok.loc, tok.value)
		}

		sx, err := sexprify(lex, tok)
		if err != nil {
			return nil, err
		}
		out = append(out, sx)
	}
}

// sexprify is a helper for a single s-expression; it's called when '(' is
// encountered and consumed, and returns a new sexpr. lparen is the consumed
// '(' token.
func sexprify(lex *lexer, lparen token) (*SExpr, error) {
	// list non-nil distinguishes list nodes from token nodes.
	sx := &SExpr{loc: lparen.loc, list: make([]*SExpr, 0)}

	for {
		tok := lex.nextToken()
		if tok.name == LPAREN {
			list, err := sexprify(lex, tok)
			if err != nil {
				return nil, err
			}
			sx.list = append(sx.list, list)
		} else if tok.name == RPAREN {
			return sx, nil
		} else if tok.name == EOF {
			return nil, fmt.Errorf("expression starting with ( at %v is unterminated", lparen.loc)
		} else {
			sx.list = append(sx.list, &SExpr{tok: tok, loc: tok.loc})
		}
	}
}

// ParseModule parses a text-format module source string.
// It returns a parsed module and nil on success. On any failure, it returns
// diag.ErrorList.
func ParseModule(buf string) (*Module, error) {
	sxs, err := ParseTopLevelSExprs(buf)
	if err != nil {
		return nil, diag.FromError(err)
	}
	if len(sxs) != 1 {
		return nil, diag.Fromf("expected exactly one top-level expression, found %d", len(sxs))
	}

	return ParseModuleSExpr(sxs[0])
}

// ParseModuleSExpr parses a single module SExpr.
// It returns a parsed module and nil on success. On any failure, it returns
// diag.ErrorList.
func ParseModuleSExpr(sx *SExpr) (*Module, error) {
	if sx == nil {
		return nil, diag.Fromf("module s-expression is nil")
	}
	p := &Parser{}
	m := p.parseModule(sx)

	if len(p.errs) > 0 {
		return m, p.errs
	} else {
		return m, nil
	}
}

// emitError appends one parser diagnostic, prefixed with a source location.
// format follows fmt.Sprintf semantics.
func (p *Parser) emitError(loc location, format string, args ...any) {
	if len(args) == 0 {
		p.errs.Addf("%s: %s", loc, format)
		return
	}
	p.errs.Addf("%s: %s", loc, fmt.Sprintf(format, args...))
}

// matchToken expects a list sx and matches element [idx] to the given tokname.
// If successful, it returns the actual token value at [idx]; otherwise it emits
// an error and returns "".
func (p *Parser) matchElement(sx *SExpr, idx int, tokname tokenName) string {
	if !sx.IsList() {
		p.emitError(sx.loc, "expected list")
		return ""
	}
	if len(sx.list) <= idx {
		p.emitError(sx.loc, "expected list with at least %d items", idx+1)
		return ""
	}

	sub := sx.list[idx]
	if !sub.IsToken() {
		p.emitError(sub.loc, "expected %s, found list", tokname)
		return ""
	}
	if sub.tok.name != tokname {
		p.emitError(sub.loc, "expected %s, found %s", tokname, sub.tok.value)
		return ""
	}

	return sub.tok.value
}

// parseModule parses one "(module ...)" S-expression into a typed module AST.
// Unsupported or malformed fields are reported to parser diagnostics.
func (p *Parser) parseModule(sx *SExpr) *Module {
	if sx.HeadKeyword() != "module" {
		p.emitError(sx.loc, "expected 'module'")
		return nil
	}

	m := &Module{loc: sx.loc}
	// Optional module name
	cursor := 1
	if len(sx.list) > 1 && sx.list[cursor].IsTokenKind(ID) {
		m.Id = sx.list[cursor].tok.value
		cursor++
	}
	// .wast script commands may include "(module definition ...)".
	if cursor < len(sx.list) && sx.list[cursor].IsKeywordToken("definition") {
		cursor++
	}

	// importsClosed tracks when module parsing has moved past the import phase.
	// WAT allows imports in the type/import prefix, but once a non-import
	// definition is seen, later top-level "(import ...)" forms are rejected.
	importsClosed := false
	for i := cursor; i < len(sx.list); i++ {
		sub := sx.list[i]
		switch sub.HeadKeyword() {
		case "import":
			if importsClosed {
				p.emitError(sub.loc, "import after module field")
				continue
			}
			fd, td, md, gd, ok := p.parseImportField(sub)
			if !ok {
				continue
			}
			if fd != nil {
				m.Funcs = append(m.Funcs, fd)
			}
			if td != nil {
				m.Tables = append(m.Tables, td)
			}
			if md != nil {
				m.Memories = append(m.Memories, md)
			}
			if gd != nil {
				m.Globals = append(m.Globals, gd)
			}
		case "type":
			m.Types = append(m.Types, p.parseTypeDecl(sub))
		case "rec":
			// Parse the nested type declarations as ordinary module types, but
			// remember the size of the group on its first entry so lowering can
			// re-encode this contiguous range as one recursive type group.
			groupStart := len(m.Types)
			for _, nested := range sub.Children()[1:] {
				if nested.HeadKeyword() != "type" {
					p.emitError(nested.loc, "unsupported rec field %q", nested.HeadKeyword())
					continue
				}
				m.Types = append(m.Types, p.parseTypeDecl(nested))
			}
			if groupSize := len(m.Types) - groupStart; groupSize > 0 && m.Types[groupStart] != nil {
				m.Types[groupStart].RecGroupSize = groupSize
			}
		case "table":
			td := p.parseTableDecl(sub)
			m.Tables = append(m.Tables, td)
			if td.ImportModule == "" {
				importsClosed = true
			}
		case "memory":
			md := p.parseMemoryDecl(sub)
			m.Memories = append(m.Memories, md)
			if md.ImportModule == "" {
				importsClosed = true
			}
		case "data":
			m.Data = append(m.Data, p.parseDataDecl(sub))
			importsClosed = true
		case "global":
			gd := p.parseGlobalDecl(sub)
			m.Globals = append(m.Globals, gd)
			if gd.ImportModule == "" {
				importsClosed = true
			}
		case "elem":
			m.Elems = append(m.Elems, p.parseElemDecl(sub))
			importsClosed = true
		case "func":
			fd := p.parseFunction(sub)
			m.Funcs = append(m.Funcs, fd)
			if fd.ImportModule == "" {
				importsClosed = true
			}
		case "export":
			ed := p.parseExportDecl(sub)
			if ed != nil {
				m.Exports = append(m.Exports, ed)
			}
			importsClosed = true
		case "tag":
			// Exception handling tags are outside the current lowering subset.
			// Keep parsing the rest of the module fields.
		default:
			p.emitError(sub.loc, "unsupported module field %q", sub.HeadKeyword())
			importsClosed = true
		}
	}

	return m
}

// parseExportDecl parses a top-level "(export ...)" declaration.
//
// Supported descriptor forms:
//   - (export "name" (func X))
//   - (export "name" (global X))
//   - (export "name" (table X))
//   - (export "name" (memory X))
//
// where X is an identifier or integer index token.
func (p *Parser) parseExportDecl(sx *SExpr) *ExportDecl {
	if len(sx.list) != 3 {
		p.emitError(sx.loc, "invalid export declaration")
		return nil
	}
	name := p.matchElement(sx, 1, STRING)
	desc := sx.list[2]
	head := desc.HeadKeyword()
	switch head {
	case "func", "global", "table", "memory":
		// Supported export descriptor kinds.
	default:
		// Unsupported export kinds (for example tags) are ignored in this
		// lowering subset so the rest of the module can still be processed.
		return nil
	}
	if !desc.IsList() || len(desc.list) != 2 {
		p.emitError(desc.loc, "invalid export descriptor")
		return nil
	}
	refElem := desc.list[1]
	if !refElem.IsTokenAny(ID, INT) {
		p.emitError(refElem.loc, "export %s reference must be ID or INT", head)
		return nil
	}

	return &ExportDecl{
		Name: name,
		Kind: head,
		Ref:  refElem.tok.value,
		loc:  sx.loc,
	}
}

// parseImportField parses one top-level "(import ...)" form.
// It currently supports function/table/memory/global imports and returns at
// most one descriptor of each kind plus a success flag.
func (p *Parser) parseImportField(sx *SExpr) (*Function, *TableDecl, *MemoryDecl, *GlobalDecl, bool) {
	if len(sx.list) != 4 {
		p.emitError(sx.loc, "invalid import declaration")
		return nil, nil, nil, nil, false
	}
	mod := p.matchElement(sx, 1, STRING)
	name := p.matchElement(sx, 2, STRING)
	desc := sx.list[3]
	switch desc.HeadKeyword() {
	case "func":
		fd := p.parseFuncImportDesc(desc)
		fd.ImportModule = mod
		fd.ImportName = name
		return fd, nil, nil, nil, true
	case "table":
		td := p.parseTableDecl(desc)
		td.ImportModule = mod
		td.ImportName = name
		return nil, td, nil, nil, true
	case "memory":
		md := p.parseMemoryDecl(desc)
		md.ImportModule = mod
		md.ImportName = name
		return nil, nil, md, nil, true
	case "global":
		gd := p.parseGlobalImportDesc(desc)
		gd.ImportModule = mod
		gd.ImportName = name
		return nil, nil, nil, gd, true
	case "tag":
		// Exception tags are outside the current lowering subset.
		// Ignore tag imports so the rest of the module can be compiled/tested.
		return nil, nil, nil, nil, true
	default:
		p.emitError(desc.loc, "unsupported import descriptor")
		return nil, nil, nil, nil, false
	}
}

// parseFuncImportDesc parses a function import descriptor "(func ...)" and
// returns it as a Function declaration with import fields left unset.
func (p *Parser) parseFuncImportDesc(sx *SExpr) *Function {
	fd := p.parseFunction(sx)
	if fd == nil {
		return &Function{TyUse: &TypeUse{}, loc: sx.loc}
	}
	if len(fd.Locals) > 0 {
		p.emitError(sx.loc, "function import descriptor must not declare locals")
	}
	if len(fd.Instrs) > 0 {
		p.emitError(sx.loc, "function import descriptor must not contain a body")
		fd.Instrs = nil
	}
	return fd
}

// parseGlobalImportDesc parses a global import descriptor "(global ...)".
// It accepts either immutable "<valtype>" or mutable "(mut <valtype>)".
func (p *Parser) parseGlobalImportDesc(sx *SExpr) *GlobalDecl {
	gd := &GlobalDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		gd.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "invalid global import descriptor")
		return gd
	}
	tySx := sx.list[cursor]
	if cursor+1 != len(sx.list) {
		p.emitError(sx.loc, "invalid global import descriptor")
		return gd
	}
	if tySx.HeadKeyword() == "mut" {
		if len(tySx.list) != 2 {
			p.emitError(tySx.loc, "invalid mutable global type")
			return gd
		}
		gd.Mutable = true
		gd.Ty = p.parseType(tySx.list[1])
		return gd
	}
	gd.Ty = p.parseType(tySx)
	return gd
}

// parseFunction parses one module-level "(func ...)" declaration, including
// optional ID/export/type use, local declarations, and instruction body.
func (p *Parser) parseFunction(sx *SExpr) *Function {
	f := &Function{
		TyUse: &TypeUse{},
		loc:   sx.loc,
	}

	// Optional function name
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		f.Id = sx.list[cursor].tok.value
		cursor++
	}

	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "export" {
		f.Export = p.matchElement(sx.list[cursor], 1, STRING)
		cursor++
	}

	f.TyUse = &TypeUse{}

	for ; cursor < len(sx.list); cursor++ {
		// TODO: enforce order on param/result/local clauses?
		elem := sx.list[cursor]
		switch elem.HeadKeyword() {
		case "type":
			f.TyUse.Id = p.parseTypeUseClause(elem)
		case "export":
			name := p.matchElement(elem, 1, STRING)
			if f.Export == "" {
				f.Export = name
			}
		case "import":
			modName, fieldName, ok := p.parseImportClause(elem)
			if !ok {
				p.emitError(elem.loc, "invalid function import clause")
				continue
			}
			if f.ImportModule != "" {
				p.emitError(elem.loc, "duplicate function import clause")
				continue
			}
			f.ImportModule = modName
			f.ImportName = fieldName
		case "param":
			f.TyUse.Params = append(f.TyUse.Params, p.parseParamDecl(elem)...)
		case "result":
			f.TyUse.Results = append(f.TyUse.Results, p.parseResultDecl(elem)...)
		case "local":
			f.Locals = append(f.Locals, p.parseLocalDecl(elem)...)
		default:
			// Neither of these, so the instruction sequence started. Parse the
			// entire instruction sequence.
			f.Instrs = p.parseInstrs(sx, cursor)
			return f
		}
	}

	return f
}

// parseTypeDecl parses one module-level "(type ...)" declaration.
//
// Expected forms:
//   - (type (func ...))
//   - (type $name (func ...))
//   - (type (struct (field ...)*))
//   - (type (array <fieldtype>))
//   - (type (sub <super>* (struct ...)))
//
// It returns a TypeDecl even on malformed input so parsing can continue; errors
// are reported through parser diagnostics.
func (p *Parser) parseTypeDecl(sx *SExpr) *TypeDecl {
	td := &TypeDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		td.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "type declaration missing type body")
		td.TyUse = &TypeUse{}
		return td
	}
	body := sx.list[cursor]
	switch body.HeadKeyword() {
	case "func":
		td.TyUse = p.parseFuncTypeUse(body)
	case "struct":
		td.StructFields = p.parseStructTypeFields(body)
	case "array":
		td.ArrayField = p.parseArrayTypeField(body)
	case "sub":
		p.parseSubtypeDecl(body, td)
	default:
		p.emitError(body.loc, "type declaration expects (func ...), (struct ...), (array ...), or (sub ...)")
		td.TyUse = &TypeUse{}
	}
	return td
}

// parseSubtypeDecl parses the body of a "(sub ...)" type declaration.
//
// The parser expects `sx` to be the nested "(sub ...)" S-expression from a
// module-level "(type ...)" declaration, not the outer "(type ...)" form.
//
// Supported shapes are:
//   - (sub (struct))
//   - (sub $super (struct (field i32)))
//   - (sub final $super (func (param i32) (result i32)))
//   - (sub $a $b (array (mut (ref null any))))
//
// Everything before the final composite-type body is treated as either the
// optional `final` marker or a list of supertype references. The last element
// must be one of "(func ...)", "(struct ...)", or "(array ...)".
func (p *Parser) parseSubtypeDecl(sx *SExpr, td *TypeDecl) {
	td.SubType = true
	elems := sx.Children()
	if len(elems) < 2 {
		p.emitError(sx.loc, "sub type declaration missing body")
		td.TyUse = &TypeUse{}
		return
	}
	cursor := 1
	if cursor < len(elems)-1 && elems[cursor].IsKeywordToken("final") {
		td.Final = true
		cursor++
	}
	body := elems[len(elems)-1]
	for ; cursor < len(elems)-1; cursor++ {
		if !elems[cursor].IsTokenAny(ID, INT) {
			p.emitError(elems[cursor].loc, "invalid subtype supertype reference")
			continue
		}
		td.SuperTypes = append(td.SuperTypes, elems[cursor].tok.value)
	}
	switch body.HeadKeyword() {
	case "func":
		td.TyUse = p.parseFuncTypeUse(body)
	case "struct":
		td.StructFields = p.parseStructTypeFields(body)
	case "array":
		td.ArrayField = p.parseArrayTypeField(body)
	default:
		p.emitError(body.loc, "sub type declaration expects (func ...), (struct ...), or (array ...)")
		td.TyUse = &TypeUse{}
	}
}

// parseTableDecl parses one module-level "(table ...)" declaration.
//
// Supported form in the current parser:
//   - (table funcref (elem $f ...))
//   - (table $t funcref (elem $f ...))
//   - (table i64 10 funcref)
func (p *Parser) parseTableDecl(sx *SExpr) *TableDecl {
	td := &TableDecl{loc: sx.loc, AddressType: "i32"}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		td.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "export" {
		td.Export = p.matchElement(sx.list[cursor], 1, STRING)
		cursor++
	}
	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "import" {
		modName, fieldName, ok := p.parseImportClause(sx.list[cursor])
		if !ok {
			p.emitError(sx.list[cursor].loc, "invalid table import clause")
			return td
		}
		td.ImportModule = modName
		td.ImportName = fieldName
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "table declaration missing limits or element type")
		return td
	}

	if sx.list[cursor].IsTokenKind(KEYWORD) && (sx.list[cursor].tok.value == "i32" || sx.list[cursor].tok.value == "i64") {
		td.AddressType = sx.list[cursor].tok.value
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "table declaration missing limits or element type")
		return td
	}

	// Inline element shorthand:
	//   (table funcref (elem ...))
	//   (table (ref null $t) (elem ...))
	if sx.list[cursor].IsTokenKind(KEYWORD) || sx.list[cursor].HeadKeyword() == "ref" {
		td.RefTy = p.parseType(sx.list[cursor])
		cursor++
		if cursor >= len(sx.list) {
			return td
		}
		switch sx.list[cursor].HeadKeyword() {
		case "elem":
			p.parseElemRefs(td, sx.list[cursor])
		default:
			td.Init = p.parseFoldedInstr(sx.list[cursor])
		}
		return td
	}

	// Sized table form: (table <min> [<max>] <reftype> [<init-expr>])
	if !sx.list[cursor].IsTokenKind(INT) {
		p.emitError(sx.list[cursor].loc, "table declaration expects minimum size")
		return td
	}
	min, ok := parseU64Token(sx.list[cursor].tok.value)
	if !ok {
		p.emitError(sx.list[cursor].loc, "invalid table minimum size")
		return td
	}
	td.Min = min
	cursor++

	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(INT) {
		max, ok := parseU64Token(sx.list[cursor].tok.value)
		if !ok {
			p.emitError(sx.list[cursor].loc, "invalid table maximum size")
			return td
		}
		td.HasMax = true
		td.Max = max
		cursor++
	}

	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "table declaration missing reference type")
		return td
	}
	td.RefTy = p.parseType(sx.list[cursor])
	cursor++
	if cursor < len(sx.list) {
		switch sx.list[cursor].HeadKeyword() {
		case "elem":
			p.parseElemRefs(td, sx.list[cursor])
		default:
			td.Init = p.parseFoldedInstr(sx.list[cursor])
		}
	}
	return td
}

// parseMemoryDecl parses one module-level "(memory ...)" declaration.
//
// Supported forms include:
//   - (memory 1)
//   - (memory 1 2)
//   - (memory $m (export "mem") 1 2)
//   - (memory (import "M" "m") 1 2)
//   - (memory i64 1 2)
//   - (memory i64 (data "abc"))
//   - (memory (data "abc" "..."))
func (p *Parser) parseMemoryDecl(sx *SExpr) *MemoryDecl {
	md := &MemoryDecl{loc: sx.loc, AddressType: "i32"}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		md.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "export" {
		md.Export = p.matchElement(sx.list[cursor], 1, STRING)
		cursor++
	}
	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "import" {
		modName, fieldName, ok := p.parseImportClause(sx.list[cursor])
		if !ok {
			p.emitError(sx.list[cursor].loc, "invalid memory import clause")
			return md
		}
		md.ImportModule = modName
		md.ImportName = fieldName
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "memory declaration missing minimum size")
		return md
	}

	if sx.list[cursor].IsTokenKind(KEYWORD) && (sx.list[cursor].tok.value == "i32" || sx.list[cursor].tok.value == "i64") {
		md.AddressType = sx.list[cursor].tok.value
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "memory declaration missing minimum size")
		return md
	}
	if sx.list[cursor].IsList() && sx.list[cursor].HeadKeyword() == "data" {
		md.InlineData = p.parseDataStrings(sx.list[cursor])
		return md
	}

	minTok := sx.list[cursor]
	if !minTok.IsTokenKind(INT) {
		p.emitError(minTok.loc, "memory minimum must be INT")
		return md
	}
	min, ok := parseU64Token(minTok.tok.value)
	if !ok {
		p.emitError(minTok.loc, "invalid memory minimum size")
		return md
	}
	md.Min = min
	cursor++

	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(INT) {
		max, ok := parseU64Token(sx.list[cursor].tok.value)
		if !ok {
			p.emitError(sx.list[cursor].loc, "invalid memory maximum size")
			return md
		}
		md.HasMax = true
		md.Max = max
	}
	return md
}

// parseDataDecl parses one top-level "(data ...)" declaration.
// It accepts both active and passive segment forms:
//   - (data <offset-expr> <string>+)
//   - (data (memory <id-or-index>) <offset-expr> <string>+)
//   - (data <string>+)
func (p *Parser) parseDataDecl(sx *SExpr) *DataDecl {
	dd := &DataDecl{loc: sx.loc}
	if len(sx.list) < 2 {
		p.emitError(sx.loc, "data declaration missing payload")
		return dd
	}

	cursor := 1
	if sx.list[cursor].IsTokenKind(ID) {
		dd.Id = sx.list[cursor].tok.value
		cursor++
	}
	if sx.list[cursor].HeadKeyword() == "memory" {
		memClause := sx.list[cursor]
		if len(memClause.list) != 2 {
			p.emitError(memClause.loc, "data memory clause expects one memory reference")
			return dd
		}
		if !memClause.list[1].IsTokenAny(ID, INT) {
			p.emitError(memClause.list[1].loc, "data memory reference must be ID or INT")
			return dd
		}
		dd.MemoryRef = memClause.list[1].tok.value
		cursor++
	}

	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "data declaration missing payload")
		return dd
	}

	if sx.list[cursor].IsTokenKind(STRING) {
		dd.Strings = p.parseDataStringsFrom(sx, cursor)
		return dd
	}
	if !sx.list[cursor].IsList() {
		p.emitError(sx.list[cursor].loc, "data declaration offset must be instruction expression")
		return dd
	}
	offsetExpr := sx.list[cursor]
	if offsetExpr.HeadKeyword() == "offset" {
		if len(offsetExpr.list) != 2 || !offsetExpr.list[1].IsList() {
			p.emitError(offsetExpr.loc, "data offset clause expects one instruction list")
			return dd
		}
		dd.Offset = p.parseFoldedInstr(offsetExpr.list[1])
	} else {
		dd.Offset = p.parseFoldedInstr(offsetExpr)
	}
	dd.Strings = p.parseDataStringsFrom(sx, cursor+1)
	return dd
}

// parseDataStrings parses a "(data ...)" clause payload and returns only its
// string chunks.
func (p *Parser) parseDataStrings(dataClause *SExpr) []string {
	if dataClause == nil || dataClause.HeadKeyword() != "data" {
		return nil
	}
	return p.parseDataStringsFrom(dataClause, 1)
}

// parseDataStringsFrom parses STRING tokens in sx starting at start and returns
// their raw token values. Non-STRING entries are reported as diagnostics.
func (p *Parser) parseDataStringsFrom(sx *SExpr, start int) []string {
	var out []string
	for i := start; i < len(sx.list); i++ {
		if !sx.list[i].IsTokenKind(STRING) {
			p.emitError(sx.list[i].loc, "data string must be STRING")
			continue
		}
		out = append(out, sx.list[i].tok.value)
	}
	return out
}

// parseGlobalDecl parses one module-level "(global ...)" declaration.
//
// Supported forms:
//   - (global $g i32 (i32.const 0))
//   - (global $g (mut i32) (i32.const 0))
func (p *Parser) parseGlobalDecl(sx *SExpr) *GlobalDecl {
	gd := &GlobalDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		gd.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "export" {
		gd.Export = p.matchElement(sx.list[cursor], 1, STRING)
		cursor++
	}
	if cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "import" {
		modName, fieldName, ok := p.parseImportClause(sx.list[cursor])
		if !ok {
			p.emitError(sx.list[cursor].loc, "invalid global import clause")
			return gd
		}
		gd.ImportModule = modName
		gd.ImportName = fieldName
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "global declaration missing type")
		return gd
	}
	tySx := sx.list[cursor]
	if tySx.HeadKeyword() == "mut" {
		if len(tySx.list) != 2 {
			p.emitError(tySx.loc, "invalid mutable global type")
		} else {
			gd.Mutable = true
			gd.Ty = p.parseType(tySx.list[1])
		}
	} else {
		gd.Ty = p.parseType(tySx)
	}
	cursor++
	if gd.ImportModule != "" {
		return gd
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "global declaration missing initializer")
		return gd
	}
	initSx := sx.list[cursor]
	if !initSx.IsList() {
		p.emitError(initSx.loc, "global initializer must be instruction expression")
		return gd
	}
	gd.Init = p.parseFoldedInstr(initSx)
	return gd
}

// parseFuncTypeUse parses a "(func ...)" signature nested inside a type
// declaration and returns the collected param/result declarations.
//
// Example:
//
//	(type $t (func (param i32 i32) (result i32)))
func (p *Parser) parseFuncTypeUse(sx *SExpr) *TypeUse {
	tu := &TypeUse{loc: sx.loc}
	if sx.HeadKeyword() != "func" {
		p.emitError(sx.loc, "type declaration expects (func ...)")
		return tu
	}
	for i := 1; i < len(sx.list); i++ {
		elem := sx.list[i]
		switch elem.HeadKeyword() {
		case "param":
			tu.Params = append(tu.Params, p.parseParamDecl(elem)...)
		case "result":
			tu.Results = append(tu.Results, p.parseResultDecl(elem)...)
		default:
			p.emitError(elem.loc, "unsupported type declaration clause")
		}
	}
	return tu
}

// parseTypeUseClause parses a function type-use clause "(type X)" where X is
// either an identifier (for example "$t") or a numeric type index.
//
// It returns the raw reference text as it appears in source. Invalid clauses
// emit diagnostics and return an empty string.
func (p *Parser) parseTypeUseClause(sx *SExpr) string {
	if len(sx.list) != 2 {
		p.emitError(sx.loc, "invalid '(type' use")
		return ""
	}
	ref := sx.list[1]
	if !ref.IsTokenAny(ID, INT) {
		p.emitError(ref.loc, "type use expects ID or INT")
		return ""
	}
	return ref.tok.value
}

// parseParamDecl parses one "(param ...)" clause and returns one or more
// ParamDecl entries.
//
// Supported forms are:
//   - named single param: "(param $x i32)" -> one ParamDecl {Id: "$x", Ty: i32}
//   - anonymous multi param: "(param i32 i64)" -> two ParamDecl entries
//
// On malformed input it emits a parser error and returns nil.
func (p *Parser) parseParamDecl(sx *SExpr) []*ParamDecl {
	if len(sx.list) == 1 {
		return nil
	}
	if len(sx.list) == 3 && sx.list[1].IsTokenKind(ID) {
		return []*ParamDecl{{
			Id:  p.matchElement(sx, 1, ID),
			Ty:  p.parseType(sx.list[2]),
			loc: sx.loc,
		}}
	}

	if len(sx.list) < 2 {
		p.emitError(sx.loc, "invalid '(param' declaration")
		return nil
	}

	out := make([]*ParamDecl, 0, len(sx.list)-1)
	for i := 1; i < len(sx.list); i++ {
		out = append(out, &ParamDecl{
			Ty:  p.parseType(sx.list[i]),
			loc: sx.loc,
		})
	}
	return out
}

// parseResultDecl parses one "(result ...)" clause and returns one or more
// ResultDecl entries.
//
// Supported forms are:
//   - single result: "(result i32)" -> one ResultDecl {Ty: i32}
//   - multi result: "(result i32 i64)" -> two ResultDecl entries
//
// On malformed input it emits a parser error and returns nil.
func (p *Parser) parseResultDecl(sx *SExpr) []*ResultDecl {
	if len(sx.list) == 1 {
		return nil
	}
	if len(sx.list) < 2 {
		p.emitError(sx.loc, "invalid '(result' declaration")
		return nil
	}

	out := make([]*ResultDecl, 0, len(sx.list)-1)
	for i := 1; i < len(sx.list); i++ {
		out = append(out, &ResultDecl{
			Ty:  p.parseType(sx.list[i]),
			loc: sx.loc,
		})
	}
	return out
}

// parseLocalDecl parses one "(local ...)" clause and returns one or more
// LocalDecl entries.
//
// Supported forms are:
//   - named single local: "(local $x i64)" -> one LocalDecl {Id: "$x", Ty: i64}
//   - anonymous multi local: "(local i64 i64)" -> two LocalDecl entries
//
// The parser first checks for the named-single form because otherwise a simple
// token loop would try to parse "$x" as a type. If no leading ID exists, all
// remaining items are treated as anonymous local types.
func (p *Parser) parseLocalDecl(sx *SExpr) []*LocalDecl {
	if len(sx.list) == 1 {
		return nil
	}
	if len(sx.list) == 3 && sx.list[1].IsTokenKind(ID) {
		return []*LocalDecl{{
			Id:  p.matchElement(sx, 1, ID),
			Ty:  p.parseType(sx.list[2]),
			loc: sx.loc,
		}}
	}

	if len(sx.list) < 2 {
		p.emitError(sx.loc, "invalid '(local' declaration")
		return nil
	}

	out := make([]*LocalDecl, 0, len(sx.list)-1)
	for i := 1; i < len(sx.list); i++ {
		out = append(out, &LocalDecl{
			Ty:  p.parseType(sx.list[i]),
			loc: sx.loc,
		})
	}
	return out
}

// parseType parses a value/reference type syntax node and returns the
// corresponding AST type, or nil after emitting diagnostics.
func (p *Parser) parseType(sx *SExpr) Type {
	if sx.HeadKeyword() == "ref" {
		elems := sx.Children()
		if len(elems) == 2 && elems[1].IsTokenAny(KEYWORD, ID, INT) {
			return &RefType{Nullable: false, HeapType: elems[1].tok.value}
		}
		if len(elems) == 3 &&
			elems[1].IsKeywordToken("null") &&
			elems[2].IsTokenAny(KEYWORD, ID, INT) {
			return &RefType{Nullable: true, HeapType: elems[2].tok.value}
		}
		p.emitError(sx.loc, "invalid ref type")
		return nil
	}
	if sx.IsTokenKind(KEYWORD) {
		name := sx.tok.value
		if _, ok := basicTypes[name]; ok {
			return &BasicType{Name: name}
		}
	}

	p.emitError(sx.loc, "invalid type")
	return nil
}

func (p *Parser) parseStructTypeFields(sx *SExpr) []*FieldDecl {
	fields := make([]*FieldDecl, 0, len(sx.list)-1)
	for i := 1; i < len(sx.list); i++ {
		fields = append(fields, p.parseFieldDecls(sx.list[i])...)
	}
	return fields
}

func (p *Parser) parseArrayTypeField(sx *SExpr) *FieldDecl {
	if len(sx.list) != 2 {
		p.emitError(sx.loc, "array type expects exactly one field type")
		return nil
	}
	return p.parseFieldType(sx.list[1])
}

// parseFieldDecls parses one struct field clause "(field ...)".
//
// The parser expects `sx` to be the nested "(field ...)" S-expression from a
// struct type body, not the enclosing "(struct ...)" form.
//
// Supported shapes are:
//   - (field i32)
//   - (field (mut i16))
//   - (field i32 i32)
//   - (field $name (ref null any))
//
// Unnamed field clauses may declare multiple field types, producing multiple
// FieldDecl entries. A named field clause may declare exactly one field type.
func (p *Parser) parseFieldDecls(sx *SExpr) []*FieldDecl {
	if sx.HeadKeyword() != "field" {
		p.emitError(sx.loc, "struct type expects (field ...)")
		return nil
	}
	if len(sx.list) == 1 {
		return nil
	}
	cursor := 1
	fieldID := ""
	if sx.list[cursor].IsTokenKind(ID) {
		if !sx.list[cursor].IsTokenKind(ID) {
			p.emitError(sx.list[cursor].loc, "field id must be an identifier")
			return nil
		}
		fieldID = sx.list[cursor].tok.value
		cursor++
		if cursor >= len(sx.list) {
			p.emitError(sx.loc, "field declaration with id requires a field type")
			return nil
		}
	}

	fields := make([]*FieldDecl, 0, len(sx.list)-cursor)
	for ; cursor < len(sx.list); cursor++ {
		field := p.parseFieldType(sx.list[cursor])
		if field == nil {
			continue
		}
		if fieldID != "" {
			if len(fields) > 0 {
				p.emitError(sx.loc, "field declaration with id accepts exactly one field type")
				return fields
			}
			field.Id = fieldID
		}
		fields = append(fields, field)
	}
	return fields
}

func (p *Parser) parseFieldType(sx *SExpr) *FieldDecl {
	if sx.HeadKeyword() == "mut" {
		if len(sx.list) != 2 {
			p.emitError(sx.loc, "invalid mutable field type")
			return nil
		}
		return &FieldDecl{
			Ty:      p.parseFieldStorageType(sx.list[1]),
			Mutable: true,
			loc:     sx.loc,
		}
	}
	return &FieldDecl{
		Ty:  p.parseFieldStorageType(sx),
		loc: sx.loc,
	}
}

func (p *Parser) parseFieldStorageType(sx *SExpr) Type {
	if sx.IsTokenKind(KEYWORD) {
		switch sx.tok.value {
		case "i8", "i16":
			return &BasicType{Name: sx.tok.value}
		}
	}
	return p.parseType(sx)
}

// parseElemRefs parses an inline "(elem ...)" payload inside a table
// declaration. It accepts function references by ID/INT and folded item
// expressions.
func (p *Parser) parseElemRefs(td *TableDecl, elemClause *SExpr) {
	if elemClause.HeadKeyword() != "elem" {
		p.emitError(elemClause.loc, `table declaration expects "(elem ...)"`)
		return
	}
	for i := 1; i < len(elemClause.list); i++ {
		elem := elemClause.list[i]
		switch {
		case elem.IsTokenKind(KEYWORD) &&
			(elem.tok.value == "func" || elem.tok.value == "funcref" || elem.tok.value == "externref"):
			// Grammar markers in elem payload; they don't represent entries.
		case elem.IsTokenAny(ID, INT):
			td.ElemRefs = append(td.ElemRefs, elem.tok.value)
		case elem.IsToken():
			p.emitError(elem.loc, "elem entry must be function ID/INT or reference expression")
		case elem.HeadKeyword() == "item":
			td.ElemExprs = append(td.ElemExprs, p.parseElemItemExprs(elem)...)
		default:
			td.ElemExprs = append(td.ElemExprs, p.parseFoldedInstr(elem))
		}
	}
}

// parseImportClause parses "(import \"mod\" \"name\")" and returns module/name.
// It returns ok=false on malformed input.
func (p *Parser) parseImportClause(sx *SExpr) (string, string, bool) {
	if sx.HeadKeyword() != "import" {
		return "", "", false
	}
	if len(sx.list) != 3 {
		return "", "", false
	}
	mod := p.matchElement(sx, 1, STRING)
	field := p.matchElement(sx, 2, STRING)
	if mod == "" || field == "" {
		return "", "", false
	}
	return mod, field, true
}

// parseElemDecl parses one module-level "(elem ...)" declaration.
// It handles active/passive/declarative forms and both function-ref and
// expression payloads.
func (p *Parser) parseElemDecl(sx *SExpr) *ElemDecl {
	ed := &ElemDecl{Mode: ElemModeActive, loc: sx.loc}
	cursor := 1

	if cursor < len(sx.list) && sx.list[cursor].IsTokenKind(ID) {
		ed.Id = sx.list[cursor].tok.value
		cursor++
	}

	if cursor < len(sx.list) && sx.list[cursor].IsKeywordToken("declare") {
		ed.Mode = ElemModeDeclarative
		cursor++
	}

	if ed.Mode != ElemModeDeclarative && cursor < len(sx.list) && sx.list[cursor].HeadKeyword() == "table" {
		tableClause := sx.list[cursor]
		if len(tableClause.list) != 2 {
			p.emitError(tableClause.loc, "elem table clause expects one table reference")
			return ed
		}
		if !tableClause.list[1].IsTokenAny(ID, INT) {
			p.emitError(tableClause.list[1].loc, "elem table reference must be ID or INT")
			return ed
		}
		ed.TableRef = tableClause.list[1].tok.value
		cursor++
	}

	if ed.Mode != ElemModeDeclarative && cursor < len(sx.list) && sx.list[cursor].IsList() {
		switch sx.list[cursor].HeadKeyword() {
		case "offset":
			if len(sx.list[cursor].list) != 2 || !sx.list[cursor].list[1].IsList() {
				p.emitError(sx.list[cursor].loc, "elem offset clause expects one instruction list")
			} else {
				ed.Offset = p.parseFoldedInstr(sx.list[cursor].list[1])
			}
			cursor++
		case "ref", "item":
			// Passive expr payload starts directly.
		default:
			// Active shorthand: direct offset expression like "(i32.const 0)".
			ed.Offset = p.parseFoldedInstr(sx.list[cursor])
			cursor++
		}
	}
	if ed.Mode != ElemModeDeclarative && ed.Offset == nil {
		if ed.TableRef != "" {
			p.emitError(sx.loc, "elem declaration missing offset expression")
			return ed
		}
		ed.Mode = ElemModePassive
	}

	if cursor < len(sx.list) {
		elem := sx.list[cursor]
		if elem.IsKeywordToken("func") {
			cursor++
		} else if (elem.IsTokenKind(KEYWORD) && p.parseType(elem) != nil) || elem.HeadKeyword() == "ref" {
			ed.RefTy = p.parseType(elem)
			cursor++
		}
	}

	for ; cursor < len(sx.list); cursor++ {
		ref := sx.list[cursor]
		if ref.IsTokenAny(ID, INT) {
			ed.FuncRefs = append(ed.FuncRefs, ref.tok.value)
			continue
		}
		if !ref.IsList() {
			p.emitError(ref.loc, "elem entry must be function ID/INT or reference expression")
			continue
		}
		if ref.HeadKeyword() == "item" {
			ed.Exprs = append(ed.Exprs, p.parseElemItemExprs(ref)...)
			continue
		}
		ed.Exprs = append(ed.Exprs, p.parseFoldedInstr(ref))
	}

	if len(ed.FuncRefs) > 0 && len(ed.Exprs) > 0 {
		p.emitError(sx.loc, "elem declaration must not mix function refs and expression payload")
	}
	return ed
}

// parseElemItemExprs parses an "(item ...)" element payload and returns exactly
// one expression instruction when valid.
func (p *Parser) parseElemItemExprs(item *SExpr) []Instruction {
	if len(item.list) < 2 {
		p.emitError(item.loc, "elem item must contain an expression")
		return nil
	}
	if len(item.list) == 2 && item.list[1].IsList() {
		return []Instruction{p.parseFoldedInstr(item.list[1])}
	}
	instrs := p.parseInstrs(item, 1)
	if len(instrs) != 1 {
		p.emitError(item.loc, "elem item must contain exactly one expression")
		return nil
	}
	return instrs
}

// parseInstrs parses a list of instructions from sx, starting at [idx]. It
// expects all tokens from [idx] until the end of sx to represent instructions,
// and will emit errors otherwise.
func (p *Parser) parseInstrs(sx *SExpr, idx int) []Instruction {
	var out []Instruction

	for cursor := idx; cursor < len(sx.list); {
		instr, next := p.parseInstructionElems(sx.list, cursor)
		if instr != nil {
			out = append(out, instr)
		}
		cursor = next
	}

	return out
}

// parseInstructionElems parses one instruction starting at elems[cursor].
//
// It returns the parsed instruction plus the index of the next unread element.
// The element slice is expected to contain either:
//   - a folded instruction list, or
//   - a plain instruction keyword followed by any immediate operands.
func (p *Parser) parseInstructionElems(elems []*SExpr, cursor int) (Instruction, int) {
	elem := elems[cursor]
	if elem.IsList() {
		return p.parseFoldedInstr(elem), cursor + 1
	}
	if !elem.IsTokenKind(KEYWORD) {
		p.emitError(elem.loc, "expected instruction keyword, found %s", elem.tok.name)
		return nil, cursor + 1
	}

	name := elem.tok.value
	if instructionHasSyntaxClass(name, instrdef.InstrSyntaxStructured) && cursor+1 < len(elems) {
		if typeOp, ok := p.parsePlainControlTypeOperand(elems[cursor+1]); ok {
			return &PlainInstr{Name: name, Operands: []Operand{typeOp}, loc: elem.loc}, cursor + 2
		}
	}
	switch name {
	case "local.get", "local.set", "local.tee", "call", "call_ref", "br", "br_if", "br_on_null", "br_on_non_null", "global.get", "global.set", "ref.func", "i32.const", "i64.const", "f32.const", "f64.const", "ref.null", "memory.init", "data.drop",
		"i8x16.extract_lane_s", "i8x16.extract_lane_u", "i8x16.replace_lane",
		"i16x8.extract_lane_s", "i16x8.extract_lane_u", "i16x8.replace_lane",
		"i32x4.extract_lane", "i32x4.replace_lane",
		"i64x2.extract_lane", "i64x2.replace_lane",
		"f32x4.extract_lane", "f32x4.replace_lane",
		"f64x2.extract_lane", "f64x2.replace_lane":
		if cursor+1 >= len(elems) {
			p.emitError(elem.loc, "%s expects one operand", name)
			return nil, cursor + 1
		}
		operandSx := elems[cursor+1]
		operand := p.parseOperand(operandSx)
		if operand == nil {
			p.emitError(operandSx.loc, "invalid operand for %s", name)
			return nil, cursor + 2
		}
		if !isValidPlainOperand(name, operand) {
			p.emitError(operandSx.loc, "invalid operand for %s", name)
			return nil, cursor + 2
		}
		return &PlainInstr{Name: name, Operands: []Operand{operand}, loc: elem.loc}, cursor + 2
	case "i8x16.shuffle":
		operands := make([]Operand, 0, 16)
		next := cursor + 1
		for next < len(elems) {
			op := p.parseOperand(elems[next])
			if op == nil {
				break
			}
			operands = append(operands, op)
			next++
		}
		if len(operands) != 16 {
			p.emitError(elem.loc, "invalid lane length")
			return nil, next
		}
		return &PlainInstr{Name: name, Operands: operands, loc: elem.loc}, next
	case "v128.const":
		operands := make([]Operand, 0, 17)
		next := cursor + 1
		for next < len(elems) {
			op := p.parseOperand(elems[next])
			if op == nil {
				break
			}
			operands = append(operands, op)
			next++
		}
		if len(operands) == 0 {
			p.emitError(elem.loc, "v128.const expects operands")
			return nil, cursor + 1
		}
		return &PlainInstr{Name: name, Operands: operands, loc: elem.loc}, next
	case "table.get", "table.set", "table.grow", "table.size":
		// Table ops accept an optional immediate table index; when omitted,
		// table index 0 is implied.
		operands := []Operand{}
		if cursor+1 < len(elems) {
			next := elems[cursor+1]
			op := p.parseOperand(next)
			switch op.(type) {
			case *IdOperand, *IntOperand:
				operands = append(operands, op)
				return &PlainInstr{Name: name, Operands: operands, loc: elem.loc}, cursor + 2
			}
		}
		return &PlainInstr{Name: name, loc: elem.loc}, cursor + 1
	case "struct.new", "struct.new_default", "array.new", "array.new_default", "array.get_s", "array.get_u", "array.fill":
		if cursor+1 >= len(elems) {
			p.emitError(elem.loc, "%s expects one operand", name)
			return nil, cursor + 1
		}
		operand := p.parseOperand(elems[cursor+1])
		switch operand.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid operand for %s", name)
			return nil, cursor + 2
		}
		return &PlainInstr{Name: name, Operands: []Operand{operand}, loc: elem.loc}, cursor + 2
	case "array.new_data", "array.new_elem", "array.init_data", "array.init_elem":
		if cursor+2 >= len(elems) {
			p.emitError(elem.loc, "%s expects two operands", name)
			return nil, cursor + 1
		}
		typeOp := p.parseOperand(elems[cursor+1])
		dataOp := p.parseOperand(elems[cursor+2])
		switch typeOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		switch dataOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+2].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		return &PlainInstr{Name: name, Operands: []Operand{typeOp, dataOp}, loc: elem.loc}, cursor + 3
	case "array.copy":
		if cursor+2 >= len(elems) {
			p.emitError(elem.loc, "%s expects two operands", name)
			return nil, cursor + 1
		}
		dstOp := p.parseOperand(elems[cursor+1])
		srcOp := p.parseOperand(elems[cursor+2])
		switch dstOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		switch srcOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+2].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		return &PlainInstr{Name: name, Operands: []Operand{dstOp, srcOp}, loc: elem.loc}, cursor + 3
	case "struct.get", "struct.get_s", "struct.get_u":
		if cursor+2 >= len(elems) {
			p.emitError(elem.loc, "%s expects two operands", name)
			return nil, cursor + 1
		}
		typeOp := p.parseOperand(elems[cursor+1])
		fieldOp := p.parseOperand(elems[cursor+2])
		switch typeOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		switch fieldOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+2].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		return &PlainInstr{Name: name, Operands: []Operand{typeOp, fieldOp}, loc: elem.loc}, cursor + 3
	case "struct.set":
		if cursor+2 >= len(elems) {
			p.emitError(elem.loc, "%s expects two operands", name)
			return nil, cursor + 1
		}
		typeOp := p.parseOperand(elems[cursor+1])
		fieldOp := p.parseOperand(elems[cursor+2])
		switch typeOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		switch fieldOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+2].loc, "invalid operand for %s", name)
			return nil, cursor + 3
		}
		return &PlainInstr{Name: name, Operands: []Operand{typeOp, fieldOp}, loc: elem.loc}, cursor + 3
	case "array.get", "array.set":
		if cursor+1 >= len(elems) {
			p.emitError(elem.loc, "%s expects one operand", name)
			return nil, cursor + 1
		}
		operand := p.parseOperand(elems[cursor+1])
		switch operand.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid operand for %s", name)
			return nil, cursor + 2
		}
		return &PlainInstr{Name: name, Operands: []Operand{operand}, loc: elem.loc}, cursor + 2
	case "br_on_cast", "br_on_cast_fail":
		if cursor+3 >= len(elems) {
			p.emitError(elem.loc, "%s expects branch depth and two reference types", name)
			return nil, cursor + 1
		}
		labelOp := p.parseOperand(elems[cursor+1])
		switch labelOp.(type) {
		case *IdOperand, *IntOperand:
		default:
			p.emitError(elems[cursor+1].loc, "invalid branch depth for %s", name)
			return nil, cursor + 2
		}
		srcTy := p.parseTypeOperand(elems[cursor+2])
		dstTy := p.parseTypeOperand(elems[cursor+3])
		if srcTy == nil {
			p.emitError(elems[cursor+2].loc, "invalid source type for %s", name)
			return nil, cursor + 4
		}
		if dstTy == nil {
			p.emitError(elems[cursor+3].loc, "invalid destination type for %s", name)
			return nil, cursor + 4
		}
		return &PlainInstr{Name: name, Operands: []Operand{labelOp, srcTy, dstTy}, loc: elem.loc}, cursor + 4
	default:
		if instructionHasSyntaxClass(name, instrdef.InstrSyntaxMemory) {
			operands := make([]Operand, 0, 3)
			next := cursor + 1
			for next < len(elems) {
				op := p.parseOperand(elems[next])
				if op == nil {
					break
				}
				switch operand := op.(type) {
				case *IdOperand, *IntOperand:
					operands = append(operands, operand)
					next++
				case *KeywordOperand:
					if !strings.Contains(operand.Value, "=") {
						return &PlainInstr{Name: name, Operands: operands, loc: elem.loc}, next
					}
					operands = append(operands, operand)
					next++
				default:
					return &PlainInstr{Name: name, Operands: operands, loc: elem.loc}, next
				}
			}
			return &PlainInstr{Name: name, Operands: operands, loc: elem.loc}, next
		}
	}
	// For this initial subset, parse all other instructions as plain
	// zero-operand instructions.
	return &PlainInstr{Name: name, loc: elem.loc}, cursor + 1
}

// parsePlainControlTypeOperand parses a single-result blocktype clause used by
// linear control forms such as `if (result i32)` and `block (result (ref $t))`.
func (p *Parser) parsePlainControlTypeOperand(sx *SExpr) (Operand, bool) {
	if !sx.IsList() || sx.HeadKeyword() != "result" {
		return nil, false
	}
	if len(sx.list) != 2 {
		p.emitError(sx.loc, "plain control result clause expects exactly one type")
		return nil, true
	}
	ty := p.parseType(sx.list[1])
	if ty == nil {
		p.emitError(sx.loc, "invalid plain control result type")
		return nil, true
	}
	return &TypeOperand{Ty: ty, loc: sx.loc}, true
}

// parseFoldedInstr parses one folded instruction expression and preserves it
// as a folded AST node.
//
// It expects sx to be a list of the form "(op arg...)", where each arg is
// either a nested folded instruction list or a plain operand token.
//
// Example:
//
//	(i32.add (local.get $x) (local.get $y))
//
// and is represented as a FoldedInstr("i32.add") with two nested
// FoldedInstr children.
func (p *Parser) parseFoldedInstr(sx *SExpr) Instruction {
	if !sx.IsList() || len(sx.list) == 0 {
		p.emitError(sx.loc, "expected folded instruction list")
		return nil
	}
	head := sx.list[0]
	if !head.IsTokenKind(KEYWORD) {
		p.emitError(head.loc, "expected folded instruction keyword")
		return nil
	}

	name := head.tok.value
	if name == "block" || name == "loop" || name == "then" || name == "else" {
		return p.parseFoldedStructuredInstr(sx, name)
	}
	var args []FoldedArg

	for i := 1; i < len(sx.list); i++ {
		elem := sx.list[i]
		if elem.IsList() {
			child := p.parseFoldedInstr(elem)
			if child == nil {
				p.emitError(elem.loc, "invalid nested instruction for %s", name)
				continue
			}
			args = append(args, FoldedArg{Instr: child})
			continue
		}

		op := p.parseOperand(elem)
		if op == nil {
			p.emitError(elem.loc, "invalid operand for %s", name)
			continue
		}
		args = append(args, FoldedArg{Operand: op})
	}

	return &FoldedInstr{Name: name, Args: args, loc: head.loc}
}

// parseFoldedStructuredInstr parses a folded structured instruction or clause
// that may contain a mixture of nested folded forms and flat instruction
// tokens in its body.
//
// Examples:
//
//	(loop $l (block $done
//	  (i32.eqz (local.get 0))
//	  br_if $done
//	  br $l))
//
//	(then
//	  local.get 0
//	  drop)
func (p *Parser) parseFoldedStructuredInstr(sx *SExpr, name string) Instruction {
	args := make([]FoldedArg, 0, len(sx.list)-1)
	cursor := 1
	if (name == "block" || name == "loop") &&
		cursor < len(sx.list) &&
		sx.list[cursor].IsTokenKind(ID) {
		op := p.parseOperand(sx.list[cursor])
		args = append(args, FoldedArg{Operand: op})
		cursor++
	}

	for cursor < len(sx.list) {
		elem := sx.list[cursor]
		if elem.IsList() {
			child := p.parseFoldedInstr(elem)
			if child == nil {
				p.emitError(elem.loc, "invalid nested instruction for %s", name)
			} else {
				args = append(args, FoldedArg{Instr: child})
			}
			cursor++
			continue
		}

		instr, next := p.parseInstructionElems(sx.list, cursor)
		if instr != nil {
			args = append(args, FoldedArg{Instr: instr})
		}
		cursor = next
	}

	return &FoldedInstr{Name: name, Args: args, loc: sx.list[0].loc}
}

// parseOperand parses a token node as an instruction operand.
// It returns nil for non-token nodes or unsupported token kinds.
func (p *Parser) parseOperand(sx *SExpr) Operand {
	if !sx.IsToken() {
		return nil
	}

	switch sx.tok.name {
	case ID:
		return &IdOperand{Value: sx.tok.value, loc: sx.loc}
	case INT:
		return &IntOperand{Value: sx.tok.value, loc: sx.loc}
	case FLOAT:
		return &FloatOperand{Value: sx.tok.value, loc: sx.loc}
	case STRING:
		return &StringOperand{Value: sx.tok.value, loc: sx.loc}
	case KEYWORD:
		return &KeywordOperand{Value: sx.tok.value, loc: sx.loc}
	default:
		return nil
	}
}

func (p *Parser) parseTypeOperand(sx *SExpr) Operand {
	ty := p.parseType(sx)
	if ty == nil {
		return nil
	}
	return &TypeOperand{Ty: ty, loc: sx.loc}
}

// isValidPlainOperand validates operand type constraints for plain (flat) WAT
// instructions that have one immediate operand.
func isValidPlainOperand(name string, op Operand) bool {
	switch name {
	case "local.get", "local.set", "local.tee", "call", "call_ref", "br", "br_if", "br_on_null", "br_on_non_null", "global.get", "global.set", "ref.func", "memory.init", "data.drop":
		switch op.(type) {
		case *IdOperand, *IntOperand:
			return true
		default:
			return false
		}
	case "i32.const", "i64.const":
		_, ok := op.(*IntOperand)
		return ok
	case "f32.const", "f64.const":
		switch op.(type) {
		case *IntOperand, *FloatOperand:
			return true
		default:
			return false
		}
	case "ref.null":
		switch op.(type) {
		case *IdOperand, *KeywordOperand:
			return true
		default:
			return false
		}
	default:
		return true
	}
}

// parseU32Token parses an INT token text into uint32, accepting '_' separators
// and base prefixes supported by strconv.ParseInt.
func parseU32Token(s string) (uint32, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	n, err := strconv.ParseInt(clean, 0, 64)
	if err != nil || n < 0 || n > (1<<32-1) {
		return 0, false
	}
	return uint32(n), true
}

// parseU64Token parses an INT token text into uint64, accepting '_' separators
// and base prefixes supported by strconv.ParseUint.
func parseU64Token(s string) (uint64, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	n, err := strconv.ParseUint(clean, 0, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
