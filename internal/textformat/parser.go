package textformat

import (
	"fmt"

	"github.com/eliben/watgo/diag"
)

type Parser struct {
	errs diag.ErrorList
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

	for i := cursor; i < len(sx.list); i++ {
		sub := sx.list[i]
		if sub.HeadKeyword() == "func" {
			m.Funcs = append(m.Funcs, p.parseFunction(sub))
		}
		// TODO: check all other types too
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
		if elem.HeadKeyword() == "param" {
			f.TyUse.Params = append(f.TyUse.Params, p.parseParamDecl(elem)...)
		} else if elem.HeadKeyword() == "result" {
			f.TyUse.Results = append(f.TyUse.Results, p.parseResultDecl(elem)...)
		} else if elem.HeadKeyword() == "local" {
			f.Locals = append(f.Locals, p.parseLocalDecl(elem))
		} else {
			// Neither of these, so the instruction sequence started. Parse the
			// entire instruction sequence.
			f.Instrs = p.parseInstrs(sx, cursor)
			break
		}
	}

	return f
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

func (p *Parser) parseLocalDecl(sx *SExpr) *LocalDecl {
	ld := &LocalDecl{loc: sx.loc}

	if len(sx.list) == 3 {
		ld.Id = p.matchElement(sx, 1, ID)
		ld.Ty = p.parseType(sx.list[2])
	} else if len(sx.list) == 2 {
		ld.Ty = p.parseType(sx.list[1])
	} else {
		p.emitError(sx.loc, "invalid '(local' declaration")
		return nil
	}
	return ld
}

func (p *Parser) parseType(sx *SExpr) Type {
	if sx.IsToken() && sx.tok.name == KEYWORD {
		name := sx.tok.value
		if _, ok := basicTypes[name]; ok {
			return &BasicType{Name: name}
		}
	}

	p.emitError(sx.loc, "invalid type")
	return nil
}

// parseInstrs parses a list of instructions from sx, starting at [idx]. It
// expects all tokens from [idx] until the end of sx to represent instructions,
// and will emit errors otherwise.
func (p *Parser) parseInstrs(sx *SExpr, idx int) []Instruction {
	var out []Instruction

	for cursor := idx; cursor < len(sx.list); {
		elem := sx.list[cursor]
		if elem.IsList() {
			out = append(out, p.parseFoldedInstr(elem)...)
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

// parseFoldedInstr parses one folded instruction expression and linearizes it
// into plain instructions in execution order.
//
// It expects sx to be a list of the form "(op arg...)", where each arg is
// either a nested folded instruction list or a plain operand token.
//
// Example:
//
//	(i32.add (local.get $x) (local.get $y))
//
// is lowered to:
//
//	local.get $x
//	local.get $y
//	i32.add
func (p *Parser) parseFoldedInstr(sx *SExpr) []Instruction {
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
	var out []Instruction
	var operands []Operand

	for i := 1; i < len(sx.list); i++ {
		elem := sx.list[i]
		if elem.IsList() {
			out = append(out, p.parseFoldedInstr(elem)...)
			continue
		}

		op := p.parseOperand(elem)
		if op == nil {
			p.emitError(elem.loc, fmt.Sprintf("invalid operand for %s", name))
			continue
		}
		operands = append(operands, op)
	}

	out = append(out, &PlainInstr{Name: name, Operands: operands, loc: head.loc})
	return out
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
