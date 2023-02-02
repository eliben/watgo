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
		if tok.name == EOF {
			break
		}
		toks = append(toks, tok)
	}
	return toks
}

func TestLexer(t *testing.T) {
	var tests = []struct {
		input      string
		wantTokens []token
	}{
		{`k$% $hi`, []token{token{KEYWORD, "k$%", 1}, token{ID, "$hi", 1}}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			gotTokens := tokenizeAll(tt.input)
			if !slices.Equal(gotTokens, tt.wantTokens) {
				t.Errorf("got tokens=%v, want=%v", gotTokens, tt.wantTokens)
			}
		})
	}
}
