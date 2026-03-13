package textformat

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/eliben/watgo/diag"
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

func (p *Parser) emitError(loc location, msg string) {
	p.errs.Addf("%s: %s", loc, msg)
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
		p.emitError(sx.loc, fmt.Sprintf("expected list with at least %d items", idx+1))
		return ""
	}

	sub := sx.list[idx]
	if !sub.IsToken() {
		p.emitError(sub.loc, fmt.Sprintf("expected %s, found list", tokname))
		return ""
	}
	if sub.tok.name != tokname {
		p.emitError(sub.loc, fmt.Sprintf("expected %s, found %s", tokname, sub.tok.value))
		return ""
	}

	return sub.tok.value
}

func (p *Parser) parseModule(sx *SExpr) *Module {
	if sx.HeadKeyword() != "module" {
		p.emitError(sx.loc, "expected 'module'")
		return nil
	}

	m := &Module{loc: sx.loc}
	// Optional module name
	cursor := 1
	if len(sx.list) > 1 && sx.list[cursor].tok.name == ID {
		m.Id = sx.list[cursor].tok.value
		cursor++
	}
	// .wast script commands may include "(module definition ...)".
	if cursor < len(sx.list) && sx.list[cursor].IsToken() &&
		sx.list[cursor].tok.name == KEYWORD && sx.list[cursor].tok.value == "definition" {
		cursor++
	}

	for i := cursor; i < len(sx.list); i++ {
		sub := sx.list[i]
		if sub.HeadKeyword() == "type" {
			m.Types = append(m.Types, p.parseTypeDecl(sub))
		} else if sub.HeadKeyword() == "table" {
			m.Tables = append(m.Tables, p.parseTableDecl(sub))
		} else if sub.HeadKeyword() == "memory" {
			m.Memories = append(m.Memories, p.parseMemoryDecl(sub))
		} else if sub.HeadKeyword() == "global" {
			m.Globals = append(m.Globals, p.parseGlobalDecl(sub))
		} else if sub.HeadKeyword() == "func" {
			m.Funcs = append(m.Funcs, p.parseFunction(sub))
		} else {
			p.emitError(sub.loc, fmt.Sprintf("unsupported module field %q", sub.HeadKeyword()))
		}
	}

	return m
}

func (p *Parser) parseFunction(sx *SExpr) *Function {
	f := &Function{
		TyUse: &TypeUse{},
		loc:   sx.loc,
	}

	// Optional function name
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
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
		if elem.HeadKeyword() == "type" {
			f.TyUse.Id = p.parseTypeUseClause(elem)
		} else if elem.HeadKeyword() == "param" {
			f.TyUse.Params = append(f.TyUse.Params, p.parseParamDecl(elem)...)
		} else if elem.HeadKeyword() == "result" {
			f.TyUse.Results = append(f.TyUse.Results, p.parseResultDecl(elem)...)
		} else if elem.HeadKeyword() == "local" {
			f.Locals = append(f.Locals, p.parseLocalDecl(elem)...)
		} else {
			// Neither of these, so the instruction sequence started. Parse the
			// entire instruction sequence.
			f.Instrs = p.parseInstrs(sx, cursor)
			break
		}
	}

	return f
}

// parseTypeDecl parses one module-level "(type ...)" declaration.
//
// Expected forms:
//   - (type (func ...))
//   - (type $name (func ...))
//
// It returns a TypeDecl even on malformed input so parsing can continue; errors
// are reported through parser diagnostics.
func (p *Parser) parseTypeDecl(sx *SExpr) *TypeDecl {
	td := &TypeDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
		td.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "type declaration missing function signature")
		td.TyUse = &TypeUse{}
		return td
	}
	td.TyUse = p.parseFuncTypeUse(sx.list[cursor])
	return td
}

// parseTableDecl parses one module-level "(table ...)" declaration.
//
// Supported form in the current parser:
//   - (table funcref (elem $f ...))
//   - (table $t funcref (elem $f ...))
func (p *Parser) parseTableDecl(sx *SExpr) *TableDecl {
	td := &TableDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
		td.Id = sx.list[cursor].tok.value
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

	// Legacy shorthand: (table funcref (elem ...))
	if sx.list[cursor].IsToken() && sx.list[cursor].tok.name == KEYWORD {
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
	if !sx.list[cursor].IsToken() || sx.list[cursor].tok.name != INT {
		p.emitError(sx.list[cursor].loc, "table declaration expects minimum size")
		return td
	}
	min, ok := parseU32Token(sx.list[cursor].tok.value)
	if !ok {
		p.emitError(sx.list[cursor].loc, "invalid table minimum size")
		return td
	}
	td.Min = min
	cursor++

	if cursor < len(sx.list) && sx.list[cursor].IsToken() && sx.list[cursor].tok.name == INT {
		max, ok := parseU32Token(sx.list[cursor].tok.value)
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
// Supported forms:
//   - (memory 1)
//   - (memory $m 1)
func (p *Parser) parseMemoryDecl(sx *SExpr) *MemoryDecl {
	md := &MemoryDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
		md.Id = sx.list[cursor].tok.value
		cursor++
	}
	if cursor >= len(sx.list) {
		p.emitError(sx.loc, "memory declaration missing minimum size")
		return md
	}
	minTok := sx.list[cursor]
	if !minTok.IsToken() || minTok.tok.name != INT {
		p.emitError(minTok.loc, "memory minimum must be INT")
		return md
	}
	min, ok := parseU32Token(minTok.tok.value)
	if !ok {
		p.emitError(minTok.loc, "invalid memory minimum size")
		return md
	}
	md.Min = min
	return md
}

// parseGlobalDecl parses one module-level "(global ...)" declaration.
//
// Supported forms:
//   - (global $g i32 (i32.const 0))
//   - (global $g (mut i32) (i32.const 0))
func (p *Parser) parseGlobalDecl(sx *SExpr) *GlobalDecl {
	gd := &GlobalDecl{loc: sx.loc}
	cursor := 1
	if cursor < len(sx.list) && sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
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
	if tySx.IsList() && tySx.HeadKeyword() == "mut" {
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
	if !ref.IsToken() || (ref.tok.name != ID && ref.tok.name != INT) {
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
	if len(sx.list) == 3 && sx.list[1].IsToken() && sx.list[1].tok.name == ID {
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
	if len(sx.list) == 3 && sx.list[1].IsToken() && sx.list[1].tok.name == ID {
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

func (p *Parser) parseType(sx *SExpr) Type {
	if sx.IsList() && sx.HeadKeyword() == "ref" {
		elems := sx.Children()
		if len(elems) == 2 && elems[1].IsToken() && (elems[1].tok.name == KEYWORD || elems[1].tok.name == ID) {
			return &RefType{Nullable: false, HeapType: elems[1].tok.value}
		}
		if len(elems) == 3 &&
			elems[1].IsToken() && elems[1].tok.name == KEYWORD && elems[1].tok.value == "null" &&
			elems[2].IsToken() && (elems[2].tok.name == KEYWORD || elems[2].tok.name == ID) {
			return &RefType{Nullable: true, HeapType: elems[2].tok.value}
		}
		p.emitError(sx.loc, "invalid ref type")
		return nil
	}
	if sx.IsToken() && sx.tok.name == KEYWORD {
		name := sx.tok.value
		if _, ok := basicTypes[name]; ok {
			return &BasicType{Name: name}
		}
	}

	p.emitError(sx.loc, "invalid type")
	return nil
}

func (p *Parser) parseElemRefs(td *TableDecl, elemClause *SExpr) {
	if elemClause.HeadKeyword() != "elem" {
		p.emitError(elemClause.loc, `table declaration expects "(elem ...)"`)
		return
	}
	for i := 1; i < len(elemClause.list); i++ {
		elem := elemClause.list[i]
		if !elem.IsToken() || (elem.tok.name != ID && elem.tok.name != INT) {
			p.emitError(elem.loc, "elem entry must be function ID or INT")
			continue
		}
		td.ElemRefs = append(td.ElemRefs, elem.tok.value)
	}
}

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

// parseInstrs parses a list of instructions from sx, starting at [idx]. It
// expects all tokens from [idx] until the end of sx to represent instructions,
// and will emit errors otherwise.
func (p *Parser) parseInstrs(sx *SExpr, idx int) []Instruction {
	var out []Instruction

	for cursor := idx; cursor < len(sx.list); {
		elem := sx.list[cursor]
		if elem.IsList() {
			fi := p.parseFoldedInstr(elem)
			if fi != nil {
				out = append(out, fi)
			}
			cursor++
			continue
		}
		if elem.tok.name != KEYWORD {
			p.emitError(elem.loc, fmt.Sprintf("expected instruction keyword, found %s", elem.tok.name))
			cursor++
			continue
		}

		name := elem.tok.value
		switch name {
		case "local.get":
			if cursor+1 >= len(sx.list) {
				p.emitError(elem.loc, "local.get expects one operand")
				cursor++
				continue
			}

			operandSx := sx.list[cursor+1]
			operand := p.parseOperand(operandSx)
			switch operand.(type) {
			case *IdOperand, *IntOperand:
				out = append(out, &PlainInstr{Name: name, Operands: []Operand{operand}, loc: elem.loc})
			default:
				p.emitError(operandSx.loc, "local.get operand must be ID or INT")
			}
			cursor += 2
		case "call":
			if cursor+1 >= len(sx.list) {
				p.emitError(elem.loc, "call expects one operand")
				cursor++
				continue
			}

			operandSx := sx.list[cursor+1]
			operand := p.parseOperand(operandSx)
			switch operand.(type) {
			case *IdOperand, *IntOperand:
				out = append(out, &PlainInstr{Name: name, Operands: []Operand{operand}, loc: elem.loc})
			default:
				p.emitError(operandSx.loc, "call operand must be ID or INT")
			}
			cursor += 2

		default:
			// For this initial subset, parse all other instructions as plain
			// zero-operand instructions.
			out = append(out, &PlainInstr{Name: name, loc: elem.loc})
			cursor++
		}
	}

	return out
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
	if !head.IsToken() || head.tok.name != KEYWORD {
		p.emitError(head.loc, "expected folded instruction keyword")
		return nil
	}

	name := head.tok.value
	var args []FoldedArg

	for i := 1; i < len(sx.list); i++ {
		elem := sx.list[i]
		if elem.IsList() {
			child := p.parseFoldedInstr(elem)
			if child == nil {
				p.emitError(elem.loc, fmt.Sprintf("invalid nested instruction for %s", name))
				continue
			}
			args = append(args, FoldedArg{Instr: child})
			continue
		}

		op := p.parseOperand(elem)
		if op == nil {
			p.emitError(elem.loc, fmt.Sprintf("invalid operand for %s", name))
			continue
		}
		args = append(args, FoldedArg{Operand: op})
	}

	return &FoldedInstr{Name: name, Args: args, loc: head.loc}
}

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

func parseU32Token(s string) (uint32, bool) {
	clean := strings.ReplaceAll(s, "_", "")
	n, err := strconv.ParseInt(clean, 0, 64)
	if err != nil || n < 0 || n > (1<<32-1) {
		return 0, false
	}
	return uint32(n), true
}
