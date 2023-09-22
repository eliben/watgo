package textformat

import (
	"fmt"
	"strings"
)

// TODO: if empty list - an atom, otherwise it's a list
// empty list an tok.name == EMPTY ==> empty list
type sexpr struct {
	tok  token
	list []*sexpr
}

func (sx *sexpr) IsToken() bool {
	return len(sx.list) == 0
}

func (sx *sexpr) IsList() bool {
	return len(sx.list) > 0
}

func (sx *sexpr) String() string {
	if len(sx.list) > 0 {
		var parts []string
		for _, sub := range sx.list {
			parts = append(parts, sub.String())
		}
		return "( " + strings.Join(parts, " ") + " )"
	} else {
		return sx.tok.String()
	}
}

func sexprifyTop(lex *lexer) (*sexpr, error) {
	tok := lex.nextToken()
	if tok.name == LPAREN {
		return sexprify(lex)
	} else {
		return nil, fmt.Errorf("at %s: %v: expected '('", tok.loc, tok.value)
	}
}

// TODO: assumes the last token in lex was LPAREN
func sexprify(lex *lexer) (*sexpr, error) {
	sx := &sexpr{}

	for {
		tok := lex.nextToken()
		if tok.name == LPAREN {
			list, err := sexprify(lex)
			if err != nil {
				return nil, err
			}
			sx.list = append(sx.list, list)
		} else if tok.name == RPAREN {
			if len(sx.list) == 0 {
			}
			return sx, nil
		} else if tok.name == EOF {
			// TODO: find some way to pass the opening paren here, for better
			// reporting?
			return nil, fmt.Errorf("unterminated expression at end of file")
		} else {
			sx.list = append(sx.list, &sexpr{tok: tok})
		}
	}
}
