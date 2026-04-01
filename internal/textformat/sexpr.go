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

// WithoutAnnotations returns a deep copy of sx with valid annotation forms
// removed recursively from all lists.
//
// WebAssembly annotations behave like parser-directed comments. They can wrap
// top-level script commands or appear anywhere inside module syntax, so callers
// normalize them away before ordinary parsing. Malformed annotation spellings
// are intentionally preserved so later parsing still reports them as errors.
func (sx *SExpr) WithoutAnnotations() *SExpr {
	if sx == nil {
		return nil
	}
	if sx.IsToken() {
		return &SExpr{tok: sx.tok, loc: sx.loc}
	}

	out := &SExpr{loc: sx.loc, list: make([]*SExpr, 0, len(sx.list))}
	for _, sub := range sx.list {
		if isValidAnnotationSExpr(sub) {
			continue
		}
		out.list = append(out.list, sub.WithoutAnnotations())
	}
	return out
}

// isValidAnnotationSExpr reports whether sx is one valid annotation node that
// should be removed before ordinary parsing.
//
// The parser only strips forms it can identify unambiguously from s-expression
// structure:
//   - (@name ...)
//   - (@"name" ...)
//
// Malformed spellings such as "(@)", "(@ x)", or "(@ \"x\")" are not treated
// as annotations here, because spec tests expect them to remain parse errors.
func isValidAnnotationSExpr(sx *SExpr) bool {
	if sx == nil || !sx.IsList() || len(sx.list) == 0 {
		return false
	}

	head := sx.list[0]
	if !head.IsTokenKind(KEYWORD) {
		return false
	}
	if strings.HasPrefix(head.tok.value, "@") && head.tok.value != "@" {
		return true
	}
	if head.tok.value != "@" || len(sx.list) < 2 {
		return false
	}

	name := sx.list[1]
	if !name.IsTokenKind(STRING) || name.tok.value == "" {
		return false
	}

	// (@"name") is valid, but (@ "name") is malformed. By the time we are
	// looking at s-expressions, the only remaining way to distinguish them is
	// whether the string token started immediately after the '@' token.
	return name.loc.line == head.loc.line && name.loc.column == head.loc.column+1
}
