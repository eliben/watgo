package textformat

import "fmt"

type Parser struct {
	errs errorList
}

func ParseModule(buf string) (*Module, error) {
	lex := newLexer(buf)

	sx, err := sexprifyTop(lex)
	if err != nil {
		return nil, err
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
	p.errs.Add(fmt.Errorf("%s: %s", loc, msg))
}

// matchToken matches element [idx] of sx to the given tokname. If successful,
// it returns the actual token value at [idx]; otherwise it emits an error and
// returns "".
func (p *Parser) matchElement(sx *sexpr, idx int, tokname tokenName) string {
	if sx.IsToken() {
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

func (p *Parser) parseModule(sx *sexpr) *Module {
	if sx.HeadKeyword() != "module" {
		p.emitError(sx.loc, "expected 'module'")
		return nil
	}

	m := &Module{loc: sx.loc}
	cursor := 1
	if len(sx.list) > 1 && sx.list[1].tok.name == ID {
		m.Name = sx.list[1].tok.value
		m.loc = sx.list[1].tok.loc
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

func (p *Parser) parseFunction(sx *sexpr) *Function {
	f := &Function{
		TyUse: &TypeUse{},
		loc:   sx.loc,
	}

	// Optional function name: an identifier
	cursor := 1
	if sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
		f.Id = sx.list[cursor].tok.value
		cursor++
	}

	if sx.list[cursor].HeadKeyword() == "export" {
		f.Export = p.matchElement(sx.list[cursor], 1, STRING)
		cursor++
	}

	for i := cursor; i < len(sx.list); i++ {
		if sx.list[cursor].HeadKeyword() == "param" {

		}
	}

	return f
}

func (p *Parser) parseParamDecl(sx *sexpr) *ParamDecl {
	pd := &ParamDecl{loc: sx.loc}

	if len(sx.list) == 3 {
		// id and type

	} else if len(sx.list) == 2 {
		// just type

	} else {
		// TODO: error
	}

	return pd
}
