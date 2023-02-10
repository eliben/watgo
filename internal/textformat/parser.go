package textformat

import (
	"fmt"

	"github.com/eliben/watgo/internal/textformat/ast"
)

type parser struct {
	lex *lexer

	// Current token (one token lookahead)
	tok token

	errs errorList
}

func newParser(buf string) *parser {
	lex := newLexer(buf)

	p := &parser{
		lex:  lex,
		errs: nil,
	}

	p.advance()
	return p
}

func (p *parser) parse() (module *ast.Module, err error) {
	m := p.parseModule()

	if len(p.errs) == 0 {
		return m, nil
	} else {
		return nil, p.errs
	}
}

// advance returns the current token and consumes it (the next call to advance
// will return the next token in the stream, etc.)
func (p *parser) advance() token {
	tok := p.tok
	if tok.name != EOF {
		p.tok = p.lex.nextToken()
	}
	return tok
}

// synchronize consumes tokens until if finds a place in the input where it's
// probably safe to resume parsing. Specifically, it searches for the next
// non-nested closing ')', and keeps track of nesting to skip (...)
func (p *parser) synchronize() {
	nestingDepth := 1

	for {
		switch p.tok.name {
		case EOF:
			return
		case LPAREN:
			nestingDepth++
		case RPAREN:
			nestingDepth--
		default:
			// keep going
		}
		p.advance()

		if nestingDepth == 0 {
			return
		}
	}
}

func (p *parser) emitError(tok token, msg string) {
	var tokMsg string
	if tok.name == EOF {
		tokMsg = "end of input"
	} else {
		tokMsg = fmt.Sprintf("token %v", tok.value)
	}
	p.errs.Add(fmt.Errorf("line %d: %v: %s", tok.line, tokMsg, msg))
}

// module ::= '(' 'module' id? (modulefield)* ')'
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

	t := p.advance()
	var modName string

	if t.name == ID {
		modName = t.value
		t = p.advance()
	}

	module := &ast.Module{Name: modName}

	// TODO: parse modulefield here in a loop, until ')' is encountered, which
	// terminates the module.
	for p.tok.name != RPAREN {
		p.parseModuleField(module)
	}

	return module
}

// modulefield ::= '(' field-keyword ... ')'
// The contents of each field are parsed in field-specific methods.
func (p *parser) parseModuleField(module *ast.Module) {
	// If the next token is not an LPAREN, report an error and bail.
	if t := p.advance(); t.name != LPAREN {
		p.emitError(t, "expecting opening '(' of a modulefield")
		return
	}

	// if not keyword, sync to the ending ')' ?
}
