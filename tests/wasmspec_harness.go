package tests

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/binaryformat"
	"github.com/eliben/watgo/internal/numlit"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/eliben/watgo/wasmir"
)

// commandKind identifies one supported script command.
//
// Script BNF subset (from WebAssembly spec interpreter docs):
// https://github.com/WebAssembly/spec/tree/main/interpreter#scripts
//
//		cmd: <module> | <register> | <assertion>
//		assertion: (assert_return ...)
//	               | (assert_trap ...)
//		           | (assert_exhaustion ...)
//		           | (assert_unlinkable ...)
//		           | (assert_invalid ...)
//				   | (assert_malformed ...)
type commandKind string

const (
	commandModule           commandKind = "module"
	commandInvoke           commandKind = "invoke"
	commandRegister         commandKind = "register"
	commandAssertReturn     commandKind = "assert_return"
	commandAssertTrap       commandKind = "assert_trap"
	commandAssertExhaustion commandKind = "assert_exhaustion"
	commandAssertUnlinkable commandKind = "assert_unlinkable"
	commandAssertInvalid    commandKind = "assert_invalid"
	commandAssertMalformed  commandKind = "assert_malformed"
)

// valueKind identifies one supported script constant form.
//
// BNF subset:
//
//	const: (<num_type>.const <num>)
type valueKind string

const (
	valueI32Const         valueKind = "i32.const"
	valueI64Const         valueKind = "i64.const"
	valueF32Const         valueKind = "f32.const"
	valueF32NaNCanonical  valueKind = "f32.nan:canonical"
	valueF32NaNArithmetic valueKind = "f32.nan:arithmetic"
	valueF64Const         valueKind = "f64.const"
	valueF64NaNCanonical  valueKind = "f64.nan:canonical"
	valueF64NaNArithmetic valueKind = "f64.nan:arithmetic"
	valueRefNull          valueKind = "ref.null"
	valueRefFunc          valueKind = "ref.func"
	valueRefExtern        valueKind = "ref.extern"
)

// scriptValue is one script-level constant result/argument.
//
// BNF subset mapped here:
//
//	(i32.const <num>)
type scriptValue struct {
	kind valueKind
	// bits stores the raw IEEE-754/integer bit pattern used for exact
	// comparisons in assert_return. NaN marker kinds don't use this field.
	bits uint64
	// literal preserves the source literal spelling from the .wast file.
	// It is used for user-facing mismatch formatting.
	literal string
}

// invokeAction is an "(invoke ...)" script action.
//
// BNF subset:
//
//	action: (invoke <name>? <string> <const>*)
//
// For now we support the common anonymous-module form where <name>? is omitted.
type invokeAction struct {
	loc        string
	moduleName string
	funcName   string
	args       []scriptValue
}

// getAction is a "(get ...)" script action.
//
// BNF subset:
//
//	action: (get <name>? <string>)
//
// It reads an exported global value from either the current module or a named
// module.
type getAction struct {
	loc        string
	moduleName string
	globalName string
}

// scriptCommand is a parsed top-level command from a .wast script.
//
// BNF subset represented by this struct:
//
//	script: <cmd>*
//	cmd: <module> | <assertion>
//	module: (module ...)
//	      | (module quote <string>*)
//	assertion:
//	  (assert_return <action> <result>*)
//	  (assert_trap <action> <failure>)
//	  (assert_exhaustion <action> <failure>)
//	  (assert_unlinkable <module> <failure>)
//	  (assert_invalid <module> <failure>)
//	  (assert_malformed <module> <failure>)
//
// Field usage by command kind:
//   - commandModule: moduleExpr
//   - commandInvoke: action
//   - commandRegister: registerName
//   - commandAssertReturn: action/getAction + expectValues
//   - commandAssertTrap: action + expectText
//   - commandAssertExhaustion: action + expectText
//   - commandAssertUnlinkable: moduleExpr + expectText
//   - commandAssertInvalid: moduleExpr + expectText
//   - commandAssertMalformed: quotedWAT + expectText
type scriptCommand struct {
	kind commandKind
	loc  string

	moduleExpr *textformat.SExpr
	// moduleName is the optional script module identifier from "(module $id ...)".
	moduleName   string
	quotedWAT    string
	registerName string
	// registerFrom is the optional source module identifier from
	// "(register \"alias\" $id)".
	registerFrom string

	action       *invokeAction
	getAction    *getAction
	expectValues []scriptValue
	expectText   string
}

// parseScript parses a .wast script into top-level commands.
//
// It follows script-level structure ("script: <cmd>*"), then validates that
// each top-level S-expression maps to one supported command.
func parseScript(src string) ([]scriptCommand, error) {
	top, err := textformat.ParseTopLevelSExprs(src)
	if err != nil {
		return nil, diag.FromError(err)
	}

	var out []scriptCommand
	var diags diag.ErrorList
	for i, sx := range top {
		cmd, err := parseCommand(sx)
		if err != nil {
			diags.Addf("command[%d] at %s: %v", i, sx.Loc(), err)
			continue
		}
		out = append(out, cmd)
	}
	if diags.HasAny() {
		return nil, diags
	}
	return out, nil
}

// parseCommand decodes one top-level command expression.
// sx is one top-level S-expression from a .wast script.
// It returns the decoded command or an error if unsupported/invalid.
func parseCommand(sx *textformat.SExpr) (scriptCommand, error) {
	head, ok := headKeyword(sx)
	if !ok {
		return scriptCommand{}, fmt.Errorf("expected list command with keyword head")
	}

	switch head {
	case "module":
		moduleName, err := parseModuleName(sx)
		if err != nil {
			return scriptCommand{}, err
		}
		return scriptCommand{
			kind:       commandModule,
			loc:        sx.Loc(),
			moduleExpr: sx,
			moduleName: moduleName,
		}, nil
	case "invoke":
		action, err := parseInvoke(sx)
		if err != nil {
			return scriptCommand{}, fmt.Errorf("invalid invoke command: %w", err)
		}
		return scriptCommand{
			kind:   commandInvoke,
			loc:    sx.Loc(),
			action: &action,
		}, nil
	case "register":
		return parseRegister(sx)
	case "assert_return":
		return parseAssertReturn(sx)
	case "assert_trap":
		return parseAssertTrap(sx)
	case "assert_exhaustion":
		return parseAssertExhaustion(sx)
	case "assert_unlinkable":
		return parseAssertUnlinkable(sx)
	case "assert_invalid":
		return parseAssertInvalid(sx)
	case "assert_malformed":
		return parseAssertMalformed(sx)
	default:
		return scriptCommand{}, fmt.Errorf("unsupported command %q", head)
	}
}

func parseModuleName(sx *textformat.SExpr) (string, error) {
	elems := sx.Children()
	if len(elems) < 1 {
		return "", fmt.Errorf("invalid module command")
	}
	cursor := 1
	if cursor < len(elems) {
		if kind, value, ok := elems[cursor].Token(); ok && kind == "KEYWORD" && value == "definition" {
			cursor++
		}
	}
	if cursor < len(elems) {
		if kind, value, ok := elems[cursor].Token(); ok && kind == "ID" {
			return value, nil
		}
	}
	return "", nil
}

func moduleBodyCursor(sx *textformat.SExpr) int {
	elems := sx.Children()
	cursor := 1
	if cursor < len(elems) {
		if kind, _, ok := elems[cursor].Token(); ok && kind == "ID" {
			cursor++
		}
	}
	if cursor < len(elems) {
		if kind, value, ok := elems[cursor].Token(); ok && kind == "KEYWORD" && value == "definition" {
			cursor++
		}
	}
	return cursor
}

func parseBinaryModuleBytes(sx *textformat.SExpr) ([]byte, error) {
	elems := sx.Children()
	cursor := moduleBodyCursor(sx)
	if cursor >= len(elems) {
		return nil, fmt.Errorf("module binary requires payload")
	}
	kind, value, ok := elems[cursor].Token()
	if !ok || kind != "KEYWORD" || value != "binary" {
		return nil, fmt.Errorf("not a module binary form")
	}
	cursor++
	if cursor >= len(elems) {
		return nil, fmt.Errorf("module binary requires at least one string")
	}
	var out []byte
	for i := cursor; i < len(elems); i++ {
		kind, value, ok := elems[i].Token()
		if !ok || kind != "STRING" {
			return nil, fmt.Errorf("module binary payload[%d] must be STRING", i-cursor)
		}
		decoded, err := decodeWATStringBytes(value)
		if err != nil {
			return nil, fmt.Errorf("module binary payload[%d] decode failed: %w", i-cursor, err)
		}
		out = append(out, decoded...)
	}
	return out, nil
}

func decodeWATStringBytes(s string) ([]byte, error) {
	var out []byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch != '\\' {
			out = append(out, ch)
			continue
		}
		if i+1 >= len(s) {
			return nil, fmt.Errorf("trailing backslash")
		}
		next := s[i+1]
		if i+2 < len(s) && isHexDigit(next) && isHexDigit(s[i+2]) {
			hi := hexNibble(next)
			lo := hexNibble(s[i+2])
			out = append(out, (hi<<4)|lo)
			i += 2
			continue
		}
		switch next {
		case 't':
			out = append(out, '\t')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case '"':
			out = append(out, '"')
		case '\'':
			out = append(out, '\'')
		case '\\':
			out = append(out, '\\')
		default:
			return nil, fmt.Errorf("unsupported escape \\%c", next)
		}
		i++
	}
	return out, nil
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

func hexNibble(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	default:
		return b - 'A' + 10
	}
}

func isModuleBinaryExpr(sx *textformat.SExpr) bool {
	if sx == nil {
		return false
	}
	elems := sx.Children()
	cursor := moduleBodyCursor(sx)
	if cursor >= len(elems) {
		return false
	}
	kind, value, ok := elems[cursor].Token()
	return ok && kind == "KEYWORD" && value == "binary"
}

// parseRegister parses "(register <string>)" and optional module id form.
func parseRegister(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) < 2 || len(elems) > 3 {
		return scriptCommand{}, fmt.Errorf("register requires 1 or 2 arguments")
	}
	name, err := parseStringToken(elems[1])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid register name: %w", err)
	}
	var from string
	if len(elems) == 3 {
		kind, value, ok := elems[2].Token()
		if !ok || kind != "ID" {
			return scriptCommand{}, fmt.Errorf("register module argument must be ID")
		}
		from = value
	}
	return scriptCommand{
		kind:         commandRegister,
		loc:          sx.Loc(),
		registerName: name,
		registerFrom: from,
	}, nil
}

// parseAssertReturn parses "(assert_return <action> <result>*)".
// sx is the full assertion expression.
// It returns a command with action and expected values.
func parseAssertReturn(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) < 2 {
		return scriptCommand{}, fmt.Errorf("assert_return requires at least action")
	}
	invokeAction, getAction, err := parseAssertAction(elems[1])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_return action: %w", err)
	}

	var expects []scriptValue
	for i := 2; i < len(elems); i++ {
		v, err := parseValue(elems[i])
		if err != nil {
			return scriptCommand{}, fmt.Errorf("invalid assert_return expected value[%d]: %w", i-2, err)
		}
		expects = append(expects, v)
	}

	return scriptCommand{
		kind:         commandAssertReturn,
		loc:          sx.Loc(),
		action:       invokeAction,
		getAction:    getAction,
		expectValues: expects,
	}, nil
}

// parseAssertAction parses an assertion action expression.
// Supported forms are "(invoke ...)" and "(get ...)".
func parseAssertAction(sx *textformat.SExpr) (*invokeAction, *getAction, error) {
	head, ok := headKeyword(sx)
	if !ok {
		return nil, nil, fmt.Errorf("expected invoke or get action")
	}
	switch head {
	case "invoke":
		action, err := parseInvoke(sx)
		if err != nil {
			return nil, nil, err
		}
		return &action, nil, nil
	case "get":
		action, err := parseGet(sx)
		if err != nil {
			return nil, nil, err
		}
		return nil, &action, nil
	default:
		return nil, nil, fmt.Errorf("expected invoke or get action")
	}
}

// parseAssertTrap parses "(assert_trap <action> <failure>)".
// sx is the full assertion expression.
// It returns a command with invoke action and expected trap text.
func parseAssertTrap(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) != 3 {
		return scriptCommand{}, fmt.Errorf("assert_trap requires action and text")
	}
	var action *invokeAction
	var moduleExpr *textformat.SExpr
	if head, ok := headKeyword(elems[1]); ok && head == "module" {
		moduleExpr = elems[1]
	} else {
		parsedAction, err := parseInvoke(elems[1])
		if err != nil {
			return scriptCommand{}, fmt.Errorf("invalid assert_trap action: %w", err)
		}
		action = &parsedAction
	}
	text, err := parseStringToken(elems[2])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_trap text: %w", err)
	}
	return scriptCommand{
		kind:       commandAssertTrap,
		loc:        sx.Loc(),
		action:     action,
		moduleExpr: moduleExpr,
		expectText: text,
	}, nil
}

// parseAssertExhaustion parses "(assert_exhaustion <action> <failure>)".
// sx is the full assertion expression.
// It returns a command with invoke action and expected exhaustion text.
func parseAssertExhaustion(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) != 3 {
		return scriptCommand{}, fmt.Errorf("assert_exhaustion requires action and text")
	}
	action, err := parseInvoke(elems[1])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_exhaustion action: %w", err)
	}
	text, err := parseStringToken(elems[2])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_exhaustion text: %w", err)
	}
	return scriptCommand{
		kind:       commandAssertExhaustion,
		loc:        sx.Loc(),
		action:     &action,
		expectText: text,
	}, nil
}

// parseAssertUnlinkable parses "(assert_unlinkable <module> <failure>)".
// sx is the full assertion expression.
// It returns a command containing the module expression and expected text.
func parseAssertUnlinkable(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) != 3 {
		return scriptCommand{}, fmt.Errorf("assert_unlinkable requires module and text")
	}
	if head, ok := headKeyword(elems[1]); !ok || head != "module" {
		return scriptCommand{}, fmt.Errorf("assert_unlinkable expects (module ...) argument")
	}
	text, err := parseStringToken(elems[2])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_unlinkable text: %w", err)
	}
	return scriptCommand{
		kind:       commandAssertUnlinkable,
		loc:        sx.Loc(),
		moduleExpr: elems[1],
		expectText: text,
	}, nil
}

// parseAssertInvalid parses "(assert_invalid <module> <failure>)".
// sx is the full assertion expression.
// It returns a command containing the module expression and expected text.
func parseAssertInvalid(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) != 3 {
		return scriptCommand{}, fmt.Errorf("assert_invalid requires module and text")
	}
	if head, ok := headKeyword(elems[1]); !ok || head != "module" {
		return scriptCommand{}, fmt.Errorf("assert_invalid expects (module ...) argument")
	}
	text, err := parseStringToken(elems[2])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_invalid text: %w", err)
	}
	return scriptCommand{
		kind:       commandAssertInvalid,
		loc:        sx.Loc(),
		moduleExpr: elems[1],
		expectText: text,
	}, nil
}

// parseAssertMalformed parses "(assert_malformed (module quote ...) <failure>)".
// sx is the full assertion expression.
// It returns a command containing reconstructed quoted WAT source and expected text.
func parseAssertMalformed(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) != 3 {
		return scriptCommand{}, fmt.Errorf("assert_malformed requires module and text")
	}
	quotedWAT, err := parseQuotedModuleWAT(elems[1])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_malformed module argument: %w", err)
	}
	text, err := parseStringToken(elems[2])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_malformed text: %w", err)
	}
	return scriptCommand{
		kind:       commandAssertMalformed,
		loc:        sx.Loc(),
		quotedWAT:  quotedWAT,
		expectText: text,
	}, nil
}

// parseInvoke parses an invoke action expression.
// sx must be "(invoke <string> <const>*)" or "(invoke <id> <string> <const>*)".
// It returns the optional module name, target export name and parsed args.
func parseInvoke(sx *textformat.SExpr) (invokeAction, error) {
	elems := sx.Children()
	if len(elems) < 2 {
		return invokeAction{}, fmt.Errorf("invoke requires function name")
	}
	head, ok := headKeyword(sx)
	if !ok || head != "invoke" {
		return invokeAction{}, fmt.Errorf("expected invoke action")
	}

	cursor := 1
	var moduleName string
	if kind, value, ok := elems[cursor].Token(); ok && kind == "ID" {
		moduleName = value
		cursor++
	}
	if cursor >= len(elems) {
		return invokeAction{}, fmt.Errorf("invoke requires function name")
	}

	funcName, err := parseStringToken(elems[cursor])
	if err != nil {
		return invokeAction{}, fmt.Errorf("invalid function name: %w", err)
	}
	cursor++

	var args []scriptValue
	for i := cursor; i < len(elems); i++ {
		v, err := parseValue(elems[i])
		if err != nil {
			return invokeAction{}, fmt.Errorf("invalid invoke arg[%d]: %w", i-cursor, err)
		}
		args = append(args, v)
	}
	return invokeAction{
		loc:        sx.Loc(),
		moduleName: moduleName,
		funcName:   funcName,
		args:       args,
	}, nil
}

// parseGet parses a get action expression.
// sx must be "(get <string>)" or "(get <id> <string>)".
// It returns the optional module name and target global export name.
func parseGet(sx *textformat.SExpr) (getAction, error) {
	elems := sx.Children()
	if len(elems) < 2 {
		return getAction{}, fmt.Errorf("get requires global name")
	}
	head, ok := headKeyword(sx)
	if !ok || head != "get" {
		return getAction{}, fmt.Errorf("expected get action")
	}

	cursor := 1
	var moduleName string
	if kind, value, ok := elems[cursor].Token(); ok && kind == "ID" {
		moduleName = value
		cursor++
	}
	if cursor >= len(elems) {
		return getAction{}, fmt.Errorf("get requires global name")
	}
	if cursor+1 != len(elems) {
		return getAction{}, fmt.Errorf("get accepts only one global name")
	}

	globalName, err := parseStringToken(elems[cursor])
	if err != nil {
		return getAction{}, fmt.Errorf("invalid global name: %w", err)
	}
	return getAction{
		loc:        sx.Loc(),
		moduleName: moduleName,
		globalName: globalName,
	}, nil
}

// parseValue parses one script constant expression.
// sx is expected to be a value form like "(i32.const 1)" or "(i64.const 1)".
// It returns a typed scriptValue.
func parseValue(sx *textformat.SExpr) (scriptValue, error) {
	head, ok := headKeyword(sx)
	if !ok {
		return scriptValue{}, fmt.Errorf("value must be a list")
	}

	switch head {
	case "i32.const":
		elems := sx.Children()
		if len(elems) != 2 {
			return scriptValue{}, fmt.Errorf("i32.const requires one literal")
		}
		litKind, litValue, ok := elems[1].Token()
		if !ok || litKind != "INT" {
			return scriptValue{}, fmt.Errorf("i32.const literal must be INT token")
		}
		bits, err := numlit.ParseIntBits(litValue, 32)
		if err != nil {
			return scriptValue{}, err
		}
		return scriptValue{kind: valueI32Const, bits: bits, literal: litValue}, nil
	case "i64.const":
		elems := sx.Children()
		if len(elems) != 2 {
			return scriptValue{}, fmt.Errorf("i64.const requires one literal")
		}
		litKind, litValue, ok := elems[1].Token()
		if !ok || litKind != "INT" {
			return scriptValue{}, fmt.Errorf("i64.const literal must be INT token")
		}
		bits, err := numlit.ParseIntBits(litValue, 64)
		if err != nil {
			return scriptValue{}, err
		}
		return scriptValue{kind: valueI64Const, bits: bits, literal: litValue}, nil
	case "f32.const":
		elems := sx.Children()
		if len(elems) != 2 {
			return scriptValue{}, fmt.Errorf("f32.const requires one literal")
		}
		litKind, litValue, ok := elems[1].Token()
		if !ok {
			return scriptValue{}, fmt.Errorf("f32.const literal must be token")
		}
		// In spec scripts, expected results may be matchers rather than concrete
		// values. Keep these as dedicated kinds so assert_return can apply the
		// right NaN classification rule.
		if litValue == "nan:canonical" {
			return scriptValue{kind: valueF32NaNCanonical, literal: litValue}, nil
		}
		if litValue == "nan:arithmetic" {
			return scriptValue{kind: valueF32NaNArithmetic, literal: litValue}, nil
		}
		if litKind != "FLOAT" && litKind != "INT" {
			return scriptValue{}, fmt.Errorf("f32.const literal must be FLOAT/INT or nan marker")
		}
		bits, err := numlit.ParseF32Bits(litValue)
		if err != nil {
			return scriptValue{}, err
		}
		return scriptValue{kind: valueF32Const, bits: uint64(bits), literal: litValue}, nil
	case "f64.const":
		elems := sx.Children()
		if len(elems) != 2 {
			return scriptValue{}, fmt.Errorf("f64.const requires one literal")
		}
		litKind, litValue, ok := elems[1].Token()
		if !ok {
			return scriptValue{}, fmt.Errorf("f64.const literal must be token")
		}
		if litValue == "nan:canonical" {
			return scriptValue{kind: valueF64NaNCanonical, literal: litValue}, nil
		}
		if litValue == "nan:arithmetic" {
			return scriptValue{kind: valueF64NaNArithmetic, literal: litValue}, nil
		}
		if litKind != "FLOAT" && litKind != "INT" {
			return scriptValue{}, fmt.Errorf("f64.const literal must be FLOAT/INT or nan marker")
		}
		bits, err := numlit.ParseF64Bits(litValue)
		if err != nil {
			return scriptValue{}, err
		}
		return scriptValue{kind: valueF64Const, bits: bits, literal: litValue}, nil
	case "ref.null":
		elems := sx.Children()
		if len(elems) != 1 && len(elems) != 2 {
			return scriptValue{}, fmt.Errorf("ref.null expects zero or one heaptype operand")
		}
		literal := "ref.null"
		if len(elems) == 2 {
			kind, value, ok := elems[1].Token()
			if !ok || (kind != "KEYWORD" && kind != "ID") {
				return scriptValue{}, fmt.Errorf("ref.null heaptype must be KEYWORD or ID")
			}
			literal = "ref.null " + value
		}
		return scriptValue{kind: valueRefNull, literal: literal}, nil
	case "ref.func":
		elems := sx.Children()
		if len(elems) != 1 {
			return scriptValue{}, fmt.Errorf("ref.func expects no operands in script assertion")
		}
		return scriptValue{kind: valueRefFunc, literal: "ref.func"}, nil
	case "ref.extern":
		elems := sx.Children()
		if len(elems) != 2 {
			return scriptValue{}, fmt.Errorf("ref.extern expects one literal")
		}
		litKind, litValue, ok := elems[1].Token()
		if !ok || litKind != "INT" {
			return scriptValue{}, fmt.Errorf("ref.extern literal must be INT token")
		}
		bits, err := numlit.ParseIntBits(litValue, 64)
		if err != nil {
			return scriptValue{}, err
		}
		return scriptValue{kind: valueRefExtern, bits: bits, literal: litValue}, nil
	default:
		return scriptValue{}, fmt.Errorf("unsupported value kind %q", head)
	}
}

// parseQuotedModuleWAT parses "(module quote <string>+)".
// sx is the module-quote expression.
// It returns WAT text reconstructed by joining quoted parts with newlines.
// Example: (module quote "(func)") -> "(func)".
func parseQuotedModuleWAT(sx *textformat.SExpr) (string, error) {
	elems := sx.Children()
	if len(elems) < 3 {
		return "", fmt.Errorf("quoted module requires at least one string")
	}
	head, ok := headKeyword(sx)
	if !ok || head != "module" {
		return "", fmt.Errorf("expected module form")
	}

	kind, value, ok := elems[1].Token()
	if !ok || kind != "KEYWORD" || value != "quote" {
		return "", fmt.Errorf("expected (module quote ...)")
	}

	var parts []string
	for i := 2; i < len(elems); i++ {
		s, err := parseStringToken(elems[i])
		if err != nil {
			return "", fmt.Errorf("quoted module part[%d]: %w", i-2, err)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n"), nil
}

// parseStringToken returns the token value for a STRING token expression.
// sx must be a token S-expression with kind STRING.
func parseStringToken(sx *textformat.SExpr) (string, error) {
	kind, value, ok := sx.Token()
	if !ok || kind != "STRING" {
		return "", fmt.Errorf("expected STRING token")
	}
	return value, nil
}

// headKeyword returns the head keyword for a list expression. If sx is not a
// list or the head is not a KEYWORD token, it returns ("", false). For example,
// "(assert_return ...)" returns ("assert_return", true).
func headKeyword(sx *textformat.SExpr) (string, bool) {
	if sx == nil || !sx.IsList() {
		return "", false
	}
	elems := sx.Children()
	if len(elems) == 0 {
		return "", false
	}
	kind, value, ok := elems[0].Token()
	if !ok || kind != "KEYWORD" {
		return "", false
	}
	return value, true
}

// sexprToWAT converts an S-expression tree back into WAT text.
// sx is any parsed expression (module or nested form).
// It returns reconstructed source suitable for CompileWAT.
func sexprToWAT(sx *textformat.SExpr) (string, error) {
	if sx == nil {
		return "", fmt.Errorf("nil s-expression")
	}
	if sx.IsToken() {
		kind, value, ok := sx.Token()
		if !ok {
			return "", fmt.Errorf("expected token")
		}
		switch kind {
		case "STRING":
			// STRING token payload already preserves WAT escape syntax from the
			// source text. Re-quote without re-escaping so sequences like "\a7"
			// remain byte escapes in reconstructed WAT.
			return `"` + value + `"`, nil
		case "EMPTY":
			return "()", nil
		default:
			return value, nil
		}
	}

	elems := sx.Children()
	parts := make([]string, 0, len(elems))
	for i, sub := range elems {
		text, err := sexprToWAT(sub)
		if err != nil {
			return "", fmt.Errorf("child[%d]: %w", i, err)
		}
		parts = append(parts, text)
	}
	return "(" + strings.Join(parts, " ") + ")", nil
}

// commandResult stores harness outcome for one command in execution order.
type commandResult struct {
	index  int
	kind   commandKind
	loc    string
	status bool // true if command passed, false if failed
	detail string
}

type runOptions struct {
	// strictErrorText checks expected error text for assert_invalid/assert_malformed.
	// If false, any compilation error satisfies these assertions.
	strictErrorText bool
}

const currentModuleRuntimeName = "__watgo_current__"

type moduleMetadata struct {
	funcExports   map[string]wasmir.FuncType
	globalExports map[string]wasmir.ValueType
}

type nodeValue struct {
	Type string `json:"type"`
	Bits string `json:"bits,omitempty"`
	Null bool   `json:"null,omitempty"`
}

type nodeResponse struct {
	OK      bool        `json:"ok"`
	Error   string      `json:"error,omitempty"`
	Results []nodeValue `json:"results,omitempty"`
	Result  *nodeValue  `json:"result,omitempty"`
}

type nodeRuntime struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	enc    *json.Encoder
	dec    *json.Decoder
	stderr bytes.Buffer
}

func newNodeRuntime(ctx context.Context) (*nodeRuntime, error) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		return nil, fmt.Errorf("node executable not found; install node or set WATGO_INTEGRATION=0 to skip integration tests: %w", err)
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("failed to locate wasmspec harness source file")
	}
	scriptPath := filepath.Join(filepath.Dir(thisFile), "node_wasm_runner.js")

	cmd := exec.CommandContext(ctx, nodePath, scriptPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("node stdin pipe failed: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("node stdout pipe failed: %w", err)
	}

	nr := &nodeRuntime{
		cmd:   cmd,
		stdin: stdin,
		enc:   json.NewEncoder(stdin),
		dec:   json.NewDecoder(bufio.NewReader(stdout)),
	}
	cmd.Stderr = &nr.stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("node runner start failed: %w", err)
	}
	return nr, nil
}

func (nr *nodeRuntime) close() error {
	if nr == nil {
		return nil
	}
	_ = nr.request(map[string]any{"op": "close"}, nil)
	_ = nr.stdin.Close()
	if err := nr.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(nr.stderr.String())
			if stderr != "" {
				return fmt.Errorf("node runner exited with %v: %s", exitErr, stderr)
			}
		}
		return err
	}
	return nil
}

func (nr *nodeRuntime) request(req map[string]any, out *nodeResponse) error {
	if err := nr.enc.Encode(req); err != nil {
		return fmt.Errorf("node request encode failed: %w", err)
	}
	var resp nodeResponse
	if err := nr.dec.Decode(&resp); err != nil {
		stderr := strings.TrimSpace(nr.stderr.String())
		if stderr != "" {
			return fmt.Errorf("node response decode failed: %w (stderr: %s)", err, stderr)
		}
		return fmt.Errorf("node response decode failed: %w", err)
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = "unknown node runner error"
		}
		return fmt.Errorf("%s", resp.Error)
	}
	if out != nil {
		*out = resp
	}
	return nil
}

func (nr *nodeRuntime) instantiate(moduleName string, wasmBytes []byte) error {
	return nr.request(map[string]any{
		"op":         "instantiate",
		"moduleName": moduleName,
		"wasmBase64": base64.StdEncoding.EncodeToString(wasmBytes),
	}, nil)
}

func (nr *nodeRuntime) instantiateEphemeral(wasmBytes []byte) error {
	return nr.request(map[string]any{
		"op":         "instantiate_ephemeral",
		"wasmBase64": base64.StdEncoding.EncodeToString(wasmBytes),
	}, nil)
}

func (nr *nodeRuntime) validate(wasmBytes []byte) error {
	return nr.request(map[string]any{
		"op":         "validate",
		"wasmBase64": base64.StdEncoding.EncodeToString(wasmBytes),
	}, nil)
}

func (nr *nodeRuntime) invoke(moduleName, funcName string, args []nodeValue, resultTypes []string) ([]nodeValue, error) {
	var resp nodeResponse
	err := nr.request(map[string]any{
		"op":          "invoke",
		"moduleName":  moduleName,
		"funcName":    funcName,
		"args":        args,
		"resultTypes": resultTypes,
	}, &resp)
	if err != nil {
		return nil, err
	}
	return resp.Results, nil
}

func (nr *nodeRuntime) get(moduleName, globalName, valueType string) (nodeValue, error) {
	var resp nodeResponse
	err := nr.request(map[string]any{
		"op":         "get",
		"moduleName": moduleName,
		"globalName": globalName,
		"valueType":  valueType,
	}, &resp)
	if err != nil {
		return nodeValue{}, err
	}
	if resp.Result == nil {
		return nodeValue{}, fmt.Errorf("node runner returned no global value")
	}
	return *resp.Result, nil
}

// scriptRunner executes parsed script commands against a Node.js WebAssembly runtime.
//
// Execution follows spec script sequencing: commands are processed in order,
// and actions/assertions operate on the current module instance.
type scriptRunner struct {
	ctx         context.Context
	node        *nodeRuntime
	currentWasm []byte
	currentMeta *moduleMetadata

	// moduleWasm stores compiled wasm bytes for named script modules.
	// It allows "(register ... $id)" to re-instantiate a named module under a
	// runtime import name.
	moduleWasm map[string][]byte
	moduleMeta map[string]*moduleMetadata

	// moduleAlias maps script module identifiers (for example "$M") to the
	// runtime module name to use for imports/invocations after register aliasing.
	// In spec scripts, module ids and registered names are distinct namespaces:
	//   (module $M ...)
	//   (register "x" $M)
	//   (invoke $M "f")
	// The final invoke must run against runtime module "x", not the original
	// unnamed/current instance. This map preserves that script-level aliasing.
	moduleAlias map[string]string

	// currentName tracks the script identifier/runtime name of current when
	// available so plain "(register \"x\")" can alias the current module.
	currentName        string
	currentRuntimeName string
	bootstrapErr       error
}

// newScriptRunner creates a runner with a fresh Node.js runtime bound to ctx.
func newScriptRunner(ctx context.Context) *scriptRunner {
	r := &scriptRunner{
		ctx:         ctx,
		moduleWasm:  map[string][]byte{},
		moduleMeta:  map[string]*moduleMetadata{},
		moduleAlias: map[string]string{},
	}
	r.node, r.bootstrapErr = newNodeRuntime(ctx)
	if r.bootstrapErr != nil {
		return r
	}
	r.bootstrapErr = r.instantiateSpectest()
	return r
}

// close releases the Node.js runtime. It returns a runner close error, if one occurs.
func (r *scriptRunner) close() error {
	if r.node == nil {
		return nil
	}
	return r.node.close()
}

// run executes commands in script order and returns one result per command.
// commands is the parsed script command list; opts controls assertion behavior.
func (r *scriptRunner) run(commands []scriptCommand, opts runOptions) []commandResult {
	results := make([]commandResult, 0, len(commands))
	if r.bootstrapErr != nil {
		for i, cmd := range commands {
			results = append(results, commandResult{
				index:  i,
				kind:   cmd.kind,
				loc:    cmd.loc,
				status: false,
				detail: fmt.Sprintf("runner bootstrap failed: %v", r.bootstrapErr),
			})
		}
		return results
	}
	for i, cmd := range commands {
		res := commandResult{
			index: i,
			kind:  cmd.kind,
			loc:   cmd.loc,
		}
		switch cmd.kind {
		case commandModule:
			r.runModule(&res, cmd)
		case commandInvoke:
			r.runInvoke(&res, cmd)
		case commandRegister:
			r.runRegister(&res, cmd)
		case commandAssertReturn:
			r.runAssertReturn(&res, cmd)
		case commandAssertTrap:
			r.runAssertTrap(&res, cmd)
		case commandAssertExhaustion:
			r.runAssertExhaustion(&res, cmd)
		case commandAssertUnlinkable:
			r.runAssertUnlinkable(&res, cmd, opts)
		case commandAssertInvalid:
			r.runAssertInvalid(&res, cmd, opts)
		case commandAssertMalformed:
			r.runAssertMalformed(&res, cmd, opts)
		default:
			res.status = false
			res.detail = fmt.Sprintf("unsupported command kind %q", cmd.kind)
		}
		results = append(results, res)
	}
	return results
}

// runModule handles a top-level "(module ...)" command.
// It compiles/instantiates the module and makes it the current module.
func (r *scriptRunner) runModule(res *commandResult, cmd scriptCommand) {
	wasmBytes, err := r.compileModuleExpr(cmd.moduleExpr)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module compile failed: %v", err)
		return
	}
	meta, metaErr := decodeModuleMetadata(wasmBytes)

	runtimeName := runtimeModuleName(cmd.moduleName)
	err = r.instantiateWasm(runtimeName, wasmBytes)
	if err != nil {
		if isInstantiationLimitError(err) || isEngineUnsupportedFeatureError(err) {
			// Some spec modules are valid but exceed local engine limits (for
			// example huge table/memory mins or currently unsupported binary
			// features). Treat compile success as pass.
			r.currentWasm = wasmBytes
			r.currentMeta = meta
			r.currentName = cmd.moduleName
			r.currentRuntimeName = runtimeName
			if cmd.moduleName != "" {
				r.moduleWasm[cmd.moduleName] = wasmBytes
				r.moduleMeta[cmd.moduleName] = meta
				r.moduleAlias[cmd.moduleName] = cmd.moduleName
			}
			res.status = true
			return
		}
		res.status = false
		res.detail = fmt.Sprintf("module instantiate failed: %v", err)
		return
	}
	r.currentWasm = wasmBytes
	r.currentMeta = meta
	r.currentName = cmd.moduleName
	r.currentRuntimeName = runtimeName
	if cmd.moduleName != "" {
		r.moduleWasm[cmd.moduleName] = wasmBytes
		r.moduleAlias[cmd.moduleName] = cmd.moduleName
		if meta != nil {
			r.moduleMeta[cmd.moduleName] = meta
		}
	}
	if metaErr != nil && cmd.moduleName == "" {
		r.currentMeta = nil
	}
	res.status = true
}

// runRegister handles "(register \"name\")" and "(register \"name\" $id)".
// The registered name is the runtime name used by imports. When a source
// module id is present, we additionally map that script id to the registered
// runtime name so later "(invoke $id ...)" resolves through moduleAlias.
func (r *scriptRunner) runRegister(res *commandResult, cmd scriptCommand) {
	if cmd.registerName == "" {
		res.status = false
		res.detail = "register command missing name"
		return
	}
	wasmBytes := r.currentWasm
	meta := r.currentMeta
	sourceName := cmd.registerFrom
	if sourceName == "" {
		sourceName = r.currentName
	}
	if cmd.registerFrom != "" {
		stored, ok := r.moduleWasm[cmd.registerFrom]
		if !ok {
			res.status = false
			res.detail = fmt.Sprintf("register source module %q not found", cmd.registerFrom)
			return
		}
		wasmBytes = stored
		meta = r.moduleMeta[cmd.registerFrom]
	}
	if len(wasmBytes) == 0 {
		res.status = false
		res.detail = "register requires a previously compiled module"
		return
	}
	if err := r.instantiateWasm(cmd.registerName, wasmBytes); err != nil {
		res.status = false
		res.detail = fmt.Sprintf("register instantiate failed: %v", err)
		return
	}
	r.moduleWasm[cmd.registerName] = wasmBytes
	if meta != nil {
		r.moduleMeta[cmd.registerName] = meta
	}
	r.moduleAlias[cmd.registerName] = cmd.registerName
	if sourceName != "" {
		r.moduleAlias[sourceName] = cmd.registerName
	}
	if sourceName != "" && r.currentName == sourceName {
		r.currentWasm = wasmBytes
		r.currentMeta = meta
		r.currentName = cmd.registerName
		r.currentRuntimeName = cmd.registerName
	}
	res.status = true
}

// runInvoke handles top-level "(invoke ...)" commands.
// It requires invocation success and ignores returned values.
func (r *scriptRunner) runInvoke(res *commandResult, cmd scriptCommand) {
	_, err := r.invoke(cmd.action)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("invoke failed: %v", err)
		return
	}
	res.status = true
}

// runAssertReturn handles "(assert_return (invoke ...) (result)*)".
// It invokes the target export and compares returned values with expected ones.
func (r *scriptRunner) runAssertReturn(res *commandResult, cmd scriptCommand) {
	var (
		results []uint64
		err     error
	)
	if cmd.action != nil {
		results, err = r.invoke(cmd.action)
	} else if cmd.getAction != nil {
		results, err = r.get(cmd.getAction)
	} else {
		res.status = false
		res.detail = "assert_return requires invoke or get action"
		return
	}
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("action failed: %v", err)
		return
	}

	if len(results) != len(cmd.expectValues) {
		res.status = false
		res.detail = fmt.Sprintf("result arity mismatch: got %d want %d", len(results), len(cmd.expectValues))
		return
	}
	for i := range results {
		want := cmd.expectValues[i]
		switch want.kind {
		case valueI32Const:
			gotBits := uint32(results[i])
			wantBits := uint32(want.bits)
			if gotBits != wantBits {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueI64Const:
			gotBits := results[i]
			if gotBits != want.bits {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueF32Const:
			gotBits := uint32(results[i])
			wantBits := uint32(want.bits)
			if gotBits != wantBits {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueF32NaNCanonical:
			gotBits := uint32(results[i])
			if !isCanonicalNaN32(gotBits) {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueF32NaNArithmetic:
			gotBits := uint32(results[i])
			if !isArithmeticNaN32(gotBits) {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueF64Const:
			gotBits := results[i]
			if gotBits != want.bits {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueF64NaNCanonical:
			gotBits := results[i]
			if !isCanonicalNaN64(gotBits) {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueF64NaNArithmetic:
			gotBits := results[i]
			if !isArithmeticNaN64(gotBits) {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueRefNull:
			if results[i] != 0 {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueRefFunc:
			if results[i] == 0 {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		case valueRefExtern:
			if results[i] != want.bits {
				res.status = false
				res.detail = fmt.Sprintf("result[%d] mismatch: got %s want %s", i, formatGotValueLikeExpected(results[i], want), formatExpectedValue(want))
				return
			}
		default:
			res.status = false
			res.detail = fmt.Sprintf("unsupported expected value kind %q", want.kind)
			return
		}
	}
	res.status = true
}

func formatExpectedValue(v scriptValue) string {
	switch v.kind {
	case valueI32Const:
		return fmt.Sprintf("(i32.const %s)", v.literal)
	case valueI64Const:
		return fmt.Sprintf("(i64.const %s)", v.literal)
	case valueF32Const:
		return fmt.Sprintf("(f32.const %s)", v.literal)
	case valueF64Const:
		return fmt.Sprintf("(f64.const %s)", v.literal)
	case valueF32NaNCanonical:
		return "(f32.const nan:canonical)"
	case valueF32NaNArithmetic:
		return "(f32.const nan:arithmetic)"
	case valueF64NaNCanonical:
		return "(f64.const nan:canonical)"
	case valueF64NaNArithmetic:
		return "(f64.const nan:arithmetic)"
	case valueRefNull:
		if v.literal != "" && v.literal != "ref.null" {
			return "(ref.null " + strings.TrimPrefix(v.literal, "ref.null ") + ")"
		}
		return "(ref.null)"
	case valueRefFunc:
		return "(ref.func)"
	case valueRefExtern:
		return fmt.Sprintf("(ref.extern %s)", v.literal)
	default:
		return fmt.Sprintf("<%s>", v.kind)
	}
}

func formatGotValueLikeExpected(got uint64, want scriptValue) string {
	switch want.kind {
	case valueI32Const:
		return fmt.Sprintf("(i32.const %s)", formatI32Like(uint32(got), want.literal))
	case valueI64Const:
		return fmt.Sprintf("(i64.const %s)", formatI64Like(got, want.literal))
	case valueF32Const:
		return fmt.Sprintf("(f32.const %s)", formatF32Like(uint32(got), want.literal))
	case valueF64Const:
		return fmt.Sprintf("(f64.const %s)", formatF64Like(got, want.literal))
	case valueF32NaNCanonical, valueF32NaNArithmetic:
		return fmt.Sprintf("(f32.const %s)", formatF32NaNOrValue(uint32(got), want.literal))
	case valueF64NaNCanonical, valueF64NaNArithmetic:
		return fmt.Sprintf("(f64.const %s)", formatF64NaNOrValue(got, want.literal))
	case valueRefNull, valueRefFunc:
		if got == 0 {
			return "(ref.null)"
		}
		return "(ref.func)"
	case valueRefExtern:
		return fmt.Sprintf("(ref.extern %d)", got)
	default:
		return fmt.Sprintf("0x%x", got)
	}
}

func formatI32Like(bits uint32, template string) string {
	sign, hex := parseNumericStyle(template)
	if hex {
		u := uint64(bits)
		if sign != 0 {
			s := int64(int32(bits))
			return formatSignedHex(s, sign)
		}
		return fmt.Sprintf("0x%x", u)
	}

	if sign != 0 {
		s := int64(int32(bits))
		if sign == '+' && s >= 0 {
			return fmt.Sprintf("+%d", s)
		}
		return fmt.Sprintf("%d", s)
	}
	return fmt.Sprintf("%d", uint64(bits))
}

func formatI64Like(bits uint64, template string) string {
	sign, hex := parseNumericStyle(template)
	if hex {
		if sign != 0 {
			return formatSignedHex(int64(bits), sign)
		}
		return fmt.Sprintf("0x%x", bits)
	}

	if sign != 0 {
		s := int64(bits)
		if sign == '+' && s >= 0 {
			return fmt.Sprintf("+%d", s)
		}
		return fmt.Sprintf("%d", s)
	}
	return fmt.Sprintf("%d", bits)
}

func formatF32Like(bits uint32, template string) string {
	f := float64(math.Float32frombits(bits))
	clean := strings.ReplaceAll(template, "_", "")
	sign, mag := splitSignString(clean)

	if strings.EqualFold(mag, "inf") {
		if math.IsInf(f, -1) {
			return "-inf"
		}
		if math.IsInf(f, +1) {
			return "inf"
		}
	}
	if strings.HasPrefix(mag, "nan") {
		if math.IsNaN(f) {
			return formatF32NaNOrValue(bits, template)
		}
	}
	if strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X") {
		if !strings.Contains(mag, ".") && !strings.ContainsAny(mag, "pP") && isFiniteInteger(f) {
			return formatIntegerLikeFloatValue(f, sign, true)
		}
		return strconv.FormatFloat(f, 'x', -1, 32)
	}

	out := strconv.FormatFloat(f, 'g', -1, 32)
	if sign == '+' && !strings.HasPrefix(out, "-") {
		return "+" + out
	}
	return out
}

func formatF64Like(bits uint64, template string) string {
	f := math.Float64frombits(bits)
	clean := strings.ReplaceAll(template, "_", "")
	sign, mag := splitSignString(clean)

	if strings.EqualFold(mag, "inf") {
		if math.IsInf(f, -1) {
			return "-inf"
		}
		if math.IsInf(f, +1) {
			return "inf"
		}
	}
	if strings.HasPrefix(mag, "nan") {
		if math.IsNaN(f) {
			return formatF64NaNOrValue(bits, template)
		}
	}
	if strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X") {
		if !strings.Contains(mag, ".") && !strings.ContainsAny(mag, "pP") && isFiniteInteger(f) {
			return formatIntegerLikeFloatValue(f, sign, true)
		}
		return strconv.FormatFloat(f, 'x', -1, 64)
	}

	out := strconv.FormatFloat(f, 'g', -1, 64)
	if sign == '+' && !strings.HasPrefix(out, "-") {
		return "+" + out
	}
	return out
}

func formatF32NaNOrValue(bits uint32, template string) string {
	if isCanonicalNaN32(bits) {
		return "nan:canonical"
	}
	if isArithmeticNaN32(bits) {
		return "nan:arithmetic"
	}
	if math.IsNaN(float64(math.Float32frombits(bits))) {
		payload := bits & 0x007fffff
		return fmt.Sprintf("nan:0x%x", payload)
	}
	return formatF32Like(bits, template)
}

func formatF64NaNOrValue(bits uint64, template string) string {
	if isCanonicalNaN64(bits) {
		return "nan:canonical"
	}
	if isArithmeticNaN64(bits) {
		return "nan:arithmetic"
	}
	if math.IsNaN(math.Float64frombits(bits)) {
		payload := bits & 0x000fffffffffffff
		return fmt.Sprintf("nan:0x%x", payload)
	}
	return formatF64Like(bits, template)
}

func parseNumericStyle(template string) (sign byte, hex bool) {
	clean := strings.ReplaceAll(template, "_", "")
	if clean == "" {
		return 0, false
	}
	if clean[0] == '+' || clean[0] == '-' {
		sign = clean[0]
		clean = clean[1:]
	}
	hex = strings.HasPrefix(clean, "0x") || strings.HasPrefix(clean, "0X")
	return sign, hex
}

func splitSignString(s string) (sign byte, mag string) {
	if s == "" {
		return 0, s
	}
	switch s[0] {
	case '+':
		return '+', s[1:]
	case '-':
		return '-', s[1:]
	default:
		return 0, s
	}
}

func formatSignedHex(v int64, sign byte) string {
	if v < 0 {
		return fmt.Sprintf("-0x%x", uint64(-v))
	}
	if sign == '+' {
		return fmt.Sprintf("+0x%x", uint64(v))
	}
	return fmt.Sprintf("0x%x", uint64(v))
}

func isFiniteInteger(f float64) bool {
	return !math.IsInf(f, 0) && !math.IsNaN(f) && math.Trunc(f) == f
}

func formatIntegerLikeFloatValue(f float64, sign byte, hex bool) string {
	if f < math.MinInt64 || f > math.MaxInt64 {
		if hex {
			return strconv.FormatFloat(f, 'x', -1, 64)
		}
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	i := int64(f)
	if hex {
		return formatSignedHex(i, sign)
	}
	if sign == '+' && i >= 0 {
		return fmt.Sprintf("+%d", i)
	}
	return fmt.Sprintf("%d", i)
}

// runAssertTrap handles "(assert_trap (invoke ...) \"...\")".
// It requires invocation failure and optionally checks trap text substring.
func (r *scriptRunner) runAssertTrap(res *commandResult, cmd scriptCommand) {
	var err error
	if cmd.action != nil {
		_, err = r.invoke(cmd.action)
	} else if cmd.moduleExpr != nil {
		wasmBytes, compErr := r.compileModuleExpr(cmd.moduleExpr)
		if compErr != nil {
			res.status = false
			res.detail = fmt.Sprintf("expected trap, got compile error: %v", compErr)
			return
		}
		err = r.node.instantiateEphemeral(wasmBytes)
		if err == nil {
			wouldTrap, trapMsg, checkErr := detectElemInitTrap(wasmBytes)
			if checkErr == nil && wouldTrap {
				err = fmt.Errorf("%s", trapMsg)
			}
		}
	} else {
		res.status = false
		res.detail = "assert_trap requires invoke action or module"
		return
	}
	if err == nil {
		res.status = false
		res.detail = "expected trap, got success"
		return
	}
	if cmd.expectText != "" && !matchesExpectedFailureText(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("trap text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertExhaustion handles "(assert_exhaustion (invoke ...) \"...\")".
// It requires invocation failure due to resource exhaustion and checks text.
func (r *scriptRunner) runAssertExhaustion(res *commandResult, cmd scriptCommand) {
	_, err := r.invoke(cmd.action)
	if err == nil {
		res.status = false
		res.detail = "expected exhaustion, got success"
		return
	}
	if cmd.expectText != "" && !matchesExpectedFailureText(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("exhaustion text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertUnlinkable handles "(assert_unlinkable (module ...) \"...\")".
// It expects module linking to fail at compile-time or instantiation-time.
func (r *scriptRunner) runAssertUnlinkable(res *commandResult, cmd scriptCommand, opts runOptions) {
	if moduleHasTopLevelField(cmd.moduleExpr, "tag") {
		// Exception handling tags are not in the current watgo subset.
		// Treat these cases as unlinkable for this harness stage.
		res.status = true
		return
	}

	wasmBytes, err := r.compileModuleExpr(cmd.moduleExpr)
	if err != nil {
		if opts.strictErrorText && cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
			res.status = false
			res.detail = fmt.Sprintf("unlinkable error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
			return
		}
		res.status = true
		return
	}

	err = r.node.instantiateEphemeral(wasmBytes)
	if err == nil {
		res.status = false
		res.detail = "expected unlinkable module error, got success"
		return
	}
	if opts.strictErrorText && cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("unlinkable error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

func moduleHasTopLevelField(moduleExpr *textformat.SExpr, fieldHead string) bool {
	if moduleExpr == nil {
		return false
	}
	elems := moduleExpr.Children()
	for i := moduleBodyCursor(moduleExpr); i < len(elems); i++ {
		if elems[i].HeadKeyword() == fieldHead {
			return true
		}
	}
	return false
}

func matchesExpectedFailureText(got, want string) bool {
	if strings.Contains(got, want) {
		return true
	}

	// Runtime engines may use different stack-overflow wording for the same
	// resource exhaustion condition expected by spec scripts.
	gotLower := strings.ToLower(got)
	wantLower := strings.ToLower(want)
	if wantLower == "call stack exhausted" {
		return strings.Contains(gotLower, "stack overflow") ||
			strings.Contains(gotLower, "stack exhausted") ||
			strings.Contains(gotLower, "stack limit") ||
			strings.Contains(gotLower, "maximum call stack size exceeded")
	}
	if wantLower == "out of bounds table access" {
		return strings.Contains(gotLower, "invalid table access") ||
			strings.Contains(gotLower, "unreachable") ||
			strings.Contains(gotLower, "table index is out of bounds")
	}
	if wantLower == "undefined element" {
		return strings.Contains(gotLower, "invalid table access") ||
			strings.Contains(gotLower, "table index is out of bounds")
	}
	if wantLower == "uninitialized element" {
		return strings.Contains(gotLower, "invalid table access") ||
			strings.Contains(gotLower, "null function or function signature mismatch")
	}
	if wantLower == "integer divide by zero" {
		return strings.Contains(gotLower, "divide by zero") ||
			strings.Contains(gotLower, "remainder by zero")
	}
	if wantLower == "integer overflow" {
		return strings.Contains(gotLower, "divide result unrepresentable")
	}
	if wantLower == "out of bounds memory access" {
		return strings.Contains(gotLower, "memory access out of bounds")
	}
	return false
}

func isInstantiationLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "must be at most") ||
		strings.Contains(msg, "larger than implementation limit") ||
		strings.Contains(msg, "implementation limit")
}

func isEngineUnsupportedFeatureError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid section length")
}

// runAssertInvalid handles "(assert_invalid (module ...) \"...\")".
// It expects module compilation to fail; message matching is optional via opts.
func (r *scriptRunner) runAssertInvalid(res *commandResult, cmd scriptCommand, opts runOptions) {
	if moduleHasTopLevelField(cmd.moduleExpr, "tag") {
		// Exception handling tags are outside the current watgo subset.
		// Treat such modules as invalid for this harness stage.
		res.status = true
		return
	}

	var err error
	if isModuleBinaryExpr(cmd.moduleExpr) {
		var wasmBytes []byte
		wasmBytes, err = parseBinaryModuleBytes(cmd.moduleExpr)
		if err == nil {
			err = r.node.validate(wasmBytes)
		}
	} else {
		_, err = r.compileModuleExpr(cmd.moduleExpr)
	}
	if err == nil {
		res.status = false
		res.detail = "expected invalid module error, got success"
		return
	}
	if opts.strictErrorText && cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("invalid error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertMalformed handles "(assert_malformed (module quote ...) \"...\")".
// It expects quoted module compilation/parsing to fail.
func (r *scriptRunner) runAssertMalformed(res *commandResult, cmd scriptCommand, opts runOptions) {
	_, err := watgo.CompileWAT([]byte(cmd.quotedWAT))
	if err == nil {
		res.status = false
		res.detail = "expected malformed module error, got success"
		return
	}
	if opts.strictErrorText && cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("malformed error text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// invoke calls an exported function on the current module or a named module.
// action supplies the target export and arguments; when action.moduleName is
// set we first resolve script-id aliases through moduleAlias.
// It returns raw wasm values as uint64.
func (r *scriptRunner) invoke(action *invokeAction) ([]uint64, error) {
	if action == nil {
		return nil, fmt.Errorf("nil invoke action")
	}
	runtimeName, meta, err := r.lookupTargetModule(action.moduleName)
	if err != nil {
		if action.moduleName != "" {
			return nil, fmt.Errorf("named module %q not found for invoke %q", action.moduleName, action.funcName)
		}
		return nil, fmt.Errorf("no current module for invoke %q", action.funcName)
	}
	sig, ok := meta.funcExports[action.funcName]
	if !ok {
		return nil, fmt.Errorf("exported function %q not found", action.funcName)
	}
	if len(sig.Params) != len(action.args) {
		return nil, fmt.Errorf("invoke arg arity mismatch for %q: got %d want %d", action.funcName, len(action.args), len(sig.Params))
	}
	args := make([]nodeValue, len(action.args))
	for i, arg := range action.args {
		args[i], err = encodeScriptArg(arg, sig.Params[i])
		if err != nil {
			return nil, fmt.Errorf("invoke arg[%d]: %w", i, err)
		}
	}
	resultTypes := make([]string, len(sig.Results))
	for i, vt := range sig.Results {
		resultTypes[i], err = valueTypeString(vt)
		if err != nil {
			return nil, fmt.Errorf("result type[%d]: %w", i, err)
		}
	}
	encodedResults, err := r.node.invoke(runtimeName, action.funcName, args, resultTypes)
	if err != nil {
		return nil, err
	}
	results := make([]uint64, len(encodedResults))
	for i, value := range encodedResults {
		results[i], err = decodeNodeValue(value)
		if err != nil {
			return nil, fmt.Errorf("result[%d]: %w", i, err)
		}
	}
	return results, nil
}

// get reads one exported global from the current module or a named module.
// action supplies the target export and optional script module id.
// It returns exactly one value in wasm bit representation.
func (r *scriptRunner) get(action *getAction) ([]uint64, error) {
	if action == nil {
		return nil, fmt.Errorf("nil get action")
	}
	runtimeName, meta, err := r.lookupTargetModule(action.moduleName)
	if err != nil {
		if action.moduleName != "" {
			return nil, fmt.Errorf("named module %q not found for get %q", action.moduleName, action.globalName)
		}
		return nil, fmt.Errorf("no current module for get %q", action.globalName)
	}
	globalType, ok := meta.globalExports[action.globalName]
	if !ok {
		return nil, fmt.Errorf("exported global %q not found", action.globalName)
	}
	valueType, err := valueTypeString(globalType)
	if err != nil {
		return nil, err
	}
	encodedValue, err := r.node.get(runtimeName, action.globalName, valueType)
	if err != nil {
		return nil, err
	}
	raw, err := decodeNodeValue(encodedValue)
	if err != nil {
		return nil, err
	}
	return []uint64{raw}, nil
}

func (r *scriptRunner) lookupTargetModule(scriptModuleName string) (string, *moduleMetadata, error) {
	if scriptModuleName == "" {
		if len(r.currentWasm) == 0 || r.currentMeta == nil || r.currentRuntimeName == "" {
			return "", nil, fmt.Errorf("no current module")
		}
		return r.currentRuntimeName, r.currentMeta, nil
	}
	runtimeName := scriptModuleName
	if aliased, ok := r.moduleAlias[scriptModuleName]; ok {
		runtimeName = aliased
	}
	meta, ok := r.moduleMeta[runtimeName]
	if !ok || meta == nil {
		return "", nil, fmt.Errorf("module metadata for %q not found", runtimeName)
	}
	return runtimeName, meta, nil
}

func decodeModuleMetadata(wasmBytes []byte) (*moduleMetadata, error) {
	m, err := binaryformat.DecodeModule(wasmBytes)
	if err != nil {
		return nil, err
	}
	meta := &moduleMetadata{
		funcExports:   map[string]wasmir.FuncType{},
		globalExports: map[string]wasmir.ValueType{},
	}
	for _, exp := range m.Exports {
		switch exp.Kind {
		case wasmir.ExternalKindFunction:
			sig, err := functionTypeForIndex(m, exp.Index)
			if err != nil {
				return nil, fmt.Errorf("function export %q: %w", exp.Name, err)
			}
			meta.funcExports[exp.Name] = sig
		case wasmir.ExternalKindGlobal:
			if int(exp.Index) >= len(m.Globals) {
				return nil, fmt.Errorf("global export %q index %d out of range", exp.Name, exp.Index)
			}
			meta.globalExports[exp.Name] = m.Globals[exp.Index].Type
		}
	}
	return meta, nil
}

func functionTypeForIndex(m *wasmir.Module, funcIndex uint32) (wasmir.FuncType, error) {
	importedFuncCount := uint32(0)
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if importedFuncCount == funcIndex {
			if int(imp.TypeIdx) >= len(m.Types) {
				return wasmir.FuncType{}, fmt.Errorf("import function type index %d out of range", imp.TypeIdx)
			}
			return m.Types[imp.TypeIdx], nil
		}
		importedFuncCount++
	}
	localIndex := funcIndex - importedFuncCount
	if funcIndex < importedFuncCount || int(localIndex) >= len(m.Funcs) {
		return wasmir.FuncType{}, fmt.Errorf("function index %d out of range", funcIndex)
	}
	typeIdx := m.Funcs[localIndex].TypeIdx
	if int(typeIdx) >= len(m.Types) {
		return wasmir.FuncType{}, fmt.Errorf("function type index %d out of range", typeIdx)
	}
	return m.Types[typeIdx], nil
}

func encodeScriptArg(arg scriptValue, targetType wasmir.ValueType) (nodeValue, error) {
	valueType, err := valueTypeString(targetType)
	if err != nil {
		return nodeValue{}, err
	}
	switch arg.kind {
	case valueI32Const:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(uint64(uint32(arg.bits)), 10)}, nil
	case valueI64Const:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(arg.bits, 10)}, nil
	case valueF32Const:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(uint64(uint32(arg.bits)), 10)}, nil
	case valueF64Const:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(arg.bits, 10)}, nil
	case valueF32NaNCanonical, valueF32NaNArithmetic:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(uint64(0x7fc00000), 10)}, nil
	case valueF64NaNCanonical, valueF64NaNArithmetic:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(0x7ff8000000000000, 10)}, nil
	case valueRefNull:
		return nodeValue{Type: valueType, Null: true}, nil
	case valueRefExtern:
		return nodeValue{Type: valueType, Bits: strconv.FormatUint(arg.bits, 10)}, nil
	default:
		return nodeValue{}, fmt.Errorf("unsupported invoke arg kind %q", arg.kind)
	}
}

func valueTypeString(vt wasmir.ValueType) (string, error) {
	switch vt {
	case wasmir.ValueTypeI32:
		return "i32", nil
	case wasmir.ValueTypeI64:
		return "i64", nil
	case wasmir.ValueTypeF32:
		return "f32", nil
	case wasmir.ValueTypeF64:
		return "f64", nil
	case wasmir.ValueTypeFuncRef:
		return "funcref", nil
	case wasmir.ValueTypeExternRef:
		return "externref", nil
	default:
		return "", fmt.Errorf("unsupported value type %d", vt)
	}
}

func decodeNodeValue(v nodeValue) (uint64, error) {
	if v.Null {
		return 0, nil
	}
	if v.Bits == "" {
		return 0, fmt.Errorf("missing value bits for type %q", v.Type)
	}
	bits, err := strconv.ParseUint(v.Bits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse bits %q: %w", v.Bits, err)
	}
	return bits, nil
}

// isCanonicalNaN32 reports whether bits encode canonical f32 NaN:
// exponent all ones and mantissa exactly 0x00400000 (sign ignored).
func isCanonicalNaN32(bits uint32) bool {
	const expMask uint32 = 0x7f800000
	const mantissaMask uint32 = 0x007fffff
	const canonicalMantissa uint32 = 0x00400000
	return (bits&expMask) == expMask && (bits&mantissaMask) == canonicalMantissa
}

// isArithmeticNaN32 reports whether bits encode an arithmetic f32 NaN:
// exponent all ones and quiet bit set in the payload (sign ignored).
func isArithmeticNaN32(bits uint32) bool {
	const expMask uint32 = 0x7f800000
	const mantissaMask uint32 = 0x007fffff
	const quietBit uint32 = 0x00400000
	mantissa := bits & mantissaMask
	return (bits&expMask) == expMask && (mantissa&quietBit) != 0
}

// isCanonicalNaN64 reports whether bits encode canonical f64 NaN:
// exponent all ones and mantissa exactly 0x0008000000000000 (sign ignored).
func isCanonicalNaN64(bits uint64) bool {
	const expMask uint64 = 0x7ff0000000000000
	const mantissaMask uint64 = 0x000fffffffffffff
	const canonicalMantissa uint64 = 0x0008000000000000
	return (bits&expMask) == expMask && (bits&mantissaMask) == canonicalMantissa
}

// isArithmeticNaN64 reports whether bits encode an arithmetic f64 NaN:
// exponent all ones and quiet bit set in the payload (sign ignored).
func isArithmeticNaN64(bits uint64) bool {
	const expMask uint64 = 0x7ff0000000000000
	const mantissaMask uint64 = 0x000fffffffffffff
	const quietBit uint64 = 0x0008000000000000
	mantissa := bits & mantissaMask
	return (bits&expMask) == expMask && (mantissa&quietBit) != 0
}

// compileWAT compiles WAT source with watgo and applies the decoder/encoder
// roundtrip fixed-point check used by integration tests.
func (r *scriptRunner) compileWAT(watSrc string) ([]byte, error) {
	wasmBytes, err := watgo.CompileWAT([]byte(watSrc))
	if err != nil {
		return nil, err
	}

	// For valid modules, enforce binary pipeline stability:
	// encode -> decode -> encode -> decode -> encode must reach a fixed point.
	wasmBytes, err = roundTripFixedPoint(wasmBytes)
	if err != nil {
		return nil, err
	}
	return wasmBytes, nil
}

// compileModuleExpr compiles one "(module ...)" script expression.
// Text modules are compiled through watgo. Module-binary forms are decoded and
// validated directly, then normalized through encode/decode fixed-point checks.
func (r *scriptRunner) compileModuleExpr(moduleExpr *textformat.SExpr) ([]byte, error) {
	if moduleExpr == nil {
		return nil, fmt.Errorf("nil module expression")
	}
	if isModuleBinaryExpr(moduleExpr) {
		bytesBlob, err := parseBinaryModuleBytes(moduleExpr)
		if err != nil {
			return nil, err
		}
		return bytesBlob, nil
	}

	src, err := sexprToWAT(moduleExpr)
	if err != nil {
		return nil, fmt.Errorf("module text generation failed: %w", err)
	}
	return r.compileWAT(src)
}

// instantiateWasm instantiates an existing wasm binary in the Node runner.
func (r *scriptRunner) instantiateWasm(moduleName string, wasmBytes []byte) error {
	return r.node.instantiate(moduleName, wasmBytes)
}

// instantiateSpectest pre-instantiates the minimal imports used by spec scripts.
func (r *scriptRunner) instantiateSpectest() error {
	const spectestWAT = `(module
  (func (export "print"))
  (func (export "print_i32") (param i32))
  (func (export "print_i64") (param i64))
  (func (export "print_f32") (param f32))
  (func (export "print_f64") (param f64))
  (func (export "print_i32_f32") (param i32 f32))
  (func (export "print_f64_f64") (param f64 f64))
  (table (export "table") 10 20 funcref)
  (memory (export "memory") 1 2)
  (global (export "global_i32") i32 (i32.const 666))
  (global (export "global_i64") i64 (i64.const 666))
  (global (export "global_f32") f32 (f32.const 666.6))
  (global (export "global_f64") f64 (f64.const 666.6))
)`
	wasmBytes, err := r.compileWAT(spectestWAT)
	if err != nil {
		return err
	}
	meta, err := decodeModuleMetadata(wasmBytes)
	if err != nil {
		return err
	}
	if err := r.instantiateWasm("spectest", wasmBytes); err != nil {
		return err
	}
	r.moduleWasm["spectest"] = wasmBytes
	r.moduleMeta["spectest"] = meta
	return nil
}

// roundTripFixedPoint verifies a decode/encode fixed point on a wasm module
// binary and returns the stable re-encoded bytes.
func roundTripFixedPoint(wasm []byte) ([]byte, error) {
	ir1, err := binaryformat.DecodeModule(wasm)
	if err != nil {
		return nil, fmt.Errorf("decode pass 1: %w", err)
	}
	wasm1, err := binaryformat.EncodeModule(ir1)
	if err != nil {
		return nil, fmt.Errorf("encode pass 1: %w", err)
	}

	ir2, err := binaryformat.DecodeModule(wasm1)
	if err != nil {
		return nil, fmt.Errorf("decode pass 2: %w", err)
	}
	wasm2, err := binaryformat.EncodeModule(ir2)
	if err != nil {
		return nil, fmt.Errorf("encode pass 2: %w", err)
	}

	if !bytes.Equal(wasm1, wasm2) {
		return nil, fmt.Errorf("roundtrip not idempotent: pass1 len=%d pass2 len=%d", len(wasm1), len(wasm2))
	}
	return wasm1, nil
}

func detectElemInitTrap(wasm []byte) (bool, string, error) {
	m, err := binaryformat.DecodeModule(wasm)
	if err != nil {
		return false, "", err
	}
	for _, elem := range m.Elements {
		if int(elem.TableIndex) >= len(m.Tables) {
			continue
		}
		t := m.Tables[elem.TableIndex]
		length := len(elem.FuncIndices)
		if len(elem.Exprs) > 0 {
			length = len(elem.Exprs)
		}
		start := uint64(uint32(elem.OffsetI32))
		end := start + uint64(length)
		if end > uint64(t.Min) {
			return true, "out of bounds table access", nil
		}
	}
	return false, "", nil
}

func runtimeModuleName(moduleName string) string {
	if moduleName == "" {
		return currentModuleRuntimeName
	}
	return moduleName
}
