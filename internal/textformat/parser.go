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
	if len(p.errs) > 0 {
		return m, p.errs
	} else {
		return m, nil
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
		tokMsg = fmt.Sprintf("token %q", tok.value)
	}
	p.errs.Add(fmt.Errorf("at %s: %v: %s", tok.loc, tokMsg, msg))
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

	var modName string
	if p.tok.name == ID {
		modName = p.tok.value
		p.advance()
	}

	module := &ast.Module{Name: modName}

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

	t := p.advance()
	if t.name != KEYWORD {
		p.emitError(t, "expecting keyword")
		return
	}

	//switch t.value {
	//case "func":
	//f := p.parseFunc()
	//default:
	//p.emitError(t, "unexpected keyword")
	//p.synchronize()
	//}
}

// func ::= '(' 'func' id? typeuse local* instr* ')
func (p *parser) parseFunc() *ast.Function {
	f := &ast.Function{}
	_ = f
	if p.tok.name == ID {
		//f.Id = p.tok.name
		p.advance()
	}

	// here parse type or naked param/result/local, or instructions
	if p.tok.name == LPAREN {

	}
	return nil
}
