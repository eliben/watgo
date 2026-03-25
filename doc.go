// Package watgo provides the public high-level Go API for parsing,
// validating, decoding, and encoding WebAssembly modules.
//
// The package is centered on semantic IR represented by [wasmir.Module].
// Text-format WAT and binary WASM both normalize into that IR:
//
//   - [ParseWAT] parses and lowers WAT into semantic IR.
//   - [DecodeWASM] decodes binary WASM into semantic IR.
//   - [ValidateModule] validates semantic IR.
//   - [EncodeWASM] encodes semantic IR back to binary WASM.
//   - [CompileWATToWASM] is the end-to-end convenience helper for WAT-to-WASM.
//
// A typical pipeline is:
//
//	m, err := watgo.ParseWAT(src)
//	if err != nil {
//		// handle parse/lower diagnostics
//	}
//	if err := watgo.ValidateModule(m); err != nil {
//		// handle validation diagnostics
//	}
//	wasm, err := watgo.EncodeWASM(m)
//	if err != nil {
//		// handle encoding diagnostics
//	}
//
// See the package examples for complete runnable usage snippets.
package watgo
