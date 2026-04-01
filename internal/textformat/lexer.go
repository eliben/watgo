package textformat

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// tokenName is a type for describing tokens mnemonically.
type tokenName int

type token struct {
	name  tokenName
	value string
	loc   location
}

type location struct {
	line   int
	column int
}

func (loc location) String() string {
	// The zero-value location means "unknown / unavailable". Rendering it as
	// empty avoids noisy "0:0" prefixes in diagnostics.
	if loc.line == 0 {
		return ""
	}
	return fmt.Sprintf("%v:%v", loc.line, loc.column)
}

const (
	// Special tokens

	// EMPTY means "no token", it's the zero value of a token{}
	EMPTY tokenName = iota
	ERROR
	EOF

	LPAREN
	RPAREN
	DOLLAR
	ID
	KEYWORD
	INT
	FLOAT
	STRING
)

var tokenNames = [...]string{
	EMPTY: "EMPTY",
	ERROR: "ERROR",
	EOF:   "EOF",

	LPAREN:  "LPAREN",
	RPAREN:  "RPAREN",
	DOLLAR:  "DOLLAR",
	ID:      "ID",
	KEYWORD: "KEYWORD",
	INT:     "INT",
	FLOAT:   "FLOAT",
	STRING:  "STRING",
}

func (tn tokenName) String() string {
	return tokenNames[tn]
}

func (tok token) String() string {
	return fmt.Sprintf("token{%s, '%s', %s}", tok.name, tok.value, tok.loc)
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

	// location of r
	loc location
}

func newLexer(buf string) *lexer {
	lex := lexer{
		buf:     buf,
		r:       -1,
		rpos:    0,
		nextpos: 0,

		// column starts at 0 since next() always increments it before we have
		// the first rune in r.
		loc: location{1, 0},
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
		lex.loc.column += 1
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
		return lex.errorToken(err.Error(), lex.loc)
	}
	rloc := lex.loc

	if lex.r < 0 {
		return token{EOF, "<end of input>", rloc}
	}

	if lex.r == '$' {
		return lex.scanId()
	} else if isDigit(lex.r) || isSign(lex.r) {
		return lex.scanNumber()
	} else if isIdChar(lex.r) {
		return lex.scanKeyword()
	} else if lex.r == '(' {
		lex.next()
		return token{LPAREN, "(", rloc}
	} else if lex.r == ')' {
		lex.next()
		return token{RPAREN, ")", rloc}
	} else if lex.r == '"' {
		return lex.scanString()
	}

	errtok := lex.errorToken(fmt.Sprintf("unknown token starting with %q", lex.r), rloc)
	lex.next()
	return errtok
}

func (lex *lexer) errorToken(msg string, loc location) token {
	return token{ERROR, msg, loc}
}

func (lex *lexer) skipNontokens() error {
	for {
		switch lex.r {
		case ' ', '\t', '\r':
			lex.next()
		case '\n':
			lex.loc.line++
			lex.loc.column = 0
			lex.next()
		case ';':
			if lex.peekNext() == ';' {
				lex.skipLineComment()
			} else {
				return nil
			}
		case '(':
			if lex.peekNext() == ';' {
				if err := lex.skipBlockComment(); err != nil {
					return err
				}
			} else {
				return nil
			}
		default:
			return nil
		}
	}
}

func (lex *lexer) skipLineComment() {
	for lex.r != '\n' && lex.r != '\r' && lex.r >= 0 {
		lex.next()
	}
}

func (lex *lexer) skipBlockComment() error {
	startloc := lex.loc
	// lex.r now points at the opening '(' with a ';' following it, so we'll start
	// by skipping both.
	lex.next()
	lex.next()

	// skip until we find ";)"
	for lex.r >= 0 {
		if lex.r == ';' && lex.peekNext() == ')' {
			lex.next()
			lex.next()
			return nil
		}

		// if we see another '(;', it's a nested comment - skip it recursively
		if lex.r == '(' && lex.peekNext() == ';' {
			lex.skipBlockComment()
		} else if lex.r == '\n' {
			lex.loc.line++
			lex.loc.column = 0
		}

		lex.next()
	}

	return fmt.Errorf("unterminated block comment starting at %v", startloc)
}

func (lex *lexer) scanId() token {
	startpos := lex.rpos
	startloc := lex.loc
	for isIdChar(lex.r) {
		lex.next()
	}
	return token{ID, lex.buf[startpos:lex.rpos], startloc}
}

func (lex *lexer) scanKeyword() token {
	startloc := lex.loc
	startpos := lex.rpos
	for isIdChar(lex.r) {
		lex.next()
	}

	word := lex.buf[startpos:lex.rpos]
	if word == "inf" || word == "nan" || strings.HasPrefix(word, "nan:0x") {
		return token{FLOAT, word, startloc}
	} else {
		return token{KEYWORD, word, startloc}
	}
}

func (lex *lexer) scanString() token {
	startpos := lex.rpos
	startloc := lex.loc

	lex.next()

	for lex.r > 0 && lex.r != '"' {
		if lex.r == '\\' {
			lex.next()
		} else if lex.r == '\n' {
			lex.loc.line++
			lex.loc.column = 0
		}
		lex.next()
	}

	if lex.r < 0 {
		return lex.errorToken(fmt.Sprintf("unterminated string starting at %v", startloc), lex.loc)
	} else {
		closeQuotePos := lex.rpos
		lex.next()
		return token{STRING, lex.buf[startpos+1 : closeQuotePos], startloc}
	}
}

func (lex *lexer) scanNumber() token {
	startpos := lex.rpos
	startloc := lex.loc
	hadSign := false
	if isSign(lex.r) {
		hadSign = true
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
	} else if lex.r == 'i' || lex.r == 'n' {
		// 'inf' and 'nan' are valid floating-point numbers
		tok := lex.scanKeyword()
		if tok.name == FLOAT {
			tok.value = string(lex.buf[startpos]) + tok.value
			tok.loc = startloc
			return tok
		} else {
			return lex.errorToken("invalid word after + or -", startloc)
		}
	} else {
		// decimal number
		for isDigit(lex.r) || lex.r == '_' {
			lex.next()
		}
	}

	// Finished parsing a number; this could either be the end of it, or a float.
	seenDot := false
	if lex.r == '.' {
		seenDot = true
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

	}

	// The exponent is preceded by [e|E] for decimal floats and [p|P] for hex
	// floats, and can appear with or without a fractional dot.
	if (hex && (lex.r == 'p' || lex.r == 'P')) || (!hex && (lex.r == 'e' || lex.r == 'E')) {
		lex.next()
		if isSign(lex.r) {
			lex.next()
		}
		for isDigit(lex.r) || lex.r == '_' {
			lex.next()
		}
		return token{FLOAT, lex.buf[startpos:lex.rpos], startloc}
	}

	if seenDot {
		return token{FLOAT, lex.buf[startpos:lex.rpos], startloc}
	}
	if hadSign && lex.rpos-startpos == 1 {
		return lex.errorToken("lonely sign", startloc)
	}
	return token{INT, lex.buf[startpos:lex.rpos], startloc}
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
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '/':
		return true
	case ':', '<', '=', '>', '?', '@', '\\', '^', '_', '`', '|', '~':
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
