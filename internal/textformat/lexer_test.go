package textformat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/eliben/watgo/internal/utils"
)

func tokenizeAll(input string) []token {
	var toks []token

	lex := newLexer(input)
	for {
		tok := lex.nextToken()
		if tok.name == EOF {
			// on EOF, stop without adding it to toks
			break
		}
		toks = append(toks, tok)

		if tok.name == ERROR {
			// stop on first error (but after adding it to toks)
			break
		}
	}
	return toks
}

func displaySliceDiff[T any](got []T, want []T) string {
	maxLen := 0
	for _, g := range got {
		gs := fmt.Sprintf("%v", g)
		maxLen = utils.Max(maxLen+1, len(gs))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-*v      %v\n", maxLen, "got", "want")

	for i := 0; i < utils.Max(len(got), len(want)); i++ {
		var sgot string
		if i < len(got) {
			sgot = fmt.Sprintf("%v", got[i])
		}

		var swant string
		if i < len(want) {
			swant = fmt.Sprintf("%v", want[i])
		}

		sign := "  "
		if swant != sgot {
			sign = "!="
		}

		fmt.Fprintf(&sb, "%-*v  %v  %v\n", maxLen, sgot, sign, swant)
	}
	return sb.String()
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

		{"parens",
			`() ( ( ) ) (hello)`,
			[]token{
				token{LPAREN, "(", 1}, token{RPAREN, ")", 1},
				token{LPAREN, "(", 1}, token{LPAREN, "(", 1}, token{RPAREN, ")", 1}, token{RPAREN, ")", 1},
				token{LPAREN, "(", 1}, token{KEYWORD, "hello", 1}, token{RPAREN, ")", 1},
			}},

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
			`0.1 199.34 25.
			+2.12 -17. +2_4.5_6
			4.4e4 2.e-9 0.e+8 2.99e+111  100.008e-012`,
			[]token{
				token{FLOAT, "0.1", 1}, token{FLOAT, "199.34", 1}, token{FLOAT, "25.", 1},
				token{FLOAT, "+2.12", 2}, token{FLOAT, "-17.", 2}, token{FLOAT, "+2_4.5_6", 2},
				token{FLOAT, "4.4e4", 3}, token{FLOAT, "2.e-9", 3}, token{FLOAT, "0.e+8", 3}, token{FLOAT, "2.99e+111", 3}, token{FLOAT, "100.008e-012", 3},
			}},

		{"hex floats",
			`0xfa.3fe 0x13.
			-0xD1.p+21 +0x01EEF.20FEEP-100
			`,
			[]token{
				token{FLOAT, "0xfa.3fe", 1}, token{FLOAT, "0x13.", 1},
				token{FLOAT, "-0xD1.p+21", 2}, token{FLOAT, "+0x01EEF.20FEEP-100", 2},
			}},

		{"inf/nan floats",
			`+inf -inf +nan -nan
			inf nan
			nan:0xf0f0 -nan:0x12 +nan:0x4FFA`,
			[]token{
				token{FLOAT, "+inf", 1}, token{FLOAT, "-inf", 1}, token{FLOAT, "+nan", 1}, token{FLOAT, "-nan", 1},
				token{FLOAT, "inf", 2}, token{FLOAT, "nan", 2},
				token{FLOAT, "nan:0xf0f0", 3}, token{FLOAT, "-nan:0x12", 3}, token{FLOAT, "+nan:0x4FFA", 3},
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

		{"strings",
			`hi "name"
			"str1"  "str2"
			"str3""str4"
			"escape \" still \\\" going \\" id
			`,

			[]token{
				token{KEYWORD, "hi", 1}, token{STRING, `"name"`, 1},
				token{STRING, `"str1"`, 2}, token{STRING, `"str2"`, 2},
				token{STRING, `"str3"`, 3}, token{STRING, `"str4"`, 3},
				token{STRING, `"escape \" still \\\" going \\"`, 4}, token{KEYWORD, "id", 4},
			}},

		{"string with newline",
			`id "string starting
and ending" id2`,
			[]token{
				token{KEYWORD, "id", 1},
				token{STRING, `"string starting
and ending"`, 1},
				token{KEYWORD, "id2", 2},
			}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTokens := tokenizeAll(tt.input)
			if !utils.SlicesEqual(gotTokens, tt.wantTokens) {
				t.Errorf("mismatch between got and want:\n%v", displaySliceDiff(gotTokens, tt.wantTokens))
			}
		})
	}
}

func TestLexerErrors(t *testing.T) {
	var tests = []struct {
		input     string
		wantError string
	}{
		{"{", "unknown token"},
		{`"hello`, "unterminated string starting at line 1"},
		{`+nunu`, "invalid word after +"},
		{`+ kk`, "lonely sign"},
		{`id (;`, "unterminated block comment"},
	}

	for _, tt := range tests {
		t.Run(tt.wantError, func(t *testing.T) {
			gotTokens := tokenizeAll(tt.input)
			errTok := gotTokens[len(gotTokens)-1]
			if errTok.name != ERROR {
				t.Errorf("got last tok %s, want ERROR", errTok)
			}
			if strings.Index(errTok.value, tt.wantError) < 0 {
				t.Errorf("got error %q, wanted to find %q", errTok.value, tt.wantError)
			}
		})
	}
}
