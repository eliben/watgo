package textformat

import (
	"fmt"
	"unicode/utf8"
)

// tokenName is a type for describing tokens mnemonically.
type tokenName int

type token struct {
	name tokenName
	val  string
	line int
}

const (
	// Special tokens
	ERROR tokenName = iota
	EOF

	LPAREN
	RPAREN
	DOLLAR
	ID
	KEYWORD
	INT
	FLOAT
)

var tokenNames = [...]string{
	ERROR: "ERROR",
	EOF:   "EOF",

	LPAREN:  "LPAREN",
	RPAREN:  "RPAREN",
	DOLLAR:  "DOLLAR",
	ID:      "ID",
	KEYWORD: "KEYWORD",
	INT:     "INT",
	FLOAT:   "FLOAT",
}

func (tok token) String() string {
	return fmt.Sprintf("token{%s, '%s', %v}", tokenNames[tok.name], tok.val, tok.line)
}

// lexer
//
// Create a new lexer with newLexer and then call nextToken repeatedly to get
// tokens from the stream. The lexer will return a token with the name EOF when
// done.
type lexer struct {
	buf string

	// Current rune.
	r rune

	// Offest of the current rune in buf.
	rpos int

	// Offset of the next rune in buf.
	nextpos int

	lineNum int
}

func newLexer(buf string) *lexer {
	lex := lexer{
		buf:     buf,
		r:       -1,
		rpos:    0,
		nextpos: 0,
		lineNum: 1,
	}

	// Prime the lexer by calling .next
	lex.next()
	return &lex
}

// next advances the lexer's internal state to point to the next rune in the
// input.
func (lex *lexer) next() {
	if lex.nextpos < len(lex.buf) {
		lex.rpos = lex.nextpos
		r, w := rune(lex.buf[lex.nextpos]), 1

		if r >= utf8.RuneSelf {
			r, w = utf8.DecodeRuneInString(lex.buf[lex.nextpos:])
		}

		lex.nextpos += w
		lex.r = r
	} else {
		lex.rpos = len(lex.buf)
		lex.r = -1 // EOF
	}
}

func (lex *lexer) peekNext() rune {
	if lex.nextpos < len(lex.buf) {
		return rune(lex.buf[lex.nextpos])
	} else {
		return -1
	}
}

func (lex *lexer) nextToken() token {
	// Skip non-tokens like whitespace and check for EOF.
	if err := lex.skipNontokens(); err != nil {
		return lex.errorToken(err.Error())
	}

	if lex.r < 0 {
		return token{EOF, "", lex.lineNum}
	}

	if lex.r == '$' {
		return lex.scanId()
	} else if isLetter(lex.r) {
		return lex.scanKeyword()
	} else if isDigit(lex.r) || isSign(lex.r) {
		return lex.scanNumber()
	}

	return lex.errorToken(fmt.Sprintf("unknown token starting with %q", lex.r))
}

func (lex *lexer) errorToken(msg string) token {
	return token{ERROR, msg, lex.lineNum}
}

func (lex *lexer) skipNontokens() error {
	for {
		switch lex.r {
		case ' ', '\t', '\r':
			lex.next()
		case '\n':
			lex.lineNum++
			lex.next()
		case ';':
			if lex.peekNext() == ';' {
				lex.skipLineComment()
			}
		case '(':
			if lex.peekNext() == ';' {
				if err := lex.skipBlockComment(); err != nil {
					return err
				}
			}
		default:
			return nil
		}
	}
}

func (lex *lexer) skipLineComment() {
	for lex.r != '\n' && lex.r > 0 {
		lex.next()
	}
}

func (lex *lexer) skipBlockComment() error {
	startLine := lex.lineNum
	// lex.r now points at the opening '(' with a ';' following it, so we'll start
	// by skipping both.
	lex.next()
	lex.next()

	// skip until we find ";)"
	for lex.r > 0 {
		if lex.r == ';' && lex.peekNext() == ')' {
			lex.next()
			lex.next()
			return nil
		}

		// if we see another '(;', it's a nested comment - skip it recursively
		if lex.r == '(' && lex.peekNext() == ';' {
			lex.skipBlockComment()
		} else if lex.r == '\n' {
			lex.lineNum++
		}

		lex.next()
	}

	return fmt.Errorf("unterminated block comment starting at line %v", startLine)
}

func (lex *lexer) scanId() token {
	startpos := lex.rpos
	for isIdChar(lex.r) {
		lex.next()
	}
	return token{ID, lex.buf[startpos:lex.rpos], lex.lineNum}
}

func (lex *lexer) scanKeyword() token {
	startpos := lex.rpos
	for isIdChar(lex.r) {
		lex.next()
	}
	return token{KEYWORD, lex.buf[startpos:lex.rpos], lex.lineNum}
}

func (lex *lexer) scanNumber() token {
	// TODO: should also handle float constants,
	// including "nan", "inf" etc.
	startpos := lex.rpos
	if isSign(lex.r) {
		lex.next()
	}

	hex := false

	if lex.r == '0' && lex.peekNext() == 'x' {
		// lex.r is now pointing at the starting "0x"; consume both.
		hex = true
		lex.next()
		lex.next()
		for isHexDigit(lex.r) || lex.r == '_' {
			lex.next()
		}
	} else {
		// decimal number
		for isDigit(lex.r) || lex.r == '_' {
			lex.next()
		}
	}

	// Finished parsing a number; this could either be the end of it, or a float
	// if the next rune is a decimal dot.

	if lex.r == '.' {
		lex.next()

		// Either a fractional part maybe followed by +/- exponent, or directly
		// the latter without a fractional part.
		if !isSign(lex.r) {
			if hex {
				for isHexDigit(lex.r) || lex.r == '_' {
					lex.next()
				}
			} else {
				for isDigit(lex.r) || lex.r == '_' {
					lex.next()
				}
			}
		}

		if isSign(lex.r) {
			lex.next()
			for isDigit(lex.r) {
				lex.next()
			}
		}

		return token{FLOAT, lex.buf[startpos:lex.rpos], lex.lineNum}
	} else {
		if lex.rpos-startpos == 1 {
			return lex.errorToken("lonely sign")
		}
		return token{INT, lex.buf[startpos:lex.rpos], lex.lineNum}
	}
}

// isIdChar checks whether r is in the idchar group defined by the wasm
// spec at https://webassembly.github.io/spec/core/text/values.html
//
// Note: this can probably be sped up using a lookup table
func isIdChar(r rune) bool {
	if '0' <= r && r <= '9' || 'A' <= r && r <= 'Z' || 'a' <= r && r <= 'z' {
		return true
	}
	switch r {
	case '!', '#', '$', '%', '&', '`', '*', '+', '-', '.', '/':
		return true
	case ':', '<', '=', '>', '?', '@', '\\', '^', '_', '|', '~':
		return true
	default:
		return false
	}
}

func isLetter(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z'
}

func isDigit(r rune) bool {
	return '0' <= r && r <= '9'
}

func isHexDigit(r rune) bool {
	return '0' <= r && r <= '9' || 'A' <= r && r <= 'F' || 'a' <= r && r <= 'f'
}

func isSign(r rune) bool {
	return r == '+' || r == '-'
}
