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

func newParser(tokens []token) *parser {
	return &parser{
		tokens:  tokens,
		current: 0,
		errs:    nil,
	}
}

func (p *parser) parse() (module *ast.Module, err error) {
	return nil, nil
}

// isAtEnd reports whether we're at the end of the input.
func (p *parser) isAtEnd() bool {
	return p.current >= len(p.tokens) || p.tokens[p.current].name == EOF
}

// ErrorList represents multiple parse errors reported by the parser on a given
// source. It's loosely modeled on scanner.ErrorList in the Go standard library.
// ErrorList implements the error interface.
type errorList []error

func (el *errorList) Add(err error) {
	*el = append(*el, err)
}

func (el errorList) Error() string {
	if len(el) == 0 {
		return "no errors"
	} else if len(el) == 1 {
		return el[0].Error()
	} else {
		return fmt.Sprintf("%s (and %d more errors)", el[0], len(el)-1)
	}
}
