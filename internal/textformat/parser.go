package textformat

import (
	"fmt"

	"github.com/eliben/watgo/internal/textformat/ast"
)

type parser struct {
	tokens  []token
	current int
	errs    errorList
}

// TODO: when encountering an error, register it in errs and keep going, trying
// to resync (maybe to the upcoming closing RPAREN?)
func newParser(tokens []token) *parser {
	return &parser{
		tokens:  tokens,
		current: 0,
		errs:    nil,
	}
}

func (p *parser) parse() (module *ast.Module, err error) {
	m := p.parseModule()

	if len(p.errs) == 0 {
		return m, nil
	} else {
		return nil, p.errs
	}
}

// isAtEnd reports whether we're at the end of the input.
func (p *parser) isAtEnd() bool {
	return p.current >= len(p.tokens) || p.tokens[p.current].name == EOF
}

// advance consumes the current token and returns it.
func (p *parser) advance() token {
	tok := p.tokens[p.current]
	if !p.isAtEnd() {
		p.current++
	}
	return tok
}

func (p *parser) match(name tokenName, errMsg string) token {
	tok := p.advance()
	if tok.name != name {
		// TODO: report error here
	}
	return tok
}

func (p *parser) emitError(tok token, msg string) {
	var tokMsg string
	if tok.name == EOF {
		tokMsg = "end of input"
	} else {
		tokMsg = fmt.Sprintf("token %v", tok.value)
	}
	self.errs.Add(fmt.Errorf("line %d: %v: %s", tok.line, tokMsg, msg))
}

// module ::= '(' 'module' id? (module-field)* ')'
func (p *parser) parseModule() *ast.Module {
	// If we can't even find a proper '(' 'module', just bail out immediately.
	if t := p.advance(); t.name != LPAREN {
		p.emitError(t, "expecting opening '(' of a module")
		return nil
	}

	if t := p.advance(); t.name != KEYWORD || t.value != "module" {
		p.emitError(t, "expecting 'module'")
		return nil
	}

	return nil
}
