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

// matchToken expects a list sx and matches element [idx] to the given tokname.
// If successful, it returns the actual token value at [idx]; otherwise it emits
// an error and returns "".
func (p *Parser) matchElement(sx *sexpr, idx int, tokname tokenName) string {
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

func (p *Parser) parseModule(sx *sexpr) *Module {
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

func (p *Parser) parseFunction(sx *sexpr) *Function {
	f := &Function{
		TyUse: &TypeUse{},
		loc:   sx.loc,
	}

	// Optional function name
	cursor := 1
	if sx.list[cursor].IsToken() && sx.list[cursor].tok.name == ID {
		f.Id = sx.list[cursor].tok.value
		cursor++
	}

	if sx.list[cursor].HeadKeyword() == "export" {
		f.Export = p.matchElement(sx.list[cursor], 1, STRING)
		cursor++
	}

	f.TyUse = &TypeUse{}

	for ; cursor < len(sx.list); cursor++ {
		elem := sx.list[cursor]
		if elem.HeadKeyword() == "param" {
			f.TyUse.Params = append(f.TyUse.Params, p.parseParamDecl(elem))
		} else if elem.HeadKeyword() == "result" {
			f.TyUse.Results = append(f.TyUse.Results, p.parseResultDecl(elem))
		}
		// TODO: parse locals first
		// TODO: here parse instructions

	}

	return f
}

func (p *Parser) parseParamDecl(sx *sexpr) *ParamDecl {
	pd := &ParamDecl{loc: sx.loc}

	if len(sx.list) == 3 {
		pd.Id = p.matchElement(sx, 1, ID)
		pd.Ty = p.parseType(sx.list[2])
	} else if len(sx.list) == 2 {
		pd.Ty = p.parseType(sx.list[1])
	} else {
		p.emitError(sx.loc, "invalid '(param' declaration")
		return nil
	}
	return pd
}

func (p *Parser) parseResultDecl(sx *sexpr) *ResultDecl {
	rd := &ResultDecl{loc: sx.loc}

	if len(sx.list) == 2 {
		rd.Ty = p.parseType(sx.list[1])
	} else {
		p.emitError(sx.loc, "invalid '(result' declaration")
		return nil
	}
	return rd
}

func (p *Parser) parseType(sx *sexpr) Type {
	if sx.IsToken() && sx.tok.name == KEYWORD {
		name := sx.tok.value
		if _, ok := basicTypes[name]; ok {
			return &BasicType{Name: name}
		}
	}

	p.emitError(sx.loc, "invalid type")
	return nil
}
