package binaryformat

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/wasmir"
)

type decodedFuncBody struct {
	locals []wasmir.ValueType
	body   []wasmir.Instruction
}

// DecodeModule decodes a WASM binary module into semantic IR.
// It returns a decoded module and nil error on success. On failure, it returns
// a (possibly partial) module and a diag.ErrorList.
func DecodeModule(bin []byte) (*wasmir.Module, error) {
	out := &wasmir.Module{}
	var diags diag.ErrorList

	r := bytes.NewReader(bin)
	decodePreamble(r, &diags)

	var funcTypeIdxs []uint32
	var funcBodies []decodedFuncBody

	seenType := false
	seenFunction := false
	seenExport := false
	seenCode := false

	for !atEOF(r) {
		sectionID, err := readByte(r)
		if err != nil {
			diags.Addf("failed to read section id: %v", err)
			break
		}

		sectionSize, err := readU32(r)
		if err != nil {
			diags.Addf("failed to read section %d size: %v", sectionID, err)
			break
		}
		sectionPayload, err := readN(r, int(sectionSize))
		if err != nil {
			diags.Addf("failed to read section %d payload: %v", sectionID, err)
			break
		}
		sr := bytes.NewReader(sectionPayload)

		switch sectionID {
		case 0:
			// Ignore custom sections in this MVP decoder.
		case sectionTypeID:
			if seenType {
				diags.Addf("duplicate type section")
				break
			}
			seenType = true
			out.Types = decodeTypeSection(sr, &diags)

		case sectionFunctionID:
			if seenFunction {
				diags.Addf("duplicate function section")
				break
			}
			seenFunction = true
			funcTypeIdxs = decodeFunctionSection(sr, &diags)

		case sectionExportID:
			if seenExport {
				diags.Addf("duplicate export section")
				break
			}
			seenExport = true
			out.Exports = decodeExportSection(sr, &diags)

		case sectionCodeID:
			if seenCode {
				diags.Addf("duplicate code section")
				break
			}
			seenCode = true
			funcBodies = decodeCodeSection(sr, &diags)

		default:
			diags.Addf("unsupported section id %d", sectionID)
		}

		if !atEOF(sr) {
			diags.Addf("section %d was not fully consumed", sectionID)
		}
	}

	if len(funcTypeIdxs) != len(funcBodies) {
		diags.Addf("function/code count mismatch: %d type indices vs %d code entries", len(funcTypeIdxs), len(funcBodies))
	}

	pairCount := min(len(funcTypeIdxs), len(funcBodies))
	for i := 0; i < pairCount; i++ {
		out.Funcs = append(out.Funcs, wasmir.Function{
			TypeIdx: funcTypeIdxs[i],
			Locals:  funcBodies[i].locals,
			Body:    funcBodies[i].body,
		})
	}

	if diags.HasAny() {
		return out, diags
	}
	return out, nil
}

func decodePreamble(r *bytes.Reader, diags *diag.ErrorList) {
	magic, err := readN(r, len(wasmMagic))
	if err != nil {
		diags.Addf("failed to read wasm magic: %v", err)
		return
	}
	if string(magic) != wasmMagic {
		diags.Addf("bad wasm magic: got %x", magic)
		return
	}

	version, err := readN(r, len(wasmVersion))
	if err != nil {
		diags.Addf("failed to read wasm version: %v", err)
		return
	}
	if string(version) != wasmVersion {
		diags.Addf("unsupported wasm version: got %x", version)
	}
}

func decodeTypeSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.FuncType {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("type section: invalid vector length: %v", err)
		return nil
	}

	out := make([]wasmir.FuncType, 0, n)
	for i := uint32(0); i < n; i++ {
		form, err := readByte(r)
		if err != nil {
			diags.Addf("type[%d]: failed to read form: %v", i, err)
			break
		}
		if form != typeCodeFunc {
			diags.Addf("type[%d]: unsupported type form 0x%x", i, form)
			break
		}

		params := decodeValueTypeVec(r, fmt.Sprintf("type[%d] params", i), diags)
		results := decodeValueTypeVec(r, fmt.Sprintf("type[%d] results", i), diags)
		out = append(out, wasmir.FuncType{Params: params, Results: results})
	}
	return out
}

func decodeFunctionSection(r *bytes.Reader, diags *diag.ErrorList) []uint32 {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("function section: invalid vector length: %v", err)
		return nil
	}

	out := make([]uint32, 0, n)
	for i := uint32(0); i < n; i++ {
		typeIdx, err := readU32(r)
		if err != nil {
			diags.Addf("function[%d]: invalid type index: %v", i, err)
			break
		}
		out = append(out, typeIdx)
	}
	return out
}

func decodeExportSection(r *bytes.Reader, diags *diag.ErrorList) []wasmir.Export {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("export section: invalid vector length: %v", err)
		return nil
	}

	out := make([]wasmir.Export, 0, n)
	for i := uint32(0); i < n; i++ {
		name, err := readName(r)
		if err != nil {
			diags.Addf("export[%d]: invalid name: %v", i, err)
			break
		}
		kindByte, err := readByte(r)
		if err != nil {
			diags.Addf("export[%d]: missing kind: %v", i, err)
			break
		}
		index, err := readU32(r)
		if err != nil {
			diags.Addf("export[%d]: invalid index: %v", i, err)
			break
		}

		kind, ok := decodeExportKind(kindByte)
		if !ok {
			diags.Addf("export[%d]: unsupported kind 0x%x", i, kindByte)
			continue
		}
		out = append(out, wasmir.Export{Name: name, Kind: kind, Index: index})
	}
	return out
}

func decodeCodeSection(r *bytes.Reader, diags *diag.ErrorList) []decodedFuncBody {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("code section: invalid vector length: %v", err)
		return nil
	}

	out := make([]decodedFuncBody, 0, n)
	for i := uint32(0); i < n; i++ {
		bodySize, err := readU32(r)
		if err != nil {
			diags.Addf("code[%d]: invalid body size: %v", i, err)
			break
		}
		bodyBytes, err := readN(r, int(bodySize))
		if err != nil {
			diags.Addf("code[%d]: body out of bounds: %v", i, err)
			break
		}

		br := bytes.NewReader(bodyBytes)
		locals := decodeLocals(br, i, diags)
		instrs := decodeInstructionExpr(br, i, diags)
		if !atEOF(br) {
			diags.Addf("code[%d]: trailing bytes after instruction expression", i)
		}

		out = append(out, decodedFuncBody{locals: locals, body: instrs})
	}
	return out
}

func decodeLocals(r *bytes.Reader, funcIdx uint32, diags *diag.ErrorList) []wasmir.ValueType {
	declCount, err := readU32(r)
	if err != nil {
		diags.Addf("code[%d]: invalid local decl count: %v", funcIdx, err)
		return nil
	}

	var locals []wasmir.ValueType
	for i := uint32(0); i < declCount; i++ {
		n, err := readU32(r)
		if err != nil {
			diags.Addf("code[%d] localdecl[%d]: invalid count: %v", funcIdx, i, err)
			break
		}
		tyCode, err := readByte(r)
		if err != nil {
			diags.Addf("code[%d] localdecl[%d]: missing value type: %v", funcIdx, i, err)
			break
		}
		ty, ok := decodeValueType(tyCode)
		if !ok {
			diags.Addf("code[%d] localdecl[%d]: unsupported value type 0x%x", funcIdx, i, tyCode)
			break
		}
		for j := uint32(0); j < n; j++ {
			locals = append(locals, ty)
		}
	}
	return locals
}

func decodeInstructionExpr(r *bytes.Reader, funcIdx uint32, diags *diag.ErrorList) []wasmir.Instruction {
	var out []wasmir.Instruction

	for !atEOF(r) {
		op, err := readByte(r)
		if err != nil {
			diags.Addf("code[%d]: failed reading opcode: %v", funcIdx, err)
			return out
		}

		switch op {
		case opLocalGetCode:
			localIndex, err := readU32(r)
			if err != nil {
				diags.Addf("code[%d]: local.get missing/invalid immediate: %v", funcIdx, err)
				return out
			}
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrLocalGet, LocalIndex: localIndex})
		case opI32AddCode:
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrI32Add})
		case opEndCode:
			out = append(out, wasmir.Instruction{Kind: wasmir.InstrEnd})
			return out
		default:
			diags.Addf("code[%d]: unsupported opcode 0x%x", funcIdx, op)
			return out
		}
	}

	diags.Addf("code[%d]: unterminated instruction expression (missing end)", funcIdx)
	return out
}

func decodeValueTypeVec(r *bytes.Reader, where string, diags *diag.ErrorList) []wasmir.ValueType {
	n, err := readU32(r)
	if err != nil {
		diags.Addf("%s: invalid vector length: %v", where, err)
		return nil
	}

	out := make([]wasmir.ValueType, 0, n)
	for i := uint32(0); i < n; i++ {
		b, err := readByte(r)
		if err != nil {
			diags.Addf("%s[%d]: missing value type: %v", where, i, err)
			break
		}
		vt, ok := decodeValueType(b)
		if !ok {
			diags.Addf("%s[%d]: unsupported value type 0x%x", where, i, b)
			break
		}
		out = append(out, vt)
	}
	return out
}

func decodeValueType(code byte) (wasmir.ValueType, bool) {
	switch code {
	case valueTypeI32Code:
		return wasmir.ValueTypeI32, true
	default:
		return 0, false
	}
}

func decodeExportKind(code byte) (wasmir.ExternalKind, bool) {
	switch code {
	case exportKindFunctionCode:
		return wasmir.ExternalKindFunction, true
	default:
		return 0, false
	}
}

// atEOF reports whether r has no unread bytes left.
func atEOF(r *bytes.Reader) bool {
	return r.Len() == 0
}

// readByte reads one byte from r.
// It returns an "unexpected EOF" error when no bytes remain.
func readByte(r *bytes.Reader) (byte, error) {
	b, err := r.ReadByte()
	if err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	return b, nil
}

// readN reads exactly n bytes from r.
// It returns an "unexpected EOF" error when fewer than n bytes are available.
func readN(r *bytes.Reader, n int) ([]byte, error) {
	if n < 0 {
		return nil, fmt.Errorf("negative length %d", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	if err != nil {
		if err == io.EOF {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return b, nil
}

// readU32 reads an unsigned 32-bit LEB128 value from r.
// It rejects values that overflow uint32.
func readU32(r *bytes.Reader) (uint32, error) {
	v, err := binary.ReadUvarint(r)
	if err != nil {
		if err == io.EOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("u32 overflow: %d", v)
	}
	return uint32(v), nil
}

// readName reads a WASM name from r as: u32 byte length followed by UTF-8
// bytes.
func readName(r *bytes.Reader) (string, error) {
	n, err := readU32(r)
	if err != nil {
		return "", err
	}
	b, err := readN(r, int(n))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("invalid UTF-8 name")
	}
	return string(b), nil
}
