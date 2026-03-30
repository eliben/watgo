// Package valhint carries optional validator-side metadata for lowered WAT.
//
// These hints exist only to preserve a small amount of source-shape
// information that lowering intentionally erases when it flattens folded WAT
// syntax into linear [wasmir.Instruction] bodies. The validator uses the hints
// to distinguish ordinary missing operands from operands that were supplied by
// explicit folded child instructions which are statically polymorphic bottom,
// such as `unreachable`.
//
// The hints are advisory and internal-only:
//   - text lowering may return them alongside the lowered module
//   - binary decoding does not produce them
//   - validation must tolerate missing hints and treat them as all-zero
package valhint

// ModuleHints holds optional validator hints aligned to a lowered module's
// defined functions.
//
// Funcs is indexed the same way as [wasmir.Module.Funcs]. Missing entries are
// treated as zero-valued hints.
type ModuleHints struct {
	Funcs []FuncHints
}

// FuncHints holds optional validator hints aligned to one lowered function
// body.
//
// Instrs is indexed the same way as [wasmir.Function.Body]. Missing entries are
// treated as zero-valued hints.
type FuncHints struct {
	Instrs []InstrHints
}

// InstrHints carries the folded-source facts the validator needs for
// stack-polymorphic unreachable code.
type InstrHints struct {
	// ExplicitInstrArgs counts folded child instruction arguments in the source
	// WAT for the lowered instruction at the same body index.
	ExplicitInstrArgs uint8

	// BottomInstrArgs is the subset of ExplicitInstrArgs that were statically
	// polymorphic-bottom child instructions, such as `unreachable`.
	BottomInstrArgs uint8
}
