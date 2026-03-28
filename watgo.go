package watgo

import (
	"github.com/eliben/watgo/internal/binaryformat"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/internal/validate"
	"github.com/eliben/watgo/wasmir"
)

// ParseWAT parses and lowers text-format WebAssembly into semantic IR.
//
// ParseWAT performs text parsing plus lowering into [wasmir.Module], but does
// not run semantic validation. Call [ValidateModule] on the returned module
// before encoding or executing it when you need a validated module.
//
// On failure, ParseWAT returns a non-nil error. Most parser and lowering
// failures are returned as diag.ErrorList values.
func ParseWAT(src []byte) (*wasmir.Module, error) {
	tm, err := textformat.ParseModule(string(src))
	if err != nil {
		return nil, err
	}

	return textformat.LowerModule(tm)
}

// DecodeWASM decodes binary WebAssembly into semantic IR.
//
// DecodeWASM performs binary decoding into [wasmir.Module], but does not run
// semantic validation. Call [ValidateModule] on the returned module before
// using it when you need a validated module.
//
// On failure, DecodeWASM returns a non-nil error. Decode errors are typically
// returned as diag.ErrorList values.
func DecodeWASM(src []byte) (*wasmir.Module, error) {
	return binaryformat.DecodeModule(src)
}

// ValidateModule validates a semantic WebAssembly module.
//
// This is the public validation entry point over [wasmir.Module]. It reports
// semantic issues such as type mismatches, invalid indices, and malformed
// instruction sequences. Validation errors are typically returned as
// diag.ErrorList values.
func ValidateModule(m *wasmir.Module) error {
	return validate.ValidateModule(m)
}

// EncodeWASM encodes semantic IR as binary WebAssembly.
//
// EncodeWASM does not implicitly validate m. Call [ValidateModule] first when
// you need validation before encoding.
//
// On failure, EncodeWASM returns a non-nil error. Encoder failures are
// typically returned as diag.ErrorList values.
func EncodeWASM(m *wasmir.Module) ([]byte, error) {
	return binaryformat.EncodeModule(m)
}

// CompileWATToWASM parses, lowers, validates, and encodes text-format
// WebAssembly to binary WebAssembly.
//
// This is the public end-to-end convenience API when the caller wants binary
// output directly from WAT input.
func CompileWATToWASM(src []byte) ([]byte, error) {
	m, err := ParseWAT(src)
	if err != nil {
		return nil, err
	}
	if err := ValidateModule(m); err != nil {
		return nil, err
	}
	return EncodeWASM(m)
}
