package watgo

import (
	"github.com/eliben/watgo/internal/binaryformat"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/wasmir"
)

// CompileWAT compiles text-format WebAssembly to binary WebAssembly.
func CompileWAT(src []byte) ([]byte, error) {
	tm, err := textformat.ParseModule(string(src))
	if err != nil {
		return nil, err
	}

	m, err := textformat.LowerModule(tm)
	if err != nil {
		return nil, err
	}

	if err := wasmir.ValidateModule(m); err != nil {
		return nil, err
	}

	return binaryformat.EncodeModule(m)
}
