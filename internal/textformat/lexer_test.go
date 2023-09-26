package textformat

import (
	"fmt"
	"slices"
	"strings"
	"testing"
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
	}
	return toks
}

func displaySliceDiff[T any](got []T, want []T) string {
	maxLen := 0
	for _, g := range got {
		gs := fmt.Sprintf("%v", g)
		maxLen = max(maxLen+1, len(gs))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%-*v      %v\n", maxLen, "got", "want")

	for i := 0; i < max(len(got), len(want)); i++ {
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
			[]token{{KEYWORD, "k$%", location{1, 1}}, {ID, "$hi", location{1, 5}}}},

		{"parens",
			`() ( ( ) ) (hello)`,
			[]token{
				{LPAREN, "(", location{1, 1}}, {RPAREN, ")", location{1, 2}},
				{LPAREN, "(", location{1, 4}}, {LPAREN, "(", location{1, 6}},
				{RPAREN, ")", location{1, 8}}, {RPAREN, ")", location{1, 10}},
				{LPAREN, "(", location{1, 12}}, {KEYWORD, "hello", location{1, 13}}, {RPAREN, ")", location{1, 18}},
			}},

		{"decimal integers",
			`20 +441 -882 0123 1_000_000`,
			[]token{
				{INT, "20", location{1, 1}}, {INT, "+441", location{1, 4}},
				{INT, "-882", location{1, 9}}, {INT, "0123", location{1, 14}},
				{INT, "1_000_000", location{1, 19}},
			}},

		{"hex integers",
			`0xaBc -0x03f +0x1 0xfF_aB`,
			[]token{
				{INT, "0xaBc", location{1, 1}}, {INT, "-0x03f", location{1, 7}},
				{INT, "+0x1", location{1, 14}}, {INT, "0xfF_aB", location{1, 19}},
			}},

		{"decimal floats",
			`0.1 199.34 25.
		+2.12 -17. +2_4.5_6
		4.4e4 2.e-9 0.e+8 2.99e+111  100.008e-012`,
			[]token{
				{FLOAT, "0.1", location{1, 1}}, {FLOAT, "199.34", location{1, 5}}, {FLOAT, "25.", location{1, 12}},
				{FLOAT, "+2.12", location{2, 3}}, {FLOAT, "-17.", location{2, 9}}, {FLOAT, "+2_4.5_6", location{2, 14}},
				{FLOAT, "4.4e4", location{3, 3}}, {FLOAT, "2.e-9", location{3, 9}}, {FLOAT, "0.e+8", location{3, 15}}, {FLOAT, "2.99e+111", location{3, 21}}, {FLOAT, "100.008e-012", location{3, 32}},
			}},

		{"hex floats",
			`0xfa.3fe 0x13.
		-0xD1.p+21 +0x01EEF.20FEEP-100
		`,
			[]token{
				{FLOAT, "0xfa.3fe", location{1, 1}}, {FLOAT, "0x13.", location{1, 10}},
				{FLOAT, "-0xD1.p+21", location{2, 3}}, {FLOAT, "+0x01EEF.20FEEP-100", location{2, 14}},
			}},

		{"inf/nan floats",
			`+inf -inf +nan -nan
		inf nan
		nan:0xf0f0 -nan:0x12 +nan:0x4FFA`,
			[]token{
				{FLOAT, "+inf", location{1, 1}}, {FLOAT, "-inf", location{1, 6}}, {FLOAT, "+nan", location{1, 11}}, {FLOAT, "-nan", location{1, 16}},
				{FLOAT, "inf", location{2, 3}}, {FLOAT, "nan", location{2, 7}},
				{FLOAT, "nan:0xf0f0", location{3, 3}}, {FLOAT, "-nan:0x12", location{3, 14}}, {FLOAT, "+nan:0x4FFA", location{3, 24}},
			}},

		{"skipping line comments",
			`kwa ;;comment
		;; another comment
		koi ;;;yet another comment`,
			[]token{{KEYWORD, "kwa", location{1, 1}}, {KEYWORD, "koi", location{3, 3}}}},

		{"block comment",
			`tok (;
		x
		y
		;) tok2`,
			[]token{{KEYWORD, "tok", location{1, 1}}, {KEYWORD, "tok2", location{4, 6}}}},

		{"nested block comment",
			`;; line comment
		aa (; outer block comment (; inner block comment
		;) more text
		;) bb`,
			[]token{{KEYWORD, "aa", location{2, 3}}, {KEYWORD, "bb", location{4, 6}}}},

		{"strings",
			`hi "name"
		"str1"  "str2"
		"str3""str4"
		"escape \" still \\\" going \\" id
		`,
			[]token{
				{KEYWORD, "hi", location{1, 1}}, {STRING, `name`, location{1, 4}},
				{STRING, `"str1"`, location{2, 3}}, {STRING, `str2`, location{2, 11}},
				{STRING, `"str3"`, location{3, 3}}, {STRING, `str4`, location{3, 9}},
				{STRING, `escape \" still \\\" going \\`, location{4, 3}}, {KEYWORD, "id", location{4, 35}},
			}},

		{"string with newline",
			`id "string starting
		and ending" id2`,
			[]token{
				{KEYWORD, "id", location{1, 1}},
				{STRING, `string starting
		and ending`, location{1, 4}},
				{KEYWORD, "id2", location{2, 15}},
			}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTokens := tokenizeAll(tt.input)
			if !slices.Equal(gotTokens, tt.wantTokens) {
				t.Errorf("mismatch between got and want:\n%v", displaySliceDiff(gotTokens, tt.wantTokens))
			}
		})
	}
}

func TestLexerErrors(t *testing.T) {
	var tests = []struct {
		input         string
		errorIndex    int
		errorValue    string
		errorLocation location
	}{
		{"{", 0, "unknown token", location{1, 1}},
		{`"hello`, 0, "unterminated string starting at 1:1", location{1, 6}},
		{`+nunu`, 0, "invalid word after", location{1, 1}},
		{`+ kk`, 0, "lonely sign", location{1, 1}},
		{`id (;`, 1, "unterminated block comment", location{1, 5}},
		{`hello
tok +isdf tok`, 2, "invalid word after", location{2, 5}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotTokens := tokenizeAll(tt.input)
			gotErrTok := gotTokens[tt.errorIndex]
			if gotErrTok.name != ERROR || !strings.Contains(gotErrTok.value, tt.errorValue) || gotErrTok.loc != tt.errorLocation {
				t.Errorf("got error %v (loc %v), want %v (loc %v)", gotErrTok.value, gotErrTok.loc, tt.errorValue, tt.errorLocation)
			}
		})
	}
}
