// Package vm implements watgo's minimal WebAssembly interpreter runtime.
//
// This internal package holds the implementation used by the public wasmvm
// facade. It intentionally starts small: function instantiation, exported
// function lookup, host function imports, and a tiny instruction subset.
package vm

import (
	"fmt"

	"github.com/eliben/watgo/internal/validate"
	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value.
type Value struct {
	Type wasmir.ValueType
	I32  int32
	I64  int64
	F32  float32
	F64  float64
}

// I32 constructs an i32 runtime value.
func I32(v int32) Value {
	return Value{Type: wasmir.ValueTypeI32, I32: v}
}

// I64 constructs an i64 runtime value.
func I64(v int64) Value {
	return Value{Type: wasmir.ValueTypeI64, I64: v}
}

// F32 constructs an f32 runtime value.
func F32(v float32) Value {
	return Value{Type: wasmir.ValueTypeF32, F32: v}
}

// F64 constructs an f64 runtime value.
func F64(v float64) Value {
	return Value{Type: wasmir.ValueTypeF64, F64: v}
}

// Imports maps import module names and field names to runtime externs.
type Imports map[string]map[string]Extern

// Extern is one runtime object supplied for a module import.
type Extern interface {
	isExtern()
}

// HostFunc is a Go function exposed as a WebAssembly function import.
type HostFunc struct {
	Params  []wasmir.ValueType
	Results []wasmir.ValueType
	Func    func(ctx *Context, args []Value) ([]Value, error)
}

// isExtern marks HostFunc as a valid import object.
func (HostFunc) isExtern() {}

// NewHostFunc constructs a function import from a Go callback.
func NewHostFunc(params, results []wasmir.ValueType, fn func(ctx *Context, args []Value) ([]Value, error)) HostFunc {
	return HostFunc{Params: params, Results: results, Func: fn}
}

// Context is passed to host functions.
type Context struct {
	Runtime  *Runtime
	Instance *ModuleInstance
}

// Runtime owns instantiated modules and host/runtime state.
type Runtime struct{}

// NewRuntime constructs an empty runtime.
func NewRuntime() *Runtime {
	return &Runtime{}
}

// Instantiate validates and instantiates m with the supplied imports.
func (rt *Runtime) Instantiate(m *wasmir.Module, imports Imports) (*ModuleInstance, error) {
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}
	if err := validate.ValidateModule(m, nil); err != nil {
		return nil, err
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
type ModuleInstance struct {
	rt      *Runtime
	m       *wasmir.Module
	funcs   []funcInst
	exports map[string]*Func
}

// ExportedFunc returns the exported function with name.
func (inst *ModuleInstance) ExportedFunc(name string) (*Func, bool) {
	f, ok := inst.exports[name]
	return f, ok
}

// Func is a callable WebAssembly or host function.
type Func struct {
	inst  *ModuleInstance
	index uint32
}

// Call invokes f with WebAssembly runtime values.
func (f *Func) Call(args ...Value) ([]Value, error) {
	if f == nil || f.inst == nil {
		return nil, fmt.Errorf("function is nil")
	}
	return f.inst.callFunc(f.index, args)
}

type funcInst struct {
	typeIdx uint32
	host    *HostFunc
	def     *wasmir.Function
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
		inst.funcs = append(inst.funcs, funcInst{typeIdx: f.TypeIdx, def: f})
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
	if !sameValueTypes(host.Params, ft.Params) || !sameValueTypes(host.Results, ft.Results) {
		return fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
	}
	if host.Func == nil {
		return fmt.Errorf("import %q.%q has nil function", imp.Module, imp.Name)
	}
	return nil
}

// sameValueTypes reports whether two value-type lists are exactly equal.
func sameValueTypes(a, b []wasmir.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		results, err := fn.host.Func(&Context{Runtime: inst.rt, Instance: inst}, cloneValues(args))
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

// cloneValues copies a value slice before handing it to host code or returning
// stack slices to callers.
func cloneValues(values []Value) []Value {
	out := make([]Value, len(values))
	copy(out, values)
	return out
}

// callDefined interprets one module-defined function body.
//
// This is deliberately minimal for now: it initializes locals, maintains a
// single operand stack, and executes only the small instruction subset needed
// by the first wasmvm tests.
func (inst *ModuleInstance) callDefined(fn funcInst, ft wasmir.TypeDef, args []Value) ([]Value, error) {
	locals := cloneValues(args)
	for _, vt := range fn.def.Locals {
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

	for pc := 0; pc < len(fn.def.Body); pc++ {
		ins := fn.def.Body[pc]
		switch ins.Kind {
		case wasmir.InstrLocalGet:
			if int(ins.LocalIndex) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.LocalIndex)
			}
			stack = append(stack, locals[ins.LocalIndex])
		case wasmir.InstrI32Const:
			stack = append(stack, I32(ins.I32Const))
		case wasmir.InstrI32Add:
			rhs, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			lhs, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, I32(lhs+rhs))
		case wasmir.InstrCall:
			if int(ins.FuncIndex) >= len(inst.funcs) {
				return nil, fmt.Errorf("call function index %d out of range", ins.FuncIndex)
			}
			calleeType, err := inst.funcType(inst.funcs[ins.FuncIndex].typeIdx)
			if err != nil {
				return nil, err
			}
			callArgs, err := popArgs(&stack, calleeType.Params)
			if err != nil {
				return nil, err
			}
			results, err := inst.callFunc(ins.FuncIndex, callArgs)
			if err != nil {
				return nil, err
			}
			stack = append(stack, results...)
		case wasmir.InstrReturn:
			return popResults(&stack, ft.Results)
		case wasmir.InstrEnd:
			return popResults(&stack, ft.Results)
		default:
			return nil, fmt.Errorf("unsupported instruction %s", instrName(ins.Kind))
		}
	}
	return nil, fmt.Errorf("function ended without end")
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

// popArgs removes call arguments from the operand stack in parameter order.
func popArgs(stack *[]Value, params []wasmir.ValueType) ([]Value, error) {
	if len(*stack) < len(params) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(*stack) - len(params)
	args := cloneValues((*stack)[base:])
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
	out := cloneValues((*stack)[base:])
	*stack = (*stack)[:base]
	if err := checkResults(results, out); err != nil {
		return nil, err
	}
	return out, nil
}

// instrName formats instruction kinds for current interpreter errors.
func instrName(kind wasmir.InstrKind) string {
	switch kind {
	case wasmir.InstrLocalGet:
		return "local.get"
	case wasmir.InstrI32Const:
		return "i32.const"
	case wasmir.InstrI32Add:
		return "i32.add"
	case wasmir.InstrCall:
		return "call"
	case wasmir.InstrReturn:
		return "return"
	case wasmir.InstrEnd:
		return "end"
	default:
		return fmt.Sprintf("instruction(%d)", kind)
	}
}
