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
)

var tokenNames = [...]string{
	ERROR: "ERROR",
	EOF:   "EOF",

	LPAREN:  "LPAREN",
	RPAREN:  "RPAREN",
	DOLLAR:  "DOLLAR",
	ID:      "ID",
	KEYWORD: "KEYWORD",
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
	lex.skipNontokens()
	if lex.r < 0 {
		return token{EOF, "", lex.lineNum}
	}

	if lex.r == '$' {
		return lex.scanId()
	} else if isLetter(lex.r) {
		return lex.scanKeyword()
	}

	return token{ERROR, "", lex.lineNum}
}

func (lex *lexer) skipNontokens() {
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
		default:
			return
		}
	}
}

func (lex *lexer) skipLineComment() {
	for lex.r != '\n' && lex.r > 0 {
		lex.next()
	}
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

// isIdChar checks whether r is in the idchar group defined by the wasm
// spec at https://webassembly.github.io/spec/core/text/values.html
//
// Note: this can probably be sped up using a lookup table
func isIdChar(r rune) bool {
	if r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
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
