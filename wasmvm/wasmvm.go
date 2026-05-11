// Package wasmvm exposes a minimal WebAssembly interpreter runtime for wasmir
// modules.
//
// The package can instantiate an already-validated wasmir.Module, look up
// exported functions, call them with runtime values, and satisfy WebAssembly
// function imports with Go callbacks.
package wasmvm

import (
	"fmt"
	"math"
	"slices"

	"github.com/eliben/watgo/internal/instrdef"
	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value passed into or returned from a Func.
//
// Type identifies which payload field is meaningful. For example, values with
// Type set to wasmir.ValueTypeI32 use I32, while values with Type set to
// wasmir.ValueTypeF64 use F64. Prefer the I32, I64, F32, and F64 constructors
// over constructing Value directly.
type Value struct {
	// Type is the WebAssembly value type carried by this value.
	Type wasmir.ValueType

	// I32 is the payload for wasmir.ValueTypeI32 values.
	I32 int32

	// I64 is the payload for wasmir.ValueTypeI64 values.
	I64 int64

	// F32 is the payload for wasmir.ValueTypeF32 values.
	F32 float32

	// F64 is the payload for wasmir.ValueTypeF64 values.
	F64 float64
}

// I32 returns a runtime Value whose type is wasmir.ValueTypeI32 and whose
// payload is v.
func I32(v int32) Value {
	return Value{Type: wasmir.ValueTypeI32, I32: v}
}

// I64 returns a runtime Value whose type is wasmir.ValueTypeI64 and whose
// payload is v.
func I64(v int64) Value {
	return Value{Type: wasmir.ValueTypeI64, I64: v}
}

// F32 returns a runtime Value whose type is wasmir.ValueTypeF32 and whose
// payload is v.
func F32(v float32) Value {
	return Value{Type: wasmir.ValueTypeF32, F32: v}
}

// F64 returns a runtime Value whose type is wasmir.ValueTypeF64 and whose
// payload is v.
func F64(v float64) Value {
	return Value{Type: wasmir.ValueTypeF64, F64: v}
}

// Imports maps WebAssembly import module names and field names to host externs.
//
// For an import such as (import "env" "inc" (func ...)), the corresponding
// Go value belongs at imports["env"]["inc"]. Only function imports are
// supported for now, so the extern value should be a HostFunc created with
// NewHostFunc.
type Imports map[string]map[string]Extern

// Extern is a runtime object supplied for a module import.
//
// HostFunc is currently the only supported Extern implementation. This
// interface is present so Imports can grow to memory, table, or global imports
// later without changing its shape.
type Extern interface {
	isExtern()
}

// HostFunc is a Go callback exposed as a WebAssembly function import.
//
// Params and Results are the WebAssembly function signature expected by the
// importing module. Func receives the calling context and argument values in
// parameter order, and returns result values in result order. The runtime checks
// the argument and result counts and value types against Params and Results.
type HostFunc struct {
	// Params is the host function's WebAssembly parameter type list.
	Params []wasmir.ValueType

	// Results is the host function's WebAssembly result type list.
	Results []wasmir.ValueType

	// Func is called when WebAssembly code invokes this host function.
	//
	// args contains one Value per parameter. The returned slice must contain one
	// Value per result. Returning an error aborts the WebAssembly call and
	// propagates the error to Func.Call.
	Func func(ctx *Context, args []Value) ([]Value, error)
}

// isExtern marks HostFunc as a valid import object.
func (HostFunc) isExtern() {}

// NewHostFunc returns a HostFunc with the given WebAssembly signature and Go
// callback.
//
// params and results are copied by reference, so callers should treat them as
// immutable after passing them here. fn must be non-nil before the HostFunc is
// used to instantiate a module; otherwise Instantiate returns an error.
func NewHostFunc(params, results []wasmir.ValueType, fn func(ctx *Context, args []Value) ([]Value, error)) HostFunc {
	return HostFunc{Params: params, Results: results, Func: fn}
}

// Context is passed to host functions during a WebAssembly call.
//
// Runtime is the runtime that owns the current instance. Instance is the module
// instance that made the call. These fields let host functions inspect or call
// back into the instance as the API grows.
type Context struct {
	// Runtime owns Instance and the current call.
	Runtime *Runtime

	// Instance is the WebAssembly module instance that invoked the host function.
	Instance *ModuleInstance
}

// Runtime owns instantiated modules and runtime-wide state.
//
// A Runtime is created with NewRuntime.
type Runtime struct{}

// NewRuntime returns a new empty Runtime.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// Instantiate instantiates m with the supplied imports.
//
// m must already be validated before it is passed to Instantiate. In
// particular, modules produced from WAT should be validated using the hints
// produced by WAT parsing before reaching this runtime API.
//
// imports supplies host functions needed by m's import section; pass nil when
// the module has no imports. On success, Instantiate returns a ModuleInstance
// whose exported functions can be obtained with ModuleInstance.ExportedFunc. It
// returns an error when an import is missing, an import has the wrong type, or
// the module uses an import/export/instruction kind this minimal runtime does
// not support yet.
func (rt *Runtime) Instantiate(m *wasmir.Module, imports Imports) (*ModuleInstance, error) {
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}

	inst := &ModuleInstance{
		rt:      rt,
		m:       m,
		exports: make(map[string]*Func),
	}
	if err := inst.buildFuncs(imports); err != nil {
		return nil, err
	}
	for _, exp := range m.Exports {
		if exp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if int(exp.Index) >= len(inst.funcs) {
			return nil, fmt.Errorf("export %q: function index %d out of range", exp.Name, exp.Index)
		}
		inst.exports[exp.Name] = &Func{inst: inst, index: exp.Index}
	}
	return inst, nil
}

// ModuleInstance is one instantiated WebAssembly module.
//
// A ModuleInstance owns the module's function index space and exported
// functions. Values returned by ExportedFunc are bound to this instance.
type ModuleInstance struct {
	rt      *Runtime
	m       *wasmir.Module
	funcs   []funcInst
	exports map[string]*Func
}

// ExportedFunc returns the exported function with the given name.
//
// The returned boolean is false when name is not exported as a function. Other
// export kinds are ignored by this method. The returned Func is bound to this
// ModuleInstance and can be invoked with Func.Call.
func (inst *ModuleInstance) ExportedFunc(name string) (*Func, bool) {
	f, ok := inst.exports[name]
	return f, ok
}

// Func is a callable WebAssembly function exported from a ModuleInstance.
//
// A Func is obtained with ModuleInstance.ExportedFunc. Calls validate argument
// count and value types against the function's WebAssembly signature.
type Func struct {
	inst  *ModuleInstance
	index uint32
}

// Call invokes f with WebAssembly runtime values.
//
// args must contain one Value per function parameter, in parameter order. On
// success, Call returns one Value per function result, in result order. It
// returns an error when the argument count or types are wrong, when a host
// callback returns an error, or when execution traps in the currently supported
// instruction subset.
func (f *Func) Call(args ...Value) ([]Value, error) {
	return f.inst.callFunc(f.index, args)
}

type funcInst struct {
	typeIdx uint32
	host    *HostFunc
	code    *compiledFunc
}

// buildFuncs creates the instance function address space.
//
// WebAssembly numbers imported functions before module-defined functions, so
// the order here has to match the function index space used by exports and
// call instructions.
func (inst *ModuleInstance) buildFuncs(imports Imports) error {
	for _, imp := range inst.m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			return fmt.Errorf("unsupported import %q.%q kind %d", imp.Module, imp.Name, imp.Kind)
		}
		host, err := resolveHostFunc(imports, imp)
		if err != nil {
			return err
		}
		if err := inst.checkHostFuncType(imp, host); err != nil {
			return err
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: imp.TypeIdx, host: &host})
	}
	for i := range inst.m.Funcs {
		f := &inst.m.Funcs[i]
		code, err := compileFunc(f)
		if err != nil {
			return fmt.Errorf("func[%d]: %w", len(inst.funcs), err)
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: f.TypeIdx, code: code})
	}
	return nil
}

// resolveHostFunc finds the Go callback supplied for a function import.
func resolveHostFunc(imports Imports, imp wasmir.Import) (HostFunc, error) {
	fields, ok := imports[imp.Module]
	if !ok {
		return HostFunc{}, fmt.Errorf("missing import module %q", imp.Module)
	}
	ext, ok := fields[imp.Name]
	if !ok {
		return HostFunc{}, fmt.Errorf("missing import %q.%q", imp.Module, imp.Name)
	}
	switch host := ext.(type) {
	case HostFunc:
		return host, nil
	case *HostFunc:
		if host == nil {
			return HostFunc{}, fmt.Errorf("import %q.%q is nil", imp.Module, imp.Name)
		}
		return *host, nil
	default:
		return HostFunc{}, fmt.Errorf("import %q.%q is not a function", imp.Module, imp.Name)
	}
}

// checkHostFuncType checks that a supplied host function matches the module's
// declared import type.
func (inst *ModuleInstance) checkHostFuncType(imp wasmir.Import, host HostFunc) error {
	if int(imp.TypeIdx) >= len(inst.m.Types) || inst.m.Types[imp.TypeIdx].Kind != wasmir.TypeDefKindFunc {
		return fmt.Errorf("import %q.%q has invalid function type", imp.Module, imp.Name)
	}
	ft := inst.m.Types[imp.TypeIdx]
	if !slices.Equal(host.Params, ft.Params) || !slices.Equal(host.Results, ft.Results) {
		return fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
	}
	if host.Func == nil {
		return fmt.Errorf("import %q.%q has nil function", imp.Module, imp.Name)
	}
	return nil
}

// callFunc dispatches a function-index call to either a host function or a
// module-defined function after checking the call arguments.
func (inst *ModuleInstance) callFunc(index uint32, args []Value) ([]Value, error) {
	if int(index) >= len(inst.funcs) {
		return nil, fmt.Errorf("function index %d out of range", index)
	}
	fn := inst.funcs[index]
	ft, err := inst.funcType(fn.typeIdx)
	if err != nil {
		return nil, err
	}
	if err := checkArgs(ft.Params, args); err != nil {
		return nil, fmt.Errorf("func[%d]: %w", index, err)
	}
	if fn.host != nil {
		results, err := fn.host.Func(&Context{Runtime: inst.rt, Instance: inst}, args)
		if err != nil {
			return nil, err
		}
		if err := checkResults(ft.Results, results); err != nil {
			return nil, fmt.Errorf("func[%d]: %w", index, err)
		}
		return results, nil
	}
	return inst.callDefined(fn, ft, args)
}

// funcType returns the function type referenced by typeIdx.
func (inst *ModuleInstance) funcType(typeIdx uint32) (wasmir.TypeDef, error) {
	if int(typeIdx) >= len(inst.m.Types) || inst.m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d is not a function type", typeIdx)
	}
	return inst.m.Types[typeIdx], nil
}

// checkArgs verifies call argument count and value types.
func checkArgs(params []wasmir.ValueType, args []Value) error {
	if len(args) != len(params) {
		return fmt.Errorf("got %d arguments, want %d", len(args), len(params))
	}
	for i, want := range params {
		if args[i].Type != want {
			return fmt.Errorf("argument %d has type %s, want %s", i, args[i].Type, want)
		}
	}
	return nil
}

// checkResults verifies result count and value types.
func checkResults(want []wasmir.ValueType, got []Value) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Type != want[i] {
			return fmt.Errorf("result %d has type %s, want %s", i, got[i].Type, want[i])
		}
	}
	return nil
}

// compiledFunc is the VM's execution form for a module-defined function.
//
// It is intentionally separate from wasmir.Function: wasmir is the semantic
// interchange representation, while compiledFunc stores runtime-oriented
// immediates such as resolved branch targets.
type compiledFunc struct {
	locals []wasmir.ValueType
	code   []vmInstr
}

type vmInstr struct {
	// Kind is the semantic instruction kind executed by the interpreter.
	Kind wasmir.InstrKind

	// Target is the resolved program counter for control-flow instructions.
	// It is used by if, else, br, and br_if; other instructions leave it at -1.
	Target int

	// Index is the resolved index immediate for local and function instructions.
	Index uint32

	// I32 is the immediate payload for i32.const.
	I32 int32

	// I64 is the immediate payload for i64.const.
	I64 int64

	// F32 is the raw IEEE-754 bit pattern immediate for f32.const.
	F32 uint32

	// F64 is the raw IEEE-754 bit pattern immediate for f64.const.
	F64 uint64
}

func compileFunc(fn *wasmir.Function) (*compiledFunc, error) {
	ctrl, err := analyzeControl(fn.Body)
	if err != nil {
		return nil, err
	}

	out := &compiledFunc{
		locals: slices.Clone(fn.Locals),
		code:   make([]vmInstr, len(fn.Body)),
	}
	labelStack := make([]int, 0)

	for pc, ins := range fn.Body {
		op := vmInstr{Kind: ins.Kind, Target: -1}
		switch ins.Kind {
		case wasmir.InstrBlock:
			if _, ok := ctrl.labels[pc]; !ok {
				return nil, fmt.Errorf("block at %d has no matching end", pc)
			}
			labelStack = append(labelStack, pc)
		case wasmir.InstrIf:
			label, ok := ctrl.labels[pc]
			if !ok {
				return nil, fmt.Errorf("if at %d has no matching end", pc)
			}
			if label.elseIndex >= 0 {
				op.Target = label.elseIndex
			} else {
				op.Target = label.endIndex
			}
			labelStack = append(labelStack, pc)
		case wasmir.InstrElse:
			if len(labelStack) == 0 {
				return nil, fmt.Errorf("else at %d without active label", pc)
			}
			start := labelStack[len(labelStack)-1]
			label := ctrl.labels[start]
			if label.elseIndex != pc {
				return nil, fmt.Errorf("else at %d does not match active label", pc)
			}
			op.Target = label.endIndex
		case wasmir.InstrLocalGet, wasmir.InstrLocalSet, wasmir.InstrLocalTee:
			op.Index = ins.LocalIndex
		case wasmir.InstrCall:
			op.Index = ins.FuncIndex
		case wasmir.InstrBr, wasmir.InstrBrIf:
			target, err := compileBranchTarget(ins.BranchDepth, labelStack, ctrl)
			if err != nil {
				return nil, fmt.Errorf("%s at %d: %w", instrName(ins.Kind), pc, err)
			}
			op.Target = target
		case wasmir.InstrI32Const:
			op.I32 = ins.I32Const
		case wasmir.InstrI64Const:
			op.I64 = ins.I64Const
		case wasmir.InstrF32Const:
			op.F32 = ins.F32Const
		case wasmir.InstrF64Const:
			op.F64 = ins.F64Const
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul,
			wasmir.InstrI32Eq, wasmir.InstrI32Ne,
			wasmir.InstrI32LtS, wasmir.InstrI32LeS, wasmir.InstrI32GtS, wasmir.InstrI32GeS,
			wasmir.InstrI32Eqz,
			wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul,
			wasmir.InstrI64Eq, wasmir.InstrI64Ne,
			wasmir.InstrI64LtS, wasmir.InstrI64LeS, wasmir.InstrI64GtS, wasmir.InstrI64GeS,
			wasmir.InstrI64Eqz,
			wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div,
			wasmir.InstrF32Eq, wasmir.InstrF32Ne,
			wasmir.InstrF32Lt, wasmir.InstrF32Le, wasmir.InstrF32Gt, wasmir.InstrF32Ge,
			wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div,
			wasmir.InstrF64Eq, wasmir.InstrF64Ne,
			wasmir.InstrF64Lt, wasmir.InstrF64Le, wasmir.InstrF64Gt, wasmir.InstrF64Ge,
			wasmir.InstrDrop, wasmir.InstrReturn:
		case wasmir.InstrEnd:
			if len(labelStack) == 0 {
				if pc != len(fn.Body)-1 {
					return nil, fmt.Errorf("end without active label")
				}
			} else {
				start := labelStack[len(labelStack)-1]
				label := ctrl.labels[start]
				if label.endIndex == pc {
					labelStack = labelStack[:len(labelStack)-1]
				}
			}
		default:
			return nil, fmt.Errorf("unsupported instruction %s", instrName(ins.Kind))
		}
		out.code[pc] = op
	}

	if len(labelStack) != 0 {
		start := labelStack[len(labelStack)-1]
		return nil, fmt.Errorf("%s at %d without matching end", instrName(fn.Body[start].Kind), start)
	}
	return out, nil
}

func compileBranchTarget(depth uint32, labelStack []int, ctrl controlInfo) (int, error) {
	if int(depth) >= len(labelStack) {
		return 0, fmt.Errorf("branch depth %d out of range", depth)
	}
	start := labelStack[len(labelStack)-1-int(depth)]
	label, ok := ctrl.labels[start]
	if !ok {
		return 0, fmt.Errorf("branch target at %d has no matching end", start)
	}
	return label.endIndex, nil
}

// callDefined interprets one compiled module-defined function body.
//
// This is deliberately minimal for now: it initializes locals, maintains a
// single operand stack, and executes only the small instruction subset needed
// by the first wasmvm tests.
func (inst *ModuleInstance) callDefined(fn funcInst, ft wasmir.TypeDef, args []Value) ([]Value, error) {
	if fn.code == nil {
		return nil, fmt.Errorf("defined function has no compiled code")
	}

	locals := slices.Clone(args)
	for _, vt := range fn.code.locals {
		v, err := zeroValue(vt)
		if err != nil {
			return nil, err
		}
		locals = append(locals, v)
	}

	stack := make([]Value, 0)
	pop := func() (Value, error) {
		if len(stack) == 0 {
			return Value{}, fmt.Errorf("operand stack underflow")
		}
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		return v, nil
	}

	for pc := 0; pc < len(fn.code.code); pc++ {
		ins := fn.code.code[pc]
		switch ins.Kind {
		case wasmir.InstrBlock:
		case wasmir.InstrIf:
			// The condition has already been validated as i32. A true condition
			// enters the then arm. A false condition skips to the else marker if
			// present, or to the matching end otherwise.
			cond, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			if cond == 0 {
				pc = ins.Target
				continue
			}
		case wasmir.InstrElse:
			// Reaching else normally means the then arm completed without
			// branching. Skip the else arm.
			pc = ins.Target
		case wasmir.InstrLocalGet:
			if int(ins.Index) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.Index)
			}
			stack = append(stack, locals[ins.Index])
		case wasmir.InstrLocalSet:
			if int(ins.Index) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.Index)
			}
			v, err := pop()
			if err != nil {
				return nil, err
			}
			if v.Type != locals[ins.Index].Type {
				return nil, fmt.Errorf("local.set %d got %s, want %s", ins.Index, v.Type, locals[ins.Index].Type)
			}
			locals[ins.Index] = v
		case wasmir.InstrLocalTee:
			if int(ins.Index) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.Index)
			}
			v, err := pop()
			if err != nil {
				return nil, err
			}
			if v.Type != locals[ins.Index].Type {
				return nil, fmt.Errorf("local.tee %d got %s, want %s", ins.Index, v.Type, locals[ins.Index].Type)
			}
			locals[ins.Index] = v
			stack = append(stack, v)
		case wasmir.InstrI32Const:
			stack = append(stack, I32(ins.I32))
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul,
			wasmir.InstrI32Eq, wasmir.InstrI32Ne,
			wasmir.InstrI32LtS, wasmir.InstrI32LeS, wasmir.InstrI32GtS, wasmir.InstrI32GeS:
			v, err := evalI32Binary(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(v))
		case wasmir.InstrI32Eqz:
			v, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(boolI32(v == 0)))
		case wasmir.InstrI64Const:
			stack = append(stack, I64(ins.I64))
		case wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul:
			v, err := evalI64Binary(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I64(v))
		case wasmir.InstrI64Eq, wasmir.InstrI64Ne,
			wasmir.InstrI64LtS, wasmir.InstrI64LeS, wasmir.InstrI64GtS, wasmir.InstrI64GeS:
			v, err := evalI64Compare(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(v))
		case wasmir.InstrI64Eqz:
			v, err := popI64(pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(boolI32(v == 0)))
		case wasmir.InstrF32Const:
			stack = append(stack, F32(math.Float32frombits(ins.F32)))
		case wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div:
			v, err := evalF32Binary(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, F32(v))
		case wasmir.InstrF32Eq, wasmir.InstrF32Ne,
			wasmir.InstrF32Lt, wasmir.InstrF32Le, wasmir.InstrF32Gt, wasmir.InstrF32Ge:
			v, err := evalF32Compare(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(v))
		case wasmir.InstrF64Const:
			stack = append(stack, F64(math.Float64frombits(ins.F64)))
		case wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div:
			v, err := evalF64Binary(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, F64(v))
		case wasmir.InstrF64Eq, wasmir.InstrF64Ne,
			wasmir.InstrF64Lt, wasmir.InstrF64Le, wasmir.InstrF64Gt, wasmir.InstrF64Ge:
			v, err := evalF64Compare(ins.Kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(v))
		case wasmir.InstrDrop:
			if _, err := pop(); err != nil {
				return nil, err
			}
		case wasmir.InstrCall:
			if int(ins.Index) >= len(inst.funcs) {
				return nil, fmt.Errorf("call function index %d out of range", ins.Index)
			}
			calleeType, err := inst.funcType(inst.funcs[ins.Index].typeIdx)
			if err != nil {
				return nil, err
			}
			callArgs, err := popArgs(&stack, calleeType.Params)
			if err != nil {
				return nil, err
			}
			results, err := inst.callFunc(ins.Index, callArgs)
			if err != nil {
				return nil, err
			}
			stack = append(stack, results...)
		case wasmir.InstrBr:
			pc = ins.Target
		case wasmir.InstrBrIf:
			// br_if consumes only the condition. Any branch result values are
			// already below it on the operand stack and are left there for the
			// target block's end to consume.
			cond, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			if cond != 0 {
				pc = ins.Target
			}
		case wasmir.InstrReturn:
			return popResults(&stack, ft.Results)
		case wasmir.InstrEnd:
			if pc != len(fn.code.code)-1 {
				// Non-final end closes structured control. Branch targets skip over
				// it, and ordinary fallthrough can treat it as a no-op because
				// validation has already established the operand stack contract.
				continue
			}
			return popResults(&stack, ft.Results)
		default:
			return nil, fmt.Errorf("unsupported instruction %s", instrName(ins.Kind))
		}
	}
	return nil, fmt.Errorf("function ended without end")
}

// controlLabel describes one structured-control label in the flattened
// instruction stream.
//
// endIndex is the matching end instruction and is currently the branch target
// for both block and if labels. elseIndex is the matching else instruction for
// if labels, or -1 when the label has no else arm.
type controlLabel struct {
	endIndex  int
	elseIndex int
}

// controlInfo stores precomputed control-boundary metadata by opening
// instruction index. Only block and if instructions have entries.
type controlInfo struct {
	labels map[int]controlLabel
}

// analyzeControl records matching structured-control boundaries in body.
//
// The VM assumes modules were validated before instantiation, but it still
// receives a plain wasmir.Module and should not rely on nested source syntax.
// This pass treats block/if as openers, else as metadata on the current if, and
// end as the closer for the innermost opener. End instructions with no opener
// are accepted here because the final function end is represented the same way
// as a structured-control end.
func analyzeControl(body []wasmir.Instruction) (controlInfo, error) {
	ctrl := controlInfo{labels: make(map[int]controlLabel)}
	stack := make([]int, 0)
	elseIndex := make(map[int]int)

	for pc, ins := range body {
		switch ins.Kind {
		case wasmir.InstrBlock, wasmir.InstrIf:
			stack = append(stack, pc)
		case wasmir.InstrElse:
			if len(stack) == 0 {
				return controlInfo{}, fmt.Errorf("else at %d without matching if", pc)
			}
			start := stack[len(stack)-1]
			if body[start].Kind != wasmir.InstrIf {
				return controlInfo{}, fmt.Errorf("else at %d matched non-if instruction %s", pc, instrName(body[start].Kind))
			}
			elseIndex[start] = pc
		case wasmir.InstrEnd:
			if len(stack) == 0 {
				continue
			}
			start := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			label := controlLabel{
				endIndex:  pc,
				elseIndex: -1,
			}
			if elsePC, ok := elseIndex[start]; ok {
				label.elseIndex = elsePC
			}
			ctrl.labels[start] = label
		}
	}
	if len(stack) != 0 {
		return controlInfo{}, fmt.Errorf("%s at %d without matching end", instrName(body[stack[len(stack)-1]].Kind), stack[len(stack)-1])
	}
	return ctrl, nil
}

// zeroValue constructs the default local value for a numeric value type.
func zeroValue(vt wasmir.ValueType) (Value, error) {
	switch vt {
	case wasmir.ValueTypeI32:
		return I32(0), nil
	case wasmir.ValueTypeI64:
		return I64(0), nil
	case wasmir.ValueTypeF32:
		return F32(0), nil
	case wasmir.ValueTypeF64:
		return F64(0), nil
	default:
		return Value{}, fmt.Errorf("unsupported local type %s", vt)
	}
}

// evalI64Binary pops two i64 operands and applies a supported i64 binary op.
func evalI64Binary(kind wasmir.InstrKind, pop func() (Value, error)) (int64, error) {
	rhs, err := popI64(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popI64(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrI64Add:
		return lhs + rhs, nil
	case wasmir.InstrI64Sub:
		return lhs - rhs, nil
	case wasmir.InstrI64Mul:
		return lhs * rhs, nil
	default:
		return 0, fmt.Errorf("unsupported i64 binary instruction %s", instrName(kind))
	}
}

// evalI64Compare pops two i64 operands and applies a supported i64 comparison.
func evalI64Compare(kind wasmir.InstrKind, pop func() (Value, error)) (int32, error) {
	rhs, err := popI64(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popI64(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrI64Eq:
		return boolI32(lhs == rhs), nil
	case wasmir.InstrI64Ne:
		return boolI32(lhs != rhs), nil
	case wasmir.InstrI64LtS:
		return boolI32(lhs < rhs), nil
	case wasmir.InstrI64LeS:
		return boolI32(lhs <= rhs), nil
	case wasmir.InstrI64GtS:
		return boolI32(lhs > rhs), nil
	case wasmir.InstrI64GeS:
		return boolI32(lhs >= rhs), nil
	default:
		return 0, fmt.Errorf("unsupported i64 comparison instruction %s", instrName(kind))
	}
}

// evalF32Binary pops two f32 operands and applies a supported f32 binary op.
func evalF32Binary(kind wasmir.InstrKind, pop func() (Value, error)) (float32, error) {
	rhs, err := popF32(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popF32(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrF32Add:
		return lhs + rhs, nil
	case wasmir.InstrF32Sub:
		return lhs - rhs, nil
	case wasmir.InstrF32Mul:
		return lhs * rhs, nil
	case wasmir.InstrF32Div:
		return lhs / rhs, nil
	default:
		return 0, fmt.Errorf("unsupported f32 binary instruction %s", instrName(kind))
	}
}

// evalF32Compare pops two f32 operands and applies a supported f32 comparison.
func evalF32Compare(kind wasmir.InstrKind, pop func() (Value, error)) (int32, error) {
	rhs, err := popF32(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popF32(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrF32Eq:
		return boolI32(lhs == rhs), nil
	case wasmir.InstrF32Ne:
		return boolI32(lhs != rhs), nil
	case wasmir.InstrF32Lt:
		return boolI32(lhs < rhs), nil
	case wasmir.InstrF32Le:
		return boolI32(lhs <= rhs), nil
	case wasmir.InstrF32Gt:
		return boolI32(lhs > rhs), nil
	case wasmir.InstrF32Ge:
		return boolI32(lhs >= rhs), nil
	default:
		return 0, fmt.Errorf("unsupported f32 comparison instruction %s", instrName(kind))
	}
}

// evalF64Binary pops two f64 operands and applies a supported f64 binary op.
func evalF64Binary(kind wasmir.InstrKind, pop func() (Value, error)) (float64, error) {
	rhs, err := popF64(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popF64(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrF64Add:
		return lhs + rhs, nil
	case wasmir.InstrF64Sub:
		return lhs - rhs, nil
	case wasmir.InstrF64Mul:
		return lhs * rhs, nil
	case wasmir.InstrF64Div:
		return lhs / rhs, nil
	default:
		return 0, fmt.Errorf("unsupported f64 binary instruction %s", instrName(kind))
	}
}

// evalF64Compare pops two f64 operands and applies a supported f64 comparison.
func evalF64Compare(kind wasmir.InstrKind, pop func() (Value, error)) (int32, error) {
	rhs, err := popF64(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popF64(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrF64Eq:
		return boolI32(lhs == rhs), nil
	case wasmir.InstrF64Ne:
		return boolI32(lhs != rhs), nil
	case wasmir.InstrF64Lt:
		return boolI32(lhs < rhs), nil
	case wasmir.InstrF64Le:
		return boolI32(lhs <= rhs), nil
	case wasmir.InstrF64Gt:
		return boolI32(lhs > rhs), nil
	case wasmir.InstrF64Ge:
		return boolI32(lhs >= rhs), nil
	default:
		return 0, fmt.Errorf("unsupported f64 comparison instruction %s", instrName(kind))
	}
}

// evalI32Binary pops two i32 operands and applies a supported i32 binary op.
func evalI32Binary(kind wasmir.InstrKind, pop func() (Value, error)) (int32, error) {
	rhs, err := popI32(pop)
	if err != nil {
		return 0, err
	}
	lhs, err := popI32(pop)
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrI32Add:
		return lhs + rhs, nil
	case wasmir.InstrI32Sub:
		return lhs - rhs, nil
	case wasmir.InstrI32Mul:
		return lhs * rhs, nil
	case wasmir.InstrI32Eq:
		return boolI32(lhs == rhs), nil
	case wasmir.InstrI32Ne:
		return boolI32(lhs != rhs), nil
	case wasmir.InstrI32LtS:
		return boolI32(lhs < rhs), nil
	case wasmir.InstrI32LeS:
		return boolI32(lhs <= rhs), nil
	case wasmir.InstrI32GtS:
		return boolI32(lhs > rhs), nil
	case wasmir.InstrI32GeS:
		return boolI32(lhs >= rhs), nil
	default:
		return 0, fmt.Errorf("unsupported i32 binary instruction %s", instrName(kind))
	}
}

// boolI32 converts a WebAssembly i32 condition result to 0 or 1.
func boolI32(v bool) int32 {
	if v {
		return 1
	}
	return 0
}

// popI32 pops and type-checks an i32 operand.
func popI32(pop func() (Value, error)) (int32, error) {
	v, err := pop()
	if err != nil {
		return 0, err
	}
	if v.Type != wasmir.ValueTypeI32 {
		return 0, fmt.Errorf("got %s operand, want i32", v.Type)
	}
	return v.I32, nil
}

// popI64 pops and type-checks an i64 operand.
func popI64(pop func() (Value, error)) (int64, error) {
	v, err := pop()
	if err != nil {
		return 0, err
	}
	if v.Type != wasmir.ValueTypeI64 {
		return 0, fmt.Errorf("got %s operand, want i64", v.Type)
	}
	return v.I64, nil
}

// popF32 pops and type-checks an f32 operand.
func popF32(pop func() (Value, error)) (float32, error) {
	v, err := pop()
	if err != nil {
		return 0, err
	}
	if v.Type != wasmir.ValueTypeF32 {
		return 0, fmt.Errorf("got %s operand, want f32", v.Type)
	}
	return v.F32, nil
}

// popF64 pops and type-checks an f64 operand.
func popF64(pop func() (Value, error)) (float64, error) {
	v, err := pop()
	if err != nil {
		return 0, err
	}
	if v.Type != wasmir.ValueTypeF64 {
		return 0, fmt.Errorf("got %s operand, want f64", v.Type)
	}
	return v.F64, nil
}

// popArgs removes call arguments from the operand stack in parameter order.
func popArgs(stack *[]Value, params []wasmir.ValueType) ([]Value, error) {
	if len(*stack) < len(params) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(*stack) - len(params)
	args := (*stack)[base:]
	*stack = (*stack)[:base]
	if err := checkArgs(params, args); err != nil {
		return nil, err
	}
	return args, nil
}

// popResults removes function results from the operand stack in result order.
func popResults(stack *[]Value, results []wasmir.ValueType) ([]Value, error) {
	if len(*stack) < len(results) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(*stack) - len(results)
	out := (*stack)[base:]
	*stack = (*stack)[:base]
	if err := checkResults(results, out); err != nil {
		return nil, err
	}
	return out, nil
}

// instrName formats instruction kinds for current interpreter errors.
func instrName(kind wasmir.InstrKind) string {
	if def, ok := instrdef.LookupInstructionByKind(kind); ok {
		return def.TextName
	}
	return fmt.Sprintf("instruction(%d)", kind)
}
