// Package wasmvm exposes a minimal WebAssembly interpreter runtime for wasmir
// modules.
//
// The package can instantiate an already-validated wasmir.Module, look up
// exported functions, call them with runtime values, and satisfy WebAssembly
// function imports with Go callbacks.
package wasmvm

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/eliben/watgo/internal/vm"
	"github.com/eliben/watgo/wasmir"
)

const wasmPageSize = 64 * 1024

// Value is one runtime WebAssembly value passed into or returned from a Func.
//
// Type identifies which payload field is meaningful. For example, values with
// Type set to wasmir.ValueTypeI32 use I32, while values with Type set to
// wasmir.ValueTypeF64 use F64. Prefer the I32, I64, F32, and F64 constructors
// over constructing Value directly.
type Value = vm.Value

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
	if err := inst.buildMemories(); err != nil {
		return nil, err
	}
	inst.buildDataSegments()
	if err := inst.buildFuncs(imports); err != nil {
		return nil, err
	}
	if err := inst.buildGlobals(); err != nil {
		return nil, err
	}
	if err := inst.buildTables(); err != nil {
		return nil, err
	}
	if err := inst.applyDataSegments(); err != nil {
		return nil, err
	}
	if err := inst.applyElementSegments(); err != nil {
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
	rt       *Runtime
	m        *wasmir.Module
	funcs    []funcInst
	globals  []globalInst
	memories []memoryInst
	tables   []tableInst
	data     []dataInst
	exports  map[string]*Func
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
	// typeIdx indexes inst.m.Types and describes both host and module-defined
	// functions in the unified function index space.
	typeIdx uint32

	// host is non-nil for imported host functions. Such functions are executed
	// directly by callFunc rather than through internal/vm.
	host *HostFunc

	// code is non-nil for module-defined functions. It is compiled once during
	// instantiation from wasmir.Function into internal/vm's execution form.
	code *vm.Function
}

// globalInst is one instantiated global in the module's global index space.
//
// It stores runtime state: mutable globals update value through global.set,
// while immutable globals keep the value computed from their initializer for
// the lifetime of the instance.
type globalInst struct {
	// typ is the validated value type of value. It is kept here so global.set
	// can check writes without looking back into the source module.
	typ wasmir.ValueType

	// mutable records whether global.set is allowed to update value.
	mutable bool

	// value is the current runtime value stored in this global.
	value Value
}

// memoryInst is one instantiated linear memory in the module's memory index
// space.
//
// wasmvm owns the backing bytes; internal/vm reaches them only through
// vm.Resolver memory methods so execution stays independent of instance
// layout.
type memoryInst struct {
	// addressType is the validated address type for this memory. wasmvm
	// currently supports only i32-addressed memories, but this preserves the
	// declaration needed for future memory64 support.
	addressType wasmir.ValueType

	// max is the optional declared maximum size in WebAssembly pages.
	max *uint64

	// data is the mutable linear-memory byte buffer. Its length is always a
	// whole number of WebAssembly pages.
	data []byte
}

// tableInst is one instantiated table in the module's table index space.
//
// wasmvm owns the reference slots; internal/vm reaches them only through
// vm.Resolver table methods so execution stays independent of instance layout.
type tableInst struct {
	// addressType is the validated index type for this table. wasmvm currently
	// supports only i32-indexed tables.
	addressType wasmir.ValueType

	// refType is the reference type accepted by this table's elements.
	refType wasmir.ValueType

	// max is the optional declared maximum size in elements.
	max *uint64

	// elems is the mutable table storage.
	elems []Value
}

// dataInst is one instantiated data segment in the module's data index space.
type dataInst struct {
	// init is the byte payload used by memory.init while the segment is live.
	init []byte

	// dropped reports whether data.drop or active-segment initialization has
	// made this segment unavailable.
	dropped bool
}

// buildMemories creates the instance memory address space.
func (inst *ModuleInstance) buildMemories() error {
	for i, m := range inst.m.Memories {
		if m.ImportModule != "" || m.ImportName != "" {
			return fmt.Errorf("unsupported memory import %q.%q", m.ImportModule, m.ImportName)
		}
		if m.AddressType != wasmir.ValueTypeI32 {
			return fmt.Errorf("memory[%d]: unsupported address type %s", i, m.AddressType)
		}
		if m.Min > uint64(int(^uint(0)>>1))/wasmPageSize {
			return fmt.Errorf("memory[%d]: minimum size is too large", i)
		}
		size := int(m.Min * wasmPageSize)
		inst.memories = append(inst.memories, memoryInst{
			addressType: m.AddressType,
			max:         m.Max,
			data:        make([]byte, size),
		})
	}
	return nil
}

// buildTables creates the instance table address space.
func (inst *ModuleInstance) buildTables() error {
	for i, t := range inst.m.Tables {
		if t.ImportModule != "" || t.ImportName != "" {
			return fmt.Errorf("unsupported table import %q.%q", t.ImportModule, t.ImportName)
		}
		if t.AddressType != wasmir.ValueTypeI32 {
			return fmt.Errorf("table[%d]: unsupported address type %s", i, t.AddressType)
		}
		if t.Min > uint64(int(^uint(0)>>1)) {
			return fmt.Errorf("table[%d]: minimum size is too large", i)
		}
		init, err := inst.tableInitialValue(t)
		if err != nil {
			return fmt.Errorf("table[%d]: %w", i, err)
		}
		elems := make([]Value, int(t.Min))
		for j := range elems {
			elems[j] = init
		}
		inst.tables = append(inst.tables, tableInst{
			addressType: t.AddressType,
			refType:     t.RefType,
			max:         t.Max,
			elems:       elems,
		})
	}
	return nil
}

// tableInitialValue returns the value used to initialize every slot of table t.
func (inst *ModuleInstance) tableInitialValue(t wasmir.Table) (Value, error) {
	if len(t.Init) == 0 {
		if !t.RefType.Nullable {
			return Value{}, fmt.Errorf("non-nullable table requires initializer")
		}
		return Value{Type: t.RefType, Ref: vm.Reference{Kind: vm.RefKindNull}}, nil
	}
	value, err := vm.EvalConstExpr(t.Init, vmResolver{inst: inst, constExpr: true})
	if err != nil {
		return Value{}, err
	}
	if err := vm.CheckResults([]wasmir.ValueType{t.RefType}, []Value{value}); err != nil {
		return Value{}, fmt.Errorf("initializer type mismatch: %w", err)
	}
	return value, nil
}

// buildDataSegments creates the instance data segment address space.
func (inst *ModuleInstance) buildDataSegments() {
	for _, seg := range inst.m.Data {
		inst.data = append(inst.data, dataInst{init: slices.Clone(seg.Init)})
	}
}

// applyDataSegments copies active data segments into instantiated memories.
//
// Passive segments are retained only in the source module for now. They become
// observable when memory.init/data.drop are implemented; until then, ignoring
// them matches their instantiation-time behavior.
func (inst *ModuleInstance) applyDataSegments() error {
	for i, seg := range inst.m.Data {
		if seg.Mode == wasmir.DataSegmentModePassive {
			continue
		}
		offset, err := inst.dataSegmentOffset(seg)
		if err != nil {
			return fmt.Errorf("data[%d]: %w", i, err)
		}
		if uint64(len(seg.Init)) > uint64(^uint32(0)) {
			return fmt.Errorf("data[%d]: segment is too large", i)
		}
		dst, err := (vmResolver{inst: inst}).memory(seg.MemoryIndex, offset, uint64(len(seg.Init)))
		if err != nil {
			return fmt.Errorf("data[%d]: %w", i, err)
		}
		copy(dst, seg.Init)
		inst.data[i].dropped = true
	}
	return nil
}

// dataSegmentOffset evaluates the active data segment offset as an i32 memory
// address.
func (inst *ModuleInstance) dataSegmentOffset(seg wasmir.DataSegment) (uint64, error) {
	if len(seg.OffsetExpr) > 0 {
		v, err := vm.EvalConstExpr(seg.OffsetExpr, vmResolver{inst: inst, constExpr: true})
		if err != nil {
			return 0, err
		}
		if v.Type != wasmir.ValueTypeI32 {
			return 0, fmt.Errorf("offset expression has type %s, want i32", v.Type)
		}
		return uint64(uint32(v.I32)), nil
	}
	if seg.OffsetType != wasmir.ValueTypeI32 {
		return 0, fmt.Errorf("offset has type %s, want i32", seg.OffsetType)
	}
	return uint64(uint32(int32(seg.OffsetI64))), nil
}

// applyElementSegments copies active element segments into instantiated tables.
func (inst *ModuleInstance) applyElementSegments() error {
	for i, seg := range inst.m.Elements {
		if seg.Mode != wasmir.ElemSegmentModeActive {
			continue
		}
		offset, err := inst.elementSegmentOffset(seg)
		if err != nil {
			return fmt.Errorf("element[%d]: %w", i, err)
		}
		values, err := inst.elementSegmentValues(seg)
		if err != nil {
			return fmt.Errorf("element[%d]: %w", i, err)
		}
		if uint64(len(values)) > uint64(^uint32(0)) {
			return fmt.Errorf("element[%d]: segment is too large", i)
		}
		table, err := (vmResolver{inst: inst}).table(seg.TableIndex, offset, uint64(len(values)))
		if err != nil {
			return fmt.Errorf("element[%d]: %w", i, err)
		}
		copy(table, values)
	}
	return nil
}

// elementSegmentOffset evaluates the active element segment offset as an i32
// table index.
func (inst *ModuleInstance) elementSegmentOffset(seg wasmir.ElementSegment) (uint64, error) {
	if len(seg.OffsetExpr) > 0 {
		v, err := vm.EvalConstExpr(seg.OffsetExpr, vmResolver{inst: inst, constExpr: true})
		if err != nil {
			return 0, err
		}
		if v.Type != wasmir.ValueTypeI32 {
			return 0, fmt.Errorf("offset expression has type %s, want i32", v.Type)
		}
		return uint64(uint32(v.I32)), nil
	}
	if seg.OffsetType != wasmir.ValueTypeI32 {
		return 0, fmt.Errorf("offset has type %s, want i32", seg.OffsetType)
	}
	return uint64(uint32(int32(seg.OffsetI64))), nil
}

// elementSegmentValues evaluates the element payload into runtime references.
func (inst *ModuleInstance) elementSegmentValues(seg wasmir.ElementSegment) ([]Value, error) {
	if len(seg.FuncIndices) > 0 {
		values := make([]Value, len(seg.FuncIndices))
		for i, funcIndex := range seg.FuncIndices {
			if _, err := (vmResolver{inst: inst}).FuncType(funcIndex); err != nil {
				return nil, err
			}
			values[i] = Value{Type: wasmir.RefTypeFunc(false), Ref: vm.Reference{Kind: vm.RefKindFunc, FuncIndex: funcIndex}}
		}
		return values, nil
	}
	values := make([]Value, len(seg.Exprs))
	for i, expr := range seg.Exprs {
		v, err := vm.EvalConstExpr(expr, vmResolver{inst: inst, constExpr: true})
		if err != nil {
			return nil, fmt.Errorf("expr[%d]: %w", i, err)
		}
		if !v.Type.IsRef() {
			return nil, fmt.Errorf("expr[%d]: got %s, want reference", i, v.Type)
		}
		values[i] = v
	}
	return values, nil
}

// buildGlobals creates the instance global address space.
func (inst *ModuleInstance) buildGlobals() error {
	for i, g := range inst.m.Globals {
		if g.ImportModule != "" || g.ImportName != "" {
			return fmt.Errorf("unsupported global import %q.%q", g.ImportModule, g.ImportName)
		}
		value, err := vm.EvalConstExpr(g.Init, vmResolver{inst: inst, constExpr: true})
		if err != nil {
			return fmt.Errorf("global[%d]: %w", i, err)
		}
		if err := vm.CheckResults([]wasmir.ValueType{g.Type}, []Value{value}); err != nil {
			return fmt.Errorf("global[%d]: initializer type mismatch: %w", i, err)
		}
		inst.globals = append(inst.globals, globalInst{typ: g.Type, mutable: g.Mutable, value: value})
	}
	return nil
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
		code, err := vm.CompileFunction(f)
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

// callFunc dispatches a function-index call.
func (inst *ModuleInstance) callFunc(index uint32, args []Value) ([]Value, error) {
	if int(index) >= len(inst.funcs) {
		return nil, fmt.Errorf("function index %d out of range", index)
	}
	fn := inst.funcs[index]
	ft, err := inst.funcType(fn.typeIdx)
	if err != nil {
		return nil, err
	}
	if err := vm.CheckArgs(ft.Params, args); err != nil {
		return nil, fmt.Errorf("func[%d]: %w", index, err)
	}
	if fn.host != nil {
		results, err := fn.host.Func(&Context{Runtime: inst.rt, Instance: inst}, args)
		if err != nil {
			return nil, err
		}
		if err := vm.CheckResults(ft.Results, results); err != nil {
			return nil, fmt.Errorf("func[%d]: %w", index, err)
		}
		return results, nil
	}
	return vm.ExecuteFunction(fn.code, ft, args, vmResolver{inst: inst})
}

// funcType returns the function type referenced by typeIdx.
func (inst *ModuleInstance) funcType(typeIdx uint32) (wasmir.TypeDef, error) {
	if int(typeIdx) >= len(inst.m.Types) || inst.m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d is not a function type", typeIdx)
	}
	return inst.m.Types[typeIdx], nil
}

type vmResolver struct {
	inst *ModuleInstance

	// constExpr applies the stricter global.get rules used while evaluating
	// module-level constant expressions such as global initializers and active
	// data offsets.
	constExpr bool
}

func (r vmResolver) FuncType(index uint32) (wasmir.TypeDef, error) {
	inst := r.inst
	if int(index) >= len(inst.funcs) {
		return wasmir.TypeDef{}, fmt.Errorf("call function index %d out of range", index)
	}
	return inst.funcType(inst.funcs[index].typeIdx)
}

func (r vmResolver) CallFunc(index uint32, args []Value) ([]Value, error) {
	return r.inst.callFunc(index, args)
}

func (r vmResolver) GlobalGet(index uint32) (Value, error) {
	if int(index) >= len(r.inst.globals) {
		return Value{}, fmt.Errorf("global index %d out of range", index)
	}
	g := r.inst.globals[index]
	if r.constExpr && g.mutable {
		return Value{}, fmt.Errorf("global %d is mutable", index)
	}
	return g.value, nil
}

func (r vmResolver) GlobalSet(index uint32, value Value) error {
	if int(index) >= len(r.inst.globals) {
		return fmt.Errorf("global index %d out of range", index)
	}
	g := &r.inst.globals[index]
	if !g.mutable {
		return fmt.Errorf("global %d is immutable", index)
	}
	if err := vm.CheckArgs([]wasmir.ValueType{g.typ}, []Value{value}); err != nil {
		return fmt.Errorf("global.set %d: %w", index, err)
	}
	g.value = value
	return nil
}

// MemoryLoad reads a little-endian integer from an instantiated memory.
func (r vmResolver) MemoryLoad(index uint32, address uint64, size uint32) (uint64, error) {
	mem, err := r.memory(index, address, uint64(size))
	if err != nil {
		return 0, err
	}
	switch size {
	case 1:
		return uint64(mem[0]), nil
	case 2:
		return uint64(binary.LittleEndian.Uint16(mem)), nil
	case 4:
		return uint64(binary.LittleEndian.Uint32(mem)), nil
	case 8:
		return binary.LittleEndian.Uint64(mem), nil
	default:
		return 0, fmt.Errorf("unsupported memory load size %d", size)
	}
}

// MemoryStore writes the low-order bytes of value to an instantiated memory in
// little-endian order.
func (r vmResolver) MemoryStore(index uint32, address uint64, size uint32, value uint64) error {
	mem, err := r.memory(index, address, uint64(size))
	if err != nil {
		return err
	}
	switch size {
	case 1:
		mem[0] = byte(value)
		return nil
	case 2:
		binary.LittleEndian.PutUint16(mem, uint16(value))
		return nil
	case 4:
		binary.LittleEndian.PutUint32(mem, uint32(value))
		return nil
	case 8:
		binary.LittleEndian.PutUint64(mem, value)
		return nil
	default:
		return fmt.Errorf("unsupported memory store size %d", size)
	}
}

// MemorySize returns the current size of an instantiated memory in WebAssembly
// pages.
func (r vmResolver) MemorySize(index uint32) (uint64, error) {
	mem, err := r.memoryInst(index)
	if err != nil {
		return 0, err
	}
	return uint64(len(mem.data) / wasmPageSize), nil
}

// MemoryGrow grows an instantiated memory by delta WebAssembly pages.
func (r vmResolver) MemoryGrow(index uint32, delta uint64) (uint64, bool, error) {
	mem, err := r.memoryInst(index)
	if err != nil {
		return 0, false, err
	}
	oldPages := uint64(len(mem.data) / wasmPageSize)
	if delta > ^uint64(0)-oldPages {
		return oldPages, false, nil
	}
	newPages := oldPages + delta
	if mem.max != nil && newPages > *mem.max {
		return oldPages, false, nil
	}
	if newPages > uint64(int(^uint(0)>>1))/wasmPageSize {
		return oldPages, false, nil
	}
	newSize := int(newPages * wasmPageSize)
	mem.data = append(mem.data, make([]byte, newSize-len(mem.data))...)
	return oldPages, true, nil
}

// MemoryCopy copies bytes between instantiated memories.
func (r vmResolver) MemoryCopy(dstIndex uint32, dstAddress uint64, srcIndex uint32, srcAddress uint64, size uint64) error {
	dst, err := r.memory(dstIndex, dstAddress, size)
	if err != nil {
		return err
	}
	src, err := r.memory(srcIndex, srcAddress, size)
	if err != nil {
		return err
	}
	copy(dst, src)
	return nil
}

// MemoryFill writes value to a contiguous byte range in an instantiated memory.
func (r vmResolver) MemoryFill(index uint32, address uint64, size uint64, value byte) error {
	dst, err := r.memory(index, address, size)
	if err != nil {
		return err
	}
	for i := range dst {
		dst[i] = value
	}
	return nil
}

// MemoryInit copies bytes from a live data segment into an instantiated memory.
func (r vmResolver) MemoryInit(memoryIndex uint32, dataIndex uint32, dstAddress uint64, srcOffset uint64, size uint64) error {
	data, err := r.dataSegment(dataIndex)
	if err != nil {
		return err
	}
	if data.dropped {
		return fmt.Errorf("data segment %d is dropped", dataIndex)
	}
	if srcOffset > uint64(len(data.init)) || size > uint64(len(data.init))-srcOffset {
		return fmt.Errorf("data segment access out of bounds")
	}
	dst, err := r.memory(memoryIndex, dstAddress, size)
	if err != nil {
		return err
	}
	start := int(srcOffset)
	copy(dst, data.init[start:start+int(size)])
	return nil
}

// DataDrop marks a data segment unavailable for future memory.init operations.
func (r vmResolver) DataDrop(index uint32) error {
	data, err := r.dataSegment(index)
	if err != nil {
		return err
	}
	data.dropped = true
	return nil
}

// TableGet returns one reference from an instantiated table.
func (r vmResolver) TableGet(index uint32, elemIndex uint64) (Value, error) {
	table, err := r.table(index, elemIndex, 1)
	if err != nil {
		return Value{}, err
	}
	return table[0], nil
}

// TableSet writes one reference to an instantiated table.
func (r vmResolver) TableSet(index uint32, elemIndex uint64, value Value) error {
	tableInst, err := r.tableInst(index)
	if err != nil {
		return err
	}
	if err := vm.CheckArgs([]wasmir.ValueType{tableInst.refType}, []Value{value}); err != nil {
		return err
	}
	table, err := r.table(index, elemIndex, 1)
	if err != nil {
		return err
	}
	table[0] = value
	return nil
}

// TableSize returns the current size of an instantiated table in elements.
func (r vmResolver) TableSize(index uint32) (uint64, error) {
	table, err := r.tableInst(index)
	if err != nil {
		return 0, err
	}
	return uint64(len(table.elems)), nil
}

// dataSegment resolves a data index to the mutable instantiated data segment
// state.
func (r vmResolver) dataSegment(index uint32) (*dataInst, error) {
	if int(index) >= len(r.inst.data) {
		return nil, fmt.Errorf("data segment index %d out of range", index)
	}
	return &r.inst.data[index], nil
}

// memoryInst resolves a memory index to the mutable instantiated memory state.
func (r vmResolver) memoryInst(index uint32) (*memoryInst, error) {
	if int(index) >= len(r.inst.memories) {
		return nil, fmt.Errorf("memory index %d out of range", index)
	}
	return &r.inst.memories[index], nil
}

// tableInst resolves a table index to the mutable instantiated table state.
func (r vmResolver) tableInst(index uint32) (*tableInst, error) {
	if int(index) >= len(r.inst.tables) {
		return nil, fmt.Errorf("table index %d out of range", index)
	}
	return &r.inst.tables[index], nil
}

// table returns the in-bounds element window addressed by a VM table operation.
func (r vmResolver) table(index uint32, elemIndex uint64, size uint64) ([]Value, error) {
	tableInst, err := r.tableInst(index)
	if err != nil {
		return nil, err
	}
	elems := tableInst.elems
	if elemIndex > uint64(len(elems)) || size > uint64(len(elems))-elemIndex {
		return nil, fmt.Errorf("table access out of bounds")
	}
	start := int(elemIndex)
	return elems[start : start+int(size)], nil
}

// memory returns the in-bounds byte window addressed by a VM memory operation.
//
// The VM has already computed the effective address from the dynamic address
// operand and the static offset immediate. This helper owns the instance-side
// checks: memory index resolution, overflow-safe bounds validation, and
// conversion from uint64 addresses to Go slice indices.
func (r vmResolver) memory(index uint32, address uint64, size uint64) ([]byte, error) {
	memInst, err := r.memoryInst(index)
	if err != nil {
		return nil, err
	}
	mem := memInst.data
	if address > uint64(len(mem)) || uint64(size) > uint64(len(mem))-address {
		return nil, fmt.Errorf("memory access out of bounds")
	}
	start := int(address)
	return mem[start : start+int(size)], nil
}
