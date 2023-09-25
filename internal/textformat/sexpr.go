package textformat

import (
	"fmt"
	"strings"
)

// sexpr represents textformat source as an s-expression with tokens.
// Each s-expression is either a single token (IsToken returns true) or a list
// of s-expressions (IsList returns true).
// For an empty list (), IsList will be false and IsToken will be true; the
// token name will be EMPTY.
type sexpr struct {
	tok  token
	list []*sexpr
	loc  location
}

func (sx *sexpr) IsToken() bool {
	return len(sx.list) == 0
}

func (sx *sexpr) IsList() bool {
	return len(sx.list) > 0
}

// HeadKeyword returns the keyword value at the head of the sexpr; for sexprs
// of the form (head foo bar ...), where `head` is a KEYWORD token, this returns
// the value of the token. For other sexprs it returns ""
func (sx *sexpr) HeadKeyword() string {
	if !sx.IsList() {
		return ""
	}
	head := sx.list[0]
	if head.tok.name == KEYWORD {
		return head.tok.value
	} else {
		return ""
	}
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

// sexprifyTop is the entry point to this code; it takes a freshly created
// lexer (from newLexer) and builds a sexpr representing the code. The lexer
// will be exhausted.
func sexprifyTop(lex *lexer) (*sexpr, error) {
	tok := lex.nextToken()
	if tok.name == LPAREN {
		return sexprify(lex, tok)
	} else {
		return nil, fmt.Errorf("at %s: %v: expected '('", tok.loc, tok.value)
	}
}

// sexprify is a helper for a single s-expression; it's called when '(' is
// encountered and consumed, and returns a new sexpr. lparen is the consumed
// '(' token.
func sexprify(lex *lexer, lparen token) (*sexpr, error) {
	sx := &sexpr{loc: lparen.loc}

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
			sx.list = append(sx.list, &sexpr{tok: tok, loc: tok.loc})
		}
	}
}
