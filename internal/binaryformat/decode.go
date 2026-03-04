package binaryformat

import (
	"fmt"
	"unicode/utf8"

	"github.com/eliben/watgo/internal/diag"
	"github.com/eliben/watgo/internal/wasmir"
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

	r := newByteReader(bin)
	decodePreamble(r, &diags)

	var funcTypeIdxs []uint32
	var funcBodies []decodedFuncBody

	seenType := false
	seenFunction := false
	seenExport := false
	seenCode := false

	for !r.eof() {
		sectionID, err := r.readByte()
		if err != nil {
			diags.Addf("failed to read section id: %v", err)
			break
		}

		sectionSize, err := r.readULEB128()
		if err != nil {
			diags.Addf("failed to read section %d size: %v", sectionID, err)
			break
		}
		sectionPayload, err := r.readN(int(sectionSize))
		if err != nil {
			diags.Addf("failed to read section %d payload: %v", sectionID, err)
			break
		}
		sr := newByteReader(sectionPayload)

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

		if !sr.eof() {
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

func decodePreamble(r *byteReader, diags *diag.ErrorList) {
	magic, err := r.readN(len(wasmMagic))
	if err != nil {
		diags.Addf("failed to read wasm magic: %v", err)
		return
	}
	if string(magic) != wasmMagic {
		diags.Addf("bad wasm magic: got %x", magic)
		return
	}

	version, err := r.readN(len(wasmVersion))
	if err != nil {
		diags.Addf("failed to read wasm version: %v", err)
		return
	}
	if string(version) != wasmVersion {
		diags.Addf("unsupported wasm version: got %x", version)
	}
}

func decodeTypeSection(r *byteReader, diags *diag.ErrorList) []wasmir.FuncType {
	n, err := r.readULEB128()
	if err != nil {
		diags.Addf("type section: invalid vector length: %v", err)
		return nil
	}

	out := make([]wasmir.FuncType, 0, n)
	for i := uint32(0); i < n; i++ {
		form, err := r.readByte()
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

func decodeFunctionSection(r *byteReader, diags *diag.ErrorList) []uint32 {
	n, err := r.readULEB128()
	if err != nil {
		diags.Addf("function section: invalid vector length: %v", err)
		return nil
	}

	out := make([]uint32, 0, n)
	for i := uint32(0); i < n; i++ {
		typeIdx, err := r.readULEB128()
		if err != nil {
			diags.Addf("function[%d]: invalid type index: %v", i, err)
			break
		}
		out = append(out, typeIdx)
	}
	return out
}

func decodeExportSection(r *byteReader, diags *diag.ErrorList) []wasmir.Export {
	n, err := r.readULEB128()
	if err != nil {
		diags.Addf("export section: invalid vector length: %v", err)
		return nil
	}

	out := make([]wasmir.Export, 0, n)
	for i := uint32(0); i < n; i++ {
		name, err := r.readName()
		if err != nil {
			diags.Addf("export[%d]: invalid name: %v", i, err)
			break
		}
		kindByte, err := r.readByte()
		if err != nil {
			diags.Addf("export[%d]: missing kind: %v", i, err)
			break
		}
		index, err := r.readULEB128()
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

func decodeCodeSection(r *byteReader, diags *diag.ErrorList) []decodedFuncBody {
	n, err := r.readULEB128()
	if err != nil {
		diags.Addf("code section: invalid vector length: %v", err)
		return nil
	}

	out := make([]decodedFuncBody, 0, n)
	for i := uint32(0); i < n; i++ {
		bodySize, err := r.readULEB128()
		if err != nil {
			diags.Addf("code[%d]: invalid body size: %v", i, err)
			break
		}
		bodyBytes, err := r.readN(int(bodySize))
		if err != nil {
			diags.Addf("code[%d]: body out of bounds: %v", i, err)
			break
		}

		br := newByteReader(bodyBytes)
		locals := decodeLocals(br, i, diags)
		instrs := decodeInstructionExpr(br, i, diags)
		if !br.eof() {
			diags.Addf("code[%d]: trailing bytes after instruction expression", i)
		}

		out = append(out, decodedFuncBody{locals: locals, body: instrs})
	}
	return out
}

func decodeLocals(r *byteReader, funcIdx uint32, diags *diag.ErrorList) []wasmir.ValueType {
	declCount, err := r.readULEB128()
	if err != nil {
		diags.Addf("code[%d]: invalid local decl count: %v", funcIdx, err)
		return nil
	}

	var locals []wasmir.ValueType
	for i := uint32(0); i < declCount; i++ {
		n, err := r.readULEB128()
		if err != nil {
			diags.Addf("code[%d] localdecl[%d]: invalid count: %v", funcIdx, i, err)
			break
		}
		tyCode, err := r.readByte()
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

func decodeInstructionExpr(r *byteReader, funcIdx uint32, diags *diag.ErrorList) []wasmir.Instruction {
	var out []wasmir.Instruction

	for !r.eof() {
		op, err := r.readByte()
		if err != nil {
			diags.Addf("code[%d]: failed reading opcode: %v", funcIdx, err)
			return out
		}

		switch op {
		case opLocalGetCode:
			localIndex, err := r.readULEB128()
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

func decodeValueTypeVec(r *byteReader, where string, diags *diag.ErrorList) []wasmir.ValueType {
	n, err := r.readULEB128()
	if err != nil {
		diags.Addf("%s: invalid vector length: %v", where, err)
		return nil
	}

	out := make([]wasmir.ValueType, 0, n)
	for i := uint32(0); i < n; i++ {
		b, err := r.readByte()
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

type byteReader struct {
	buf []byte
	off int
}

func newByteReader(buf []byte) *byteReader {
	return &byteReader{buf: buf}
}

func (r *byteReader) eof() bool {
	return r.off >= len(r.buf)
}

func (r *byteReader) readByte() (byte, error) {
	if r.eof() {
		return 0, fmt.Errorf("unexpected EOF")
	}
	b := r.buf[r.off]
	r.off++
	return b, nil
}

func (r *byteReader) readN(n int) ([]byte, error) {
	if n < 0 || r.off+n > len(r.buf) {
		return nil, fmt.Errorf("unexpected EOF")
	}
	b := r.buf[r.off : r.off+n]
	r.off += n
	return b, nil
}

func (r *byteReader) readULEB128() (uint32, error) {
	var result uint32
	var shift uint

	for i := 0; i < 5; i++ {
		b, err := r.readByte()
		if err != nil {
			return 0, err
		}

		result |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, nil
		}
		shift += 7
	}

	return 0, fmt.Errorf("invalid u32 LEB128 sequence")
}

func (r *byteReader) readName() (string, error) {
	n, err := r.readULEB128()
	if err != nil {
		return "", err
	}
	b, err := r.readN(int(n))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("invalid UTF-8 name")
	}
	return string(b), nil
}
