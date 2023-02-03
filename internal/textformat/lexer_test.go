package textformat

import (
	"testing"

	"github.com/eliben/watgo/internal/slices"
)

func tokenizeAll(input string) []token {
	var toks []token

	lex := newLexer(input)
	for {
		tok := lex.nextToken()
		if tok.name == EOF || tok.name == ERROR {
			break
		}
		toks = append(toks, tok)
	}
	return toks
}

func TestLexer(t *testing.T) {
	var tests = []struct {
		name       string
		input      string
		wantTokens []token
	}{
		{"basic keyword and id",
			`k$% $hi`,
			[]token{token{KEYWORD, "k$%", 1}, token{ID, "$hi", 1}}},

		{"decimal integers",
			`20 +441 -882 0123 1_000_000`,
			[]token{
				token{INT, "20", 1}, token{INT, "+441", 1},
				token{INT, "-882", 1}, token{INT, "0123", 1},
				token{INT, "1_000_000", 1},
			}},

		{"hex integers",
			`0xaBc -0x03f +0x1 0xfF_aB`,
			[]token{
				token{INT, "0xaBc", 1}, token{INT, "-0x03f", 1},
				token{INT, "+0x1", 1}, token{INT, "0xfF_aB", 1},
			}},

		{"decimal floats",
			`0.1 199.34 25.`,
			[]token{
				token{FLOAT, "0.1", 1}, token{FLOAT, "199.34", 1}, token{FLOAT, "25.", 1},
			}},

		{"skipping line comments",
			`kwa ;;comment
;; another comment
koi ;;;yet another comment`,
			[]token{token{KEYWORD, "kwa", 1}, token{KEYWORD, "koi", 3}}},

		{"block comment",
			`tok (;
		x
		y
		;) tok2`,
			[]token{token{KEYWORD, "tok", 1}, token{KEYWORD, "tok2", 4}}},

		{"nested block comment",
			`;; line comment
			aa (; outer block comment (; inner block comment
			;) more text
			;) bb`,
			[]token{token{KEYWORD, "aa", 2}, token{KEYWORD, "bb", 4}}},

		// TODO: test errors too
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTokens := tokenizeAll(tt.input)
			if !slices.Equal(gotTokens, tt.wantTokens) {
				t.Errorf("got tokens=%v, want=%v", gotTokens, tt.wantTokens)
			}
		})
	}
}
