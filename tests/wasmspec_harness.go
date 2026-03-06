package tests

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/eliben/watgo"
	"github.com/eliben/watgo/diag"
	"github.com/eliben/watgo/internal/textformat"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// commandKind identifies one supported script command.
//
// Script BNF subset (from WebAssembly spec interpreter docs):
// https://github.com/WebAssembly/spec/tree/main/interpreter#scripts
//
//	cmd: <module> | <assertion>
//	assertion: (assert_return ...) | (assert_trap ...)
//	           | (assert_invalid ...) | (assert_malformed ...)
type commandKind string

const (
	commandModule          commandKind = "module"
	commandAssertReturn    commandKind = "assert_return"
	commandAssertTrap      commandKind = "assert_trap"
	commandAssertInvalid   commandKind = "assert_invalid"
	commandAssertMalformed commandKind = "assert_malformed"
)

// valueKind identifies one supported script constant form.
//
// BNF subset:
//
//	const: (<num_type>.const <num>)
type valueKind string

const (
	valueI32Const valueKind = "i32.const"
)

// scriptValue is one script-level constant result/argument.
//
// BNF subset mapped here:
//
//	(i32.const <num>)
type scriptValue struct {
	kind valueKind
	i32  uint32
}

// invokeAction is an "(invoke ...)" script action.
//
// BNF subset:
//
//	action: (invoke <name>? <string> <const>*)
//
// For now we support the common anonymous-module form where <name>? is omitted.
type invokeAction struct {
	loc      string
	funcName string
	args     []scriptValue
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
//	  (assert_invalid <module> <failure>)
//	  (assert_malformed <module> <failure>)
//
// Field usage by command kind:
//   - commandModule: moduleExpr
//   - commandAssertReturn: action + expectValues
//   - commandAssertTrap: action + expectText
//   - commandAssertInvalid: moduleExpr + expectText
//   - commandAssertMalformed: quotedWAT + expectText
type scriptCommand struct {
	kind commandKind
	loc  string

	moduleExpr *textformat.SExpr
	quotedWAT  string

	action       *invokeAction
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
		return scriptCommand{
			kind:       commandModule,
			loc:        sx.Loc(),
			moduleExpr: sx,
		}, nil
	case "assert_return":
		return parseAssertReturn(sx)
	case "assert_trap":
		return parseAssertTrap(sx)
	case "assert_invalid":
		return parseAssertInvalid(sx)
	case "assert_malformed":
		return parseAssertMalformed(sx)
	default:
		return scriptCommand{}, fmt.Errorf("unsupported command %q", head)
	}
}

// parseAssertReturn parses "(assert_return <action> <result>*)".
// sx is the full assertion expression.
// It returns a command with action and expected values.
func parseAssertReturn(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) < 2 {
		return scriptCommand{}, fmt.Errorf("assert_return requires at least action")
	}
	action, err := parseInvoke(elems[1])
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
		action:       &action,
		expectValues: expects,
	}, nil
}

// parseAssertTrap parses "(assert_trap <action> <failure>)".
// sx is the full assertion expression.
// It returns a command with invoke action and expected trap text.
func parseAssertTrap(sx *textformat.SExpr) (scriptCommand, error) {
	elems := sx.Children()
	if len(elems) != 3 {
		return scriptCommand{}, fmt.Errorf("assert_trap requires action and text")
	}
	action, err := parseInvoke(elems[1])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_trap action: %w", err)
	}
	text, err := parseStringToken(elems[2])
	if err != nil {
		return scriptCommand{}, fmt.Errorf("invalid assert_trap text: %w", err)
	}
	return scriptCommand{
		kind:       commandAssertTrap,
		loc:        sx.Loc(),
		action:     &action,
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
// sx must be "(invoke <string> <const>*)".
// It returns the target export name and parsed constant arguments.
func parseInvoke(sx *textformat.SExpr) (invokeAction, error) {
	elems := sx.Children()
	if len(elems) < 2 {
		return invokeAction{}, fmt.Errorf("invoke requires function name")
	}
	head, ok := headKeyword(sx)
	if !ok || head != "invoke" {
		return invokeAction{}, fmt.Errorf("expected invoke action")
	}

	funcName, err := parseStringToken(elems[1])
	if err != nil {
		return invokeAction{}, fmt.Errorf("invalid function name: %w", err)
	}

	var args []scriptValue
	for i := 2; i < len(elems); i++ {
		v, err := parseValue(elems[i])
		if err != nil {
			return invokeAction{}, fmt.Errorf("invalid invoke arg[%d]: %w", i-2, err)
		}
		args = append(args, v)
	}
	return invokeAction{
		loc:      sx.Loc(),
		funcName: funcName,
		args:     args,
	}, nil
}

// parseValue parses one script constant expression.
// sx is expected to be a value form like "(i32.const 1)".
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
		i32, err := parseI32Literal(litValue)
		if err != nil {
			return scriptValue{}, err
		}
		return scriptValue{kind: valueI32Const, i32: i32}, nil
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

// parseI32Literal parses a WAT integer literal into raw i32 bits.
// s may be decimal/hex and may include underscores.
// Example: "-1" -> 0xffffffff.
func parseI32Literal(s string) (uint32, error) {
	clean := strings.ReplaceAll(s, "_", "")
	v, err := strconv.ParseInt(clean, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid i32 literal %q", s)
	}
	return uint32(v), nil
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
			return strconv.Quote(value), nil
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

// scriptRunner executes parsed script commands against a wazero runtime.
//
// Execution follows spec script sequencing: commands are processed in order,
// and actions/assertions operate on the current module instance.
type scriptRunner struct {
	ctx     context.Context
	runtime wazero.Runtime
	current api.Module
}

// newScriptRunner creates a runner with a fresh wazero runtime bound to ctx.
func newScriptRunner(ctx context.Context) *scriptRunner {
	return &scriptRunner{
		ctx:     ctx,
		runtime: wazero.NewRuntime(ctx),
	}
}

// close releases the current module (if any) and closes the wazero runtime.
// It returns a runtime close error, if one occurs.
func (r *scriptRunner) close() error {
	if r.current != nil {
		_ = r.current.Close(r.ctx)
	}
	return r.runtime.Close(r.ctx)
}

// run executes commands in script order and returns one result per command.
// commands is the parsed script command list; opts controls assertion behavior.
func (r *scriptRunner) run(commands []scriptCommand, opts runOptions) []commandResult {
	results := make([]commandResult, 0, len(commands))
	for i, cmd := range commands {
		res := commandResult{
			index: i,
			kind:  cmd.kind,
			loc:   cmd.loc,
		}
		switch cmd.kind {
		case commandModule:
			r.runModule(&res, cmd)
		case commandAssertReturn:
			r.runAssertReturn(&res, cmd)
		case commandAssertTrap:
			r.runAssertTrap(&res, cmd)
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
	src, err := sexprToWAT(cmd.moduleExpr)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module text generation failed: %v", err)
		return
	}
	mod, err := r.compileAndInstantiate(src)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module compile/instantiate failed: %v", err)
		return
	}
	r.replaceCurrent(mod)
	res.status = true
}

// runAssertReturn handles "(assert_return (invoke ...) (result)*)".
// It invokes the target export and compares returned values with expected ones.
func (r *scriptRunner) runAssertReturn(res *commandResult, cmd scriptCommand) {
	results, err := r.invoke(cmd.action)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("invoke failed: %v", err)
		return
	}

	if len(results) != len(cmd.expectValues) {
		res.status = false
		res.detail = fmt.Sprintf("result arity mismatch: got %d want %d", len(results), len(cmd.expectValues))
		return
	}
	for i := range results {
		want := cmd.expectValues[i]
		if want.kind != valueI32Const {
			res.status = false
			res.detail = fmt.Sprintf("unsupported expected value kind %q", want.kind)
			return
		}
		gotBits := uint32(results[i])
		if gotBits != want.i32 {
			res.status = false
			res.detail = fmt.Sprintf("result[%d] mismatch: got 0x%x want 0x%x", i, gotBits, want.i32)
			return
		}
	}
	res.status = true
}

// runAssertTrap handles "(assert_trap (invoke ...) \"...\")".
// It requires invocation failure and optionally checks trap text substring.
func (r *scriptRunner) runAssertTrap(res *commandResult, cmd scriptCommand) {
	_, err := r.invoke(cmd.action)
	if err == nil {
		res.status = false
		res.detail = "expected trap, got success"
		return
	}
	if cmd.expectText != "" && !strings.Contains(err.Error(), cmd.expectText) {
		res.status = false
		res.detail = fmt.Sprintf("trap text mismatch: got %q want substring %q", err.Error(), cmd.expectText)
		return
	}
	res.status = true
}

// runAssertInvalid handles "(assert_invalid (module ...) \"...\")".
// It expects module compilation to fail; message matching is optional via opts.
func (r *scriptRunner) runAssertInvalid(res *commandResult, cmd scriptCommand, opts runOptions) {
	src, err := sexprToWAT(cmd.moduleExpr)
	if err != nil {
		res.status = false
		res.detail = fmt.Sprintf("module text generation failed: %v", err)
		return
	}
	_, err = watgo.CompileWAT([]byte(src))
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

// invoke calls an exported function on the current module.
// action supplies the target export name and script argument values.
// It returns raw wasm values as uint64, matching wazero's call API.
func (r *scriptRunner) invoke(action *invokeAction) ([]uint64, error) {
	if action == nil {
		return nil, fmt.Errorf("nil invoke action")
	}
	if r.current == nil {
		return nil, fmt.Errorf("no current module for invoke %q", action.funcName)
	}

	fn := r.current.ExportedFunction(action.funcName)
	if fn == nil {
		return nil, fmt.Errorf("exported function %q not found", action.funcName)
	}

	args := make([]uint64, len(action.args))
	for i, arg := range action.args {
		switch arg.kind {
		case valueI32Const:
			args[i] = uint64(arg.i32)
		default:
			return nil, fmt.Errorf("unsupported invoke arg kind %q", arg.kind)
		}
	}

	results, err := fn.Call(r.ctx, args...)
	if err != nil {
		return nil, err
	}
	return results, nil
}

// compileAndInstantiate compiles WAT source with watgo and instantiates it.
// It returns the instantiated module or an error from compile/instantiate.
func (r *scriptRunner) compileAndInstantiate(watSrc string) (api.Module, error) {
	wasmBytes, err := watgo.CompileWAT([]byte(watSrc))
	if err != nil {
		return nil, err
	}

	compiled, err := r.runtime.CompileModule(r.ctx, wasmBytes)
	if err != nil {
		return nil, err
	}

	mod, err := r.runtime.InstantiateModule(r.ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		_ = compiled.Close(r.ctx)
		return nil, err
	}
	return mod, nil
}

// replaceCurrent swaps the current module instance, closing any previous one.
func (r *scriptRunner) replaceCurrent(mod api.Module) {
	if r.current != nil {
		_ = r.current.Close(r.ctx)
	}
	r.current = mod
}
