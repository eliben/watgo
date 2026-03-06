package textformat

import (
	"fmt"
	"strings"
)

// SExpr represents textformat source as an s-expression with tokens.
// Each s-expression is either a single token (IsToken returns true) or a list
// of s-expressions (IsList returns true).
// For an empty list (), IsList will be false and IsToken will be true; the
// token name will be EMPTY.
type SExpr struct {
	tok  token
	list []*SExpr
	loc  location
}

func (sx *SExpr) IsToken() bool {
	return len(sx.list) == 0
}

func (sx *SExpr) IsList() bool {
	return len(sx.list) > 0
}

// HeadKeyword returns the keyword value at the head of the SExpr; for SExprs
// of the form (head foo bar ...), where `head` is a KEYWORD token, this returns
// the value of the token. For other sexprs it returns ""
func (sx *SExpr) HeadKeyword() string {
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

func (sx *SExpr) String() string {
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

// Children returns sx's child expressions for list SExprs.
// For token SExprs, it returns nil.
func (sx *SExpr) Children() []*SExpr {
	return sx.list
}

// Token returns the token kind and value for token SExprs.
// For list SExprs, it returns ok=false.
func (sx *SExpr) Token() (kind string, value string, ok bool) {
	if !sx.IsToken() {
		return "", "", false
	}
	return sx.tok.name.String(), sx.tok.value, true
}

// Loc returns sx's source location as "line:column".
func (sx *SExpr) Loc() string {
	return sx.loc.String()
}

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
	sx := &SExpr{loc: lparen.loc}

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
