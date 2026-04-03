package tests

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"text/template"

	"github.com/eliben/watgo/wasmir"
)

// helperDecodeMode tells the harness how to interpret the values returned from
// Node after a helper-assisted invoke completes.
type helperDecodeMode uint8

const (
	helperDecodeNormal helperDecodeMode = iota
	helperDecodeV128Words
	helperDecodeRefNullFlag
)

// helperMode carries small helper-specific protocol branches understood by the
// Node bridge after it instantiates a Go-supplied helper module.
type helperMode string

const (
	helperModeAnyrefClassify helperMode = "anyref_classify"
)

type nodeInvokeHelper struct {
	Key         string
	Wasm        []byte
	FuncName    string
	Args        []nodeValue
	ResultTypes []string
	Mode        helperMode
}

// invokePlan is the fully resolved invocation strategy for one action. Most
// invokes go directly through JS, but some use a helper module to preserve
// exact wasm semantics across the JS embedding boundary.
type invokePlan struct {
	Args        []nodeValue
	ResultTypes []string
	Helper      *nodeInvokeHelper
	DecodeMode  helperDecodeMode
}

// helperTemplateData is the small view-model fed into the WAT templates below.
// It keeps the templates declarative and pushes the lowering decisions into Go.
type helperTemplateData struct {
	TargetParams       []string
	TargetResults      []string
	ExportParams       []string
	ExportResults      []string
	NeedsScratchMemory bool
	PreludeLines       []string
	PostCallLines      []string
}

// generalHelperTemplate covers the common helper shape:
//   - import the target export as "m"."f"
//   - optionally rebuild lowered v128 params in a scratch memory
//   - call the target
//   - optionally post-process the result before exporting it
var generalHelperTemplate = template.Must(template.New("general").Parse(`
(module
  (import "m" "f" (func $target{{if .TargetParams}} (param{{range .TargetParams}} {{.}}{{end}}){{end}}{{if .TargetResults}} (result{{range .TargetResults}} {{.}}{{end}}){{end}}))
{{if .NeedsScratchMemory}}  (memory 1)
{{end}}  (func (export "call"){{if .ExportParams}} (param{{range .ExportParams}} {{.}}{{end}}){{end}}{{if .ExportResults}} (result{{range .ExportResults}} {{.}}{{end}}){{end}}
{{range .PreludeLines}}    {{.}}
{{end}}    call $target
{{range .PostCallLines}}    {{.}}
{{end}}  )
)
`))

// anyrefHelperTemplate classifies a single anyref result inside wasm. That is
// more precise than direct JS inspection for i31/struct/array values.
//
// The exported "classify" function returns:
//
//	0 = null
//	1 = i31
//	2 = struct
//	3 = array
//	4 = other anyref
//
// Code 4 falls back to "call_raw" on the Node side so host references can keep
// their synthetic identity token instead of being collapsed to a generic eqref.
var anyrefHelperTemplate = template.Must(template.New("anyref").Parse(`
(module
  (import "m" "f" (func $target{{if .TargetParams}} (param{{range .TargetParams}} {{.}}{{end}}){{end}} (result anyref)))
{{if .NeedsScratchMemory}}  (memory 1)
{{end}}  (func (export "call_raw"){{if .ExportParams}} (param{{range .ExportParams}} {{.}}{{end}}){{end}} (result anyref)
{{range .PreludeLines}}    {{.}}
{{end}}    call $target
  )
  (func (export "classify"){{if .ExportParams}} (param{{range .ExportParams}} {{.}}{{end}}){{end}} (result i32)
    (local $r anyref)
{{range .PreludeLines}}    {{.}}
{{end}}    call $target
    local.set $r
    local.get $r
    ref.is_null
    if (result i32)
      i32.const 0
    else
      (ref.test (ref i31) (local.get $r))
      if (result i32)
        i32.const 1
      else
        (ref.test (ref struct) (local.get $r))
        if (result i32)
          i32.const 2
        else
          (ref.test (ref array) (local.get $r))
          if (result i32)
            i32.const 3
          else
            i32.const 4
          end
        end
      end
    end
  )
)
`))

// planInvoke decides whether a call can go straight through JS or should be
// routed through a Go-compiled helper module first.
func (r *scriptRunner) planInvoke(sig wasmir.TypeDef, scriptArgs []scriptValue) (invokePlan, error) {
	args, err := encodeDirectInvokeArgs(scriptArgs, sig.Params)
	if err != nil {
		return invokePlan{}, err
	}
	resultTypes, err := valueTypeStrings(sig.Results)
	if err != nil {
		return invokePlan{}, err
	}
	helper, decodeMode, err := r.buildInvokeHelper(sig, scriptArgs)
	if err != nil {
		return invokePlan{}, err
	}
	return invokePlan{
		Args:        args,
		ResultTypes: resultTypes,
		Helper:      helper,
		DecodeMode:  decodeMode,
	}, nil
}

// buildInvokeHelper selects a helper strategy for the given function type.
// Helpers are used only where the JS embedding would otherwise lose exact wasm
// behavior, such as float NaN payloads, v128 values, or anyref classification.
func (r *scriptRunner) buildInvokeHelper(sig wasmir.TypeDef, scriptArgs []scriptValue) (*nodeInvokeHelper, helperDecodeMode, error) {
	hasV128Args := funcTypeHasV128Args(sig)
	hasFloatArgs := funcTypeHasFloatArgs(sig)
	needsExactTypeIdentity := funcTypeNeedsExactTypeIdentity(sig)

	switch {
	case len(sig.Results) == 1 && isAnyrefType(sig.Results[0]):
		if needsExactTypeIdentity {
			return nil, helperDecodeNormal, nil
		}
		return r.buildAnyrefHelper(sig, scriptArgs)
	case len(sig.Results) == 1 && isExnRefType(sig.Results[0]):
		if needsExactTypeIdentity {
			return nil, helperDecodeNormal, nil
		}
		return r.buildExnrefResultHelper(sig, scriptArgs)
	case len(sig.Results) == 1 && isFloatType(sig.Results[0]):
		if needsExactTypeIdentity {
			return nil, helperDecodeNormal, nil
		}
		return r.buildFloatResultHelper(sig, scriptArgs)
	case len(sig.Results) == 1 && sig.Results[0].Kind == wasmir.ValueKindV128:
		return r.buildV128ResultHelper(sig, scriptArgs)
	case hasV128Args || hasFloatArgs:
		return r.buildPassthroughHelper(sig, scriptArgs)
	default:
		return nil, helperDecodeNormal, nil
	}
}

// buildPassthroughHelper rebuilds lowered v128 arguments inside wasm and then
// forwards the call unchanged. This keeps JS from having to materialize v128.
func (r *scriptRunner) buildPassthroughHelper(sig wasmir.TypeDef, scriptArgs []scriptValue) (*nodeInvokeHelper, helperDecodeMode, error) {
	data, err := newGeneralHelperTemplateData(sig, sig.Results, nil)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	wasm, key, err := r.compileHelperTemplate("passthrough", generalHelperTemplate, data)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	args, err := encodeHelperInvokeArgs(scriptArgs, sig.Params)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	resultTypes, err := valueTypeStrings(sig.Results)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	return &nodeInvokeHelper{
		Key:         key,
		Wasm:        wasm,
		FuncName:    "call",
		Args:        args,
		ResultTypes: resultTypes,
	}, helperDecodeNormal, nil
}

// buildFloatResultHelper reinterprets one float result to raw integer bits
// inside wasm so the JS bridge cannot canonicalize NaNs or lose payload bits.
func (r *scriptRunner) buildFloatResultHelper(sig wasmir.TypeDef, scriptArgs []scriptValue) (*nodeInvokeHelper, helperDecodeMode, error) {
	var bitsResult wasmir.ValueType
	var post []string
	switch sig.Results[0].Kind {
	case wasmir.ValueKindF32:
		bitsResult = wasmir.ValueTypeI32
		post = []string{"i32.reinterpret_f32"}
	case wasmir.ValueKindF64:
		bitsResult = wasmir.ValueTypeI64
		post = []string{"i64.reinterpret_f64"}
	default:
		return nil, helperDecodeNormal, fmt.Errorf("unsupported float helper result %s", sig.Results[0])
	}
	data, err := newGeneralHelperTemplateData(sig, []wasmir.ValueType{bitsResult}, post)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	wasm, key, err := r.compileHelperTemplate("float_bits", generalHelperTemplate, data)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	args, err := encodeHelperInvokeArgs(scriptArgs, sig.Params)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	resultTypes, err := valueTypeStrings([]wasmir.ValueType{bitsResult})
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	return &nodeInvokeHelper{
		Key:         key,
		Wasm:        wasm,
		FuncName:    "call",
		Args:        args,
		ResultTypes: resultTypes,
	}, helperDecodeNormal, nil
}

// buildExnrefResultHelper converts one exnref-like result to a ref.is_null
// flag inside wasm so the JS embedding never has to materialize an exception
// reference value directly.
func (r *scriptRunner) buildExnrefResultHelper(sig wasmir.TypeDef, scriptArgs []scriptValue) (*nodeInvokeHelper, helperDecodeMode, error) {
	data, err := newGeneralHelperTemplateData(sig, []wasmir.ValueType{wasmir.ValueTypeI32}, []string{"ref.is_null"})
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	wasm, key, err := r.compileHelperTemplate("exnref_null_flag", generalHelperTemplate, data)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	args, err := encodeHelperInvokeArgs(scriptArgs, sig.Params)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	return &nodeInvokeHelper{
		Key:         key,
		Wasm:        wasm,
		FuncName:    "call",
		Args:        args,
		ResultTypes: []string{"i32"},
	}, helperDecodeRefNullFlag, nil
}

// buildV128ResultHelper stores one v128 result into scratch memory and reloads
// it as four i32 words that can cross the JS boundary exactly.
func (r *scriptRunner) buildV128ResultHelper(sig wasmir.TypeDef, scriptArgs []scriptValue) (*nodeInvokeHelper, helperDecodeMode, error) {
	post := []string{
		"v128.store",
		"i32.const 0",
		"i32.load",
		"i32.const 4",
		"i32.load",
		"i32.const 8",
		"i32.load",
		"i32.const 12",
		"i32.load",
	}
	data, err := newGeneralHelperTemplateData(sig, []wasmir.ValueType{
		wasmir.ValueTypeI32, wasmir.ValueTypeI32, wasmir.ValueTypeI32, wasmir.ValueTypeI32,
	}, post)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	data.NeedsScratchMemory = true
	data.PreludeLines = append([]string{"i32.const 0"}, data.PreludeLines...)
	wasm, key, err := r.compileHelperTemplate("v128_words", generalHelperTemplate, data)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	args, err := encodeHelperInvokeArgs(scriptArgs, sig.Params)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	return &nodeInvokeHelper{
		Key:      key,
		Wasm:     wasm,
		FuncName: "call",
		Args:     args,
		ResultTypes: []string{
			"i32", "i32", "i32", "i32",
		},
	}, helperDecodeV128Words, nil
}

// buildAnyrefHelper runs ref.test-based classification inside wasm. For the
// generic "other anyref" case the Node side falls back to direct encoding.
func (r *scriptRunner) buildAnyrefHelper(sig wasmir.TypeDef, scriptArgs []scriptValue) (*nodeInvokeHelper, helperDecodeMode, error) {
	data, err := newAnyrefHelperTemplateData(sig)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	wasm, key, err := r.compileHelperTemplate("anyref_classify", anyrefHelperTemplate, data)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	args, err := encodeHelperInvokeArgs(scriptArgs, sig.Params)
	if err != nil {
		return nil, helperDecodeNormal, err
	}
	return &nodeInvokeHelper{
		Key:         key,
		Wasm:        wasm,
		FuncName:    "classify",
		Args:        args,
		ResultTypes: []string{"i32"},
		Mode:        helperModeAnyrefClassify,
	}, helperDecodeNormal, nil
}

// newGeneralHelperTemplateData prepares template input for helpers that share
// the single-import, single-export wrapper shape.
func newGeneralHelperTemplateData(sig wasmir.TypeDef, exportResults []wasmir.ValueType, postCall []string) (helperTemplateData, error) {
	targetParams, err := valueTypeStrings(sig.Params)
	if err != nil {
		return helperTemplateData{}, err
	}
	targetResults, err := valueTypeStrings(sig.Results)
	if err != nil {
		return helperTemplateData{}, err
	}
	exportParams, prelude, needsMemory, err := loweredHelperParamsAndPrelude(sig.Params)
	if err != nil {
		return helperTemplateData{}, err
	}
	callResults, err := valueTypeStrings(exportResults)
	if err != nil {
		return helperTemplateData{}, err
	}
	return helperTemplateData{
		TargetParams:       targetParams,
		TargetResults:      targetResults,
		ExportParams:       exportParams,
		ExportResults:      callResults,
		NeedsScratchMemory: needsMemory,
		PreludeLines:       prelude,
		PostCallLines:      postCall,
	}, nil
}

// newAnyrefHelperTemplateData prepares template input for the anyref
// classifier helper, which exports both "classify" and "call_raw".
func newAnyrefHelperTemplateData(sig wasmir.TypeDef) (helperTemplateData, error) {
	targetParams, err := valueTypeStrings(sig.Params)
	if err != nil {
		return helperTemplateData{}, err
	}
	exportParams, prelude, needsMemory, err := loweredHelperParamsAndPrelude(sig.Params)
	if err != nil {
		return helperTemplateData{}, err
	}
	return helperTemplateData{
		TargetParams:       targetParams,
		TargetResults:      []string{"anyref"},
		ExportParams:       exportParams,
		NeedsScratchMemory: needsMemory,
		PreludeLines:       prelude,
	}, nil
}

// loweredHelperParamsAndPrelude converts helper-visible parameters into the WAT
// needed to reconstruct the original target arguments.
//
// Helpers lower float parameters to raw integer bit patterns so exact NaN
// payloads survive the JS embedding boundary, and they lower v128 parameters to
// four i32 words because JS cannot materialize SIMD values directly.
func loweredHelperParamsAndPrelude(params []wasmir.ValueType) ([]string, []string, bool, error) {
	var exportParams []string
	var prelude []string
	needsMemory := false
	helperIndex := 0
	scratchOffset := 0
	for _, vt := range params {
		switch vt.Kind {
		case wasmir.ValueKindF32:
			exportParams = append(exportParams, "i32")
			prelude = append(prelude,
				fmt.Sprintf("local.get %d", helperIndex),
				"f32.reinterpret_i32",
			)
			helperIndex++
			continue
		case wasmir.ValueKindF64:
			exportParams = append(exportParams, "i64")
			prelude = append(prelude,
				fmt.Sprintf("local.get %d", helperIndex),
				"f64.reinterpret_i64",
			)
			helperIndex++
			continue
		case wasmir.ValueKindV128:
			needsMemory = true
			exportParams = append(exportParams, "i32", "i32", "i32", "i32")
			for _, wordOffset := range []int{0, 4, 8, 12} {
				prelude = append(prelude,
					fmt.Sprintf("i32.const %d", scratchOffset+wordOffset),
					fmt.Sprintf("local.get %d", helperIndex),
					"i32.store",
				)
				helperIndex++
			}
			prelude = append(prelude,
				fmt.Sprintf("i32.const %d", scratchOffset),
				"v128.load",
			)
			scratchOffset += 16
			continue
		default:
			name, err := valueTypeString(vt)
			if err != nil {
				return nil, nil, false, err
			}
			exportParams = append(exportParams, name)
			prelude = append(prelude, fmt.Sprintf("local.get %d", helperIndex))
			helperIndex++
			continue
		}
	}
	return exportParams, prelude, needsMemory, nil
}

// valueTypeStrings maps harness value types to the textual names expected by
// both the helper WAT and the Node protocol.
func valueTypeStrings(types []wasmir.ValueType) ([]string, error) {
	if len(types) == 0 {
		return nil, nil
	}
	out := make([]string, len(types))
	for i, vt := range types {
		s, err := valueTypeString(vt)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func funcTypeHasV128Args(sig wasmir.TypeDef) bool {
	for _, vt := range sig.Params {
		if vt.Kind == wasmir.ValueKindV128 {
			return true
		}
	}
	return false
}

// funcTypeNeedsExactTypeIdentity reports function types that cannot safely be
// wrapped by these generic helpers. Subtyped/recursive function exports must be
// imported with their exact declared function type, not a flattened signature.
func funcTypeNeedsExactTypeIdentity(sig wasmir.TypeDef) bool {
	return sig.SubType || sig.RecGroupSize > 0 || len(sig.SuperTypes) > 0
}

func isFloatType(vt wasmir.ValueType) bool {
	return vt.Kind == wasmir.ValueKindF32 || vt.Kind == wasmir.ValueKindF64
}

func isAnyrefType(vt wasmir.ValueType) bool {
	return vt.Kind == wasmir.ValueKindRef && vt.HeapType.Kind == wasmir.HeapKindAny
}

func isExnRefType(vt wasmir.ValueType) bool {
	return vt.Kind == wasmir.ValueKindRef &&
		(vt.HeapType.Kind == wasmir.HeapKindExn || vt.HeapType.Kind == wasmir.HeapKindNoExn)
}

func funcTypeHasFloatArgs(sig wasmir.TypeDef) bool {
	for _, vt := range sig.Params {
		if isFloatType(vt) {
			return true
		}
	}
	return false
}

// encodeDirectInvokeArgs prepares the ordinary invoke path where the target
// export is called directly from JS.
func encodeDirectInvokeArgs(scriptArgs []scriptValue, params []wasmir.ValueType) ([]nodeValue, error) {
	args := make([]nodeValue, len(scriptArgs))
	for i, arg := range scriptArgs {
		encoded, err := encodeScriptArg(arg, params[i])
		if err != nil {
			return nil, fmt.Errorf("invoke arg[%d]: %w", i, err)
		}
		args[i] = encoded
	}
	return args, nil
}

// encodeHelperInvokeArgs prepares the helper-visible argument list. Float
// arguments are passed as raw integer bits so helpers can reinterpret them
// inside wasm without JS-number NaN canonicalization. v128 arguments are
// lowered to four i32 words because the helper rebuilds them in wasm before
// calling the target.
func encodeHelperInvokeArgs(scriptArgs []scriptValue, params []wasmir.ValueType) ([]nodeValue, error) {
	var out []nodeValue
	for i, arg := range scriptArgs {
		switch params[i].Kind {
		case wasmir.ValueKindF32:
			out = append(out, nodeValue{
				Type: "i32",
				Bits: strconv.FormatUint(uint64(helperF32ArgBits(arg)), 10),
			})
			continue
		case wasmir.ValueKindF64:
			out = append(out, nodeValue{
				Type: "i64",
				Bits: strconv.FormatUint(helperF64ArgBits(arg), 10),
			})
			continue
		case wasmir.ValueKindV128:
			if arg.kind != valueV128Const {
				return nil, fmt.Errorf("invoke arg[%d]: v128 helper lowering requires v128.const", i)
			}
			for word := 0; word < 4; word++ {
				bits := binary.LittleEndian.Uint32(arg.v128[word*4:])
				out = append(out, nodeValue{
					Type: "i32",
					Bits: strconv.FormatUint(uint64(bits), 10),
				})
			}
			continue
		default:
			encoded, err := encodeScriptArg(arg, params[i])
			if err != nil {
				return nil, fmt.Errorf("invoke arg[%d]: %w", i, err)
			}
			out = append(out, encoded)
			continue
		}
	}
	return out, nil
}

// helperF32ArgBits returns the exact f32 bit pattern a helper should receive
// for one script argument.
func helperF32ArgBits(arg scriptValue) uint32 {
	switch arg.kind {
	case valueF32Const:
		return uint32(arg.bits)
	case valueF32NaNCanonical:
		return 0x7fc00000
	case valueF32NaNArithmetic:
		return 0x7fc00001
	default:
		return uint32(arg.bits)
	}
}

// helperF64ArgBits returns the exact f64 bit pattern a helper should receive
// for one script argument.
func helperF64ArgBits(arg scriptValue) uint64 {
	switch arg.kind {
	case valueF64Const:
		return arg.bits
	case valueF64NaNCanonical:
		return 0x7ff8000000000000
	case valueF64NaNArithmetic:
		return 0x7ff8000000000001
	default:
		return arg.bits
	}
}

// compileHelperTemplate renders one helper WAT module, compiles it with watgo,
// and memoizes the resulting wasm bytes for reuse across script commands.
func (r *scriptRunner) compileHelperTemplate(kind string, tmpl *template.Template, data helperTemplateData) ([]byte, string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, "", err
	}
	watSrc := strings.TrimSpace(buf.String())
	key := helperTemplateCacheKey(kind, watSrc)
	if wasm, ok := r.helperWasm[key]; ok {
		return wasm, key, nil
	}
	wasm, err := r.compileWAT(watSrc)
	if err != nil {
		return nil, "", fmt.Errorf("%s helper compile failed: %w", kind, err)
	}
	r.helperWasm[key] = wasm
	return wasm, key, nil
}

// helperTemplateCacheKey is keyed by rendered WAT so helpers with identical
// behavior share one compiled wasm module.
func helperTemplateCacheKey(kind, watSrc string) string {
	return fmt.Sprintf("%s:%s", kind, watSrc)
}

// decodeInvokeResults interprets the raw Node response according to the helper
// strategy that produced it.
func decodeInvokeResults(values []nodeValue, mode helperDecodeMode) ([]runtimeValue, error) {
	switch mode {
	case helperDecodeNormal:
		out := make([]runtimeValue, len(values))
		for i, value := range values {
			decoded, err := decodeNodeValue(value)
			if err != nil {
				return nil, fmt.Errorf("result[%d]: %w", i, err)
			}
			out[i] = decoded
		}
		return out, nil
	case helperDecodeV128Words:
		if len(values) != 4 {
			return nil, fmt.Errorf("v128 helper returned %d values, want 4", len(values))
		}
		var lanes [16]byte
		for i, value := range values {
			if value.Type != "i32" || value.Bits == "" {
				return nil, fmt.Errorf("v128 helper result[%d] must be i32 word", i)
			}
			bits, err := strconv.ParseUint(value.Bits, 10, 32)
			if err != nil {
				return nil, fmt.Errorf("v128 helper result[%d]: %w", i, err)
			}
			// The helper returns the lanes as four little-endian machine words.
			binary.LittleEndian.PutUint32(lanes[i*4:], uint32(bits))
		}
		return []runtimeValue{{v128: lanes, isV128: true}}, nil
	case helperDecodeRefNullFlag:
		if len(values) != 1 || values[0].Type != "i32" || values[0].Bits == "" {
			return nil, fmt.Errorf("exnref helper returned malformed null flag")
		}
		bits, err := strconv.ParseUint(values[0].Bits, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("exnref helper null flag: %w", err)
		}
		if bits != 0 {
			return []runtimeValue{{}}, nil
		}
		return []runtimeValue{{scalar: encodedRefHostTag}}, nil
	default:
		return nil, fmt.Errorf("unsupported helper decode mode %d", mode)
	}
}
