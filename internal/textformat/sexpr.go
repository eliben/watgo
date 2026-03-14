package textformat

import (
	"strings"
)

// SExpr represents textformat source as an s-expression with tokens.
// Each s-expression is either a single token (IsToken returns true) or a list
// of s-expressions (IsList returns true).
// An empty list () is represented as a list with zero children.
type SExpr struct {
	tok  token
	list []*SExpr
	loc  location
}

func (sx *SExpr) IsToken() bool {
	return sx.list == nil
}

func (sx *SExpr) IsList() bool {
	return sx.list != nil
}

// IsTokenKind reports whether sx is a token node with the given token kind.
func (sx *SExpr) IsTokenKind(kind tokenName) bool {
	return sx != nil && sx.IsToken() && sx.tok.name == kind
}

// IsTokenAny reports whether sx is a token node with any of the given kinds.
func (sx *SExpr) IsTokenAny(kinds ...tokenName) bool {
	if sx == nil || !sx.IsToken() {
		return false
	}
	for _, k := range kinds {
		if sx.tok.name == k {
			return true
		}
	}
	return false
}

// IsKeywordToken reports whether sx is a KEYWORD token with the given value.
func (sx *SExpr) IsKeywordToken(value string) bool {
	return sx != nil && sx.IsToken() && sx.tok.name == KEYWORD && sx.tok.value == value
}

// HeadKeyword returns the keyword value at the head of the SExpr; for SExprs
// of the form (head foo bar ...), where `head` is a KEYWORD token, this returns
// the value of the token. For other sexprs it returns ""
func (sx *SExpr) HeadKeyword() string {
	if !sx.IsList() || len(sx.list) == 0 {
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
	if sx.IsList() {
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
