// Package wasmvm exposes a minimal WebAssembly interpreter runtime for wasmir
// modules.
//
// The package can instantiate an already-validated wasmir.Module, look up
// exported functions, globals, and memories, call functions with runtime
// values, satisfy WebAssembly function imports with Go callbacks, and share
// exported globals and memories with later instantiations.
//
// Each table is limited to 1,048,576 entries. Larger initial sizes fail to
// instantiate, and table.grow returns -1 if growth would exceed this runtime
// resource limit. Declared maxima may be larger.
package wasmvm

import (
	"fmt"

	"github.com/eliben/watgo/internal/typeequiv"
	"github.com/eliben/watgo/internal/vm"
	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value passed into or returned from a Func.
//
// Type identifies which payload field is meaningful. For example, values with
// Type set to wasmir.ValueTypeI32 use I32, while values with Type set to
// wasmir.ValueTypeF64 use F64. Prefer the I32, I64, F32, and F64 constructors
// over constructing Value directly.
type Value = vm.Value

// Reference is one runtime reference value carried by a Value.
type Reference = vm.Reference

// RefKind classifies the reference payload carried by a runtime Value.
type RefKind = vm.RefKind

const (
	// RefKindNull is the null reference.
	RefKindNull = vm.RefKindNull

	// RefKindFunc is a reference to a function in the instance function index
	// space.
	RefKindFunc = vm.RefKindFunc

	// RefKindExtern is an opaque externref value supplied by the host.
	RefKindExtern = vm.RefKindExtern

	// RefKindExn is a reference to a WebAssembly exception object.
	RefKindExn = vm.RefKindExn

	// RefKindI31 is an unboxed signed 31-bit integer reference.
	RefKindI31 = vm.RefKindI31

	// RefKindStruct is a reference to a WebAssembly GC struct object.
	RefKindStruct = vm.RefKindStruct

	// RefKindArray is a reference to a WebAssembly GC array object.
	RefKindArray = vm.RefKindArray
)

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

// ExternRef returns a runtime Value carrying an opaque host externref identity.
func ExternRef(id uint64) Value {
	return Value{
		Type: wasmir.RefTypeExtern(true),
		Ref:  Reference{Kind: RefKindExtern, ExternID: id},
	}
}

// Imports maps WebAssembly import module names and field names to host externs.
//
// For an import such as (import "env" "inc" (func ...)), the corresponding
// Go value belongs at imports["env"]["inc"]. Function imports should be a
// *HostFunc created with NewHostFunc or a *Func exported from another module.
// Memory and table imports should be *Memory and *Table values created by this
// package or obtained from another instantiated module's exported resources.
type Imports map[string]map[string]Extern

// Extern is a runtime object supplied for a module import.
//
// *HostFunc, *Func, *Global, *Memory, *Table, and *Tag are the currently
// supported Extern implementations.
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

	// sourceModule is set for callbacks that adapt an exported WebAssembly
	// function, so import linking can compare indexed reference types against
	// the exporting module's type graph.
	sourceModule *wasmir.Module

	// sourceTypeIndex is the function type index in sourceModule.
	sourceTypeIndex uint32

	// sourceTypeKnown reports whether sourceModule/sourceTypeIndex describe
	// this host function's original WebAssembly type.
	sourceTypeKnown bool
}

// isExtern marks HostFunc pointers as valid import objects.
func (*HostFunc) isExtern() {}

// NewHostFunc returns a HostFunc with the given WebAssembly signature and Go
// callback.
//
// params and results are copied by reference, so callers should treat them as
// immutable after passing them here. fn must be non-nil before the HostFunc is
// used to instantiate a module; otherwise Instantiate returns an error.
func NewHostFunc(params, results []wasmir.ValueType, fn func(ctx *Context, args []Value) ([]Value, error)) *HostFunc {
	return &HostFunc{Params: params, Results: results, Func: fn}
}

// Memory is an instantiated WebAssembly linear memory exposed for imports.
type Memory struct {
	mem *vm.Memory
}

// isExtern marks Memory pointers as valid import objects.
func (*Memory) isExtern() {}

// NewMemory returns an instantiated WebAssembly memory for use as an import.
func NewMemory(def wasmir.Memory) (*Memory, error) {
	mem, err := vm.NewMemory(def)
	if err != nil {
		return nil, err
	}
	return &Memory{mem: mem}, nil
}

// Size returns the current memory size in WebAssembly pages.
func (m *Memory) Size() uint64 {
	return m.mem.Size()
}

// Table is an instantiated WebAssembly table exposed for imports.
type Table struct {
	table *vm.Table
}

// isExtern marks Table pointers as valid import objects.
func (*Table) isExtern() {}

// NewTable returns an instantiated WebAssembly table for use as an import.
func NewTable(def wasmir.Table) (*Table, error) {
	table, err := vm.NewTable(def)
	if err != nil {
		return nil, err
	}
	return &Table{table: table}, nil
}

// Size returns the current table size in elements.
func (t *Table) Size() uint64 {
	return t.table.Size()
}

// Tag is an instantiated WebAssembly exception tag exposed for imports.
type Tag struct {
	tag             *vm.Tag
	sourceModule    *wasmir.Module
	sourceTypeIndex uint32
	sourceTypeKnown bool
}

// isExtern marks Tag pointers as valid import objects.
func (*Tag) isExtern() {}

// NewGlobal returns an instantiated WebAssembly global for use as an import.
func NewGlobal(value Value, mutable bool) (*Global, error) {
	global, err := vm.NewGlobal(value.Type, mutable, value)
	if err != nil {
		return nil, err
	}
	return &Global{global: global}, nil
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
	hosts, err := buildHostFuncs(m, imports)
	if err != nil {
		return nil, err
	}

	inst := &ModuleInstance{
		module:   m,
		rt:       rt,
		hosts:    hosts,
		exports:  make(map[string]*Func),
		globals:  make(map[string]*Global),
		memories: make(map[string]*Memory),
		tables:   make(map[string]*Table),
		tags:     make(map[string]*Tag),
		imports:  imports,
	}
	vmInst, err := vm.Instantiate(m, vmResolver{inst: inst})
	if err != nil {
		return nil, err
	}
	inst.vm = vmInst

	for _, exp := range m.Exports {
		switch exp.Kind {
		case wasmir.ExternalKindFunction:
			if _, err := inst.vm.FuncType(exp.Index); err != nil {
				return nil, fmt.Errorf("export %q: function index %d out of range", exp.Name, exp.Index)
			}
			inst.exports[exp.Name] = &Func{inst: inst, index: exp.Index}
		case wasmir.ExternalKindGlobal:
			global, err := inst.vm.Global(exp.Index)
			if err != nil {
				return nil, fmt.Errorf("export %q: global index %d out of range", exp.Name, exp.Index)
			}
			inst.globals[exp.Name] = &Global{global: global}
		case wasmir.ExternalKindMemory:
			mem, err := inst.vm.Memory(exp.Index)
			if err != nil {
				return nil, fmt.Errorf("export %q: memory index %d out of range", exp.Name, exp.Index)
			}
			inst.memories[exp.Name] = &Memory{mem: mem}
		case wasmir.ExternalKindTable:
			table, err := inst.vm.Table(exp.Index)
			if err != nil {
				return nil, fmt.Errorf("export %q: table index %d out of range", exp.Name, exp.Index)
			}
			inst.tables[exp.Name] = &Table{table: table}
		case wasmir.ExternalKindTag:
			tag, err := inst.vm.Tag(exp.Index)
			if err != nil {
				return nil, fmt.Errorf("export %q: tag index %d out of range", exp.Name, exp.Index)
			}
			typeIndex, err := tagTypeIndex(m, exp.Index)
			if err != nil {
				return nil, fmt.Errorf("export %q: %w", exp.Name, err)
			}
			inst.tags[exp.Name] = &Tag{
				tag:             tag,
				sourceModule:    m,
				sourceTypeIndex: typeIndex,
				sourceTypeKnown: true,
			}
		}
	}
	return inst, nil
}

// ModuleInstance is one instantiated WebAssembly module.
//
// A ModuleInstance owns the public binding to the internal VM instance and its
// exported functions. Values returned by ExportedFunc are bound to this
// instance.
type ModuleInstance struct {
	module   *wasmir.Module
	rt       *Runtime
	vm       *vm.Instance
	hosts    []HostFunc
	exports  map[string]*Func
	globals  map[string]*Global
	memories map[string]*Memory
	tables   map[string]*Table
	tags     map[string]*Tag
	imports  Imports
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

// ExportedGlobal returns the exported global with the given name.
//
// The returned boolean is false when name is not exported as a global. The
// returned Global is bound to this ModuleInstance and can be read with
// Global.Value.
func (inst *ModuleInstance) ExportedGlobal(name string) (*Global, bool) {
	g, ok := inst.globals[name]
	return g, ok
}

// ExportedMemory returns the exported memory with the given name.
//
// The returned boolean is false when name is not exported as a memory. The
// returned Memory is bound to this ModuleInstance and can be supplied as an
// import to another instantiation.
func (inst *ModuleInstance) ExportedMemory(name string) (*Memory, bool) {
	m, ok := inst.memories[name]
	return m, ok
}

// ExportedTable returns the exported table with the given name.
//
// The returned boolean is false when name is not exported as a table. The
// returned Table is bound to this ModuleInstance and can be supplied as an
// import to another instantiation.
func (inst *ModuleInstance) ExportedTable(name string) (*Table, bool) {
	t, ok := inst.tables[name]
	return t, ok
}

// ExportedTag returns the exported tag with the given name.
//
// The returned boolean is false when name is not exported as a tag. The
// returned Tag can be supplied as an import to another instantiation.
func (inst *ModuleInstance) ExportedTag(name string) (*Tag, bool) {
	t, ok := inst.tags[name]
	return t, ok
}

// Global is an instantiated WebAssembly global.
type Global struct {
	global *vm.Global
}

// isExtern marks Global pointers as valid import objects.
func (*Global) isExtern() {}

// Type returns the value type stored in g.
func (g *Global) Type() wasmir.ValueType {
	return g.global.Type()
}

// Mutable reports whether g can be updated.
func (g *Global) Mutable() bool {
	return g.global.Mutable()
}

// Value returns the current value stored in g.
func (g *Global) Value() (Value, error) {
	return g.global.Value(), nil
}

// Set updates g after checking mutability and value type.
func (g *Global) Set(value Value) error {
	return g.global.Set(value)
}

// Func is a callable WebAssembly function exported from a ModuleInstance.
//
// A Func is obtained with ModuleInstance.ExportedFunc. Calls validate argument
// count and value types against the function's WebAssembly signature.
type Func struct {
	inst  *ModuleInstance
	index uint32
}

// isExtern marks Func pointers as valid import objects.
func (*Func) isExtern() {}

// Type returns f's WebAssembly function signature.
func (f *Func) Type() (params, results []wasmir.ValueType, err error) {
	ft, err := f.inst.vm.FuncType(f.index)
	if err != nil {
		return nil, nil, err
	}
	return ft.Params, ft.Results, nil
}

// Call invokes f with WebAssembly runtime values.
//
// args must contain one Value per function parameter, in parameter order. On
// success, Call returns one Value per function result, in result order. It
// returns an error when the argument count or types are wrong, when a host
// callback returns an error, or when execution traps in the currently supported
// instruction subset.
func (f *Func) Call(args ...Value) ([]Value, error) {
	return f.inst.vm.CallFunc(f.index, args)
}

// buildHostFuncs resolves and type-checks imported host functions in function
// index order.
func buildHostFuncs(m *wasmir.Module, imports Imports) ([]HostFunc, error) {
	var hosts []HostFunc
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		host, err := resolveHostFunc(imports, imp)
		if err != nil {
			return nil, err
		}
		if err := checkHostFuncType(m, imp, host); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// resolveMemoryImport finds the host memory supplied for a memory import.
func resolveMemoryImport(imports Imports, def wasmir.Memory) (*Memory, error) {
	fields, ok := imports[def.ImportModule]
	if !ok {
		return nil, fmt.Errorf("missing import module %q", def.ImportModule)
	}
	ext, ok := fields[def.ImportName]
	if !ok {
		return nil, fmt.Errorf("missing import %q.%q", def.ImportModule, def.ImportName)
	}
	switch mem := ext.(type) {
	case *Memory:
		if mem == nil {
			return nil, fmt.Errorf("import %q.%q is nil", def.ImportModule, def.ImportName)
		}
		return mem, nil
	default:
		return nil, fmt.Errorf("import %q.%q is not a memory", def.ImportModule, def.ImportName)
	}
}

// resolveTableImport finds the host table supplied for a table import.
func resolveTableImport(imports Imports, def wasmir.Table) (*Table, error) {
	fields, ok := imports[def.ImportModule]
	if !ok {
		return nil, fmt.Errorf("missing import module %q", def.ImportModule)
	}
	ext, ok := fields[def.ImportName]
	if !ok {
		return nil, fmt.Errorf("missing import %q.%q", def.ImportModule, def.ImportName)
	}
	switch table := ext.(type) {
	case *Table:
		if table == nil {
			return nil, fmt.Errorf("import %q.%q is nil", def.ImportModule, def.ImportName)
		}
		return table, nil
	default:
		return nil, fmt.Errorf("import %q.%q is not a table", def.ImportModule, def.ImportName)
	}
}

// resolveGlobalImport finds the host global supplied for a global import.
func resolveGlobalImport(imports Imports, def wasmir.Global) (*Global, error) {
	fields, ok := imports[def.ImportModule]
	if !ok {
		return nil, fmt.Errorf("missing import module %q", def.ImportModule)
	}
	ext, ok := fields[def.ImportName]
	if !ok {
		return nil, fmt.Errorf("missing import %q.%q", def.ImportModule, def.ImportName)
	}
	switch global := ext.(type) {
	case *Global:
		if global == nil {
			return nil, fmt.Errorf("import %q.%q is nil", def.ImportModule, def.ImportName)
		}
		return global, nil
	default:
		return nil, fmt.Errorf("import %q.%q is not a global", def.ImportModule, def.ImportName)
	}
}

// resolveTagImport finds the host tag supplied for a tag import.
func resolveTagImport(imports Imports, imp wasmir.Import) (*Tag, error) {
	fields, ok := imports[imp.Module]
	if !ok {
		return nil, fmt.Errorf("missing import module %q", imp.Module)
	}
	ext, ok := fields[imp.Name]
	if !ok {
		return nil, fmt.Errorf("missing import %q.%q", imp.Module, imp.Name)
	}
	switch tag := ext.(type) {
	case *Tag:
		if tag == nil {
			return nil, fmt.Errorf("import %q.%q is nil", imp.Module, imp.Name)
		}
		return tag, nil
	default:
		return nil, fmt.Errorf("import %q.%q is not a tag", imp.Module, imp.Name)
	}
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
	case *HostFunc:
		if host == nil {
			return HostFunc{}, fmt.Errorf("import %q.%q is nil", imp.Module, imp.Name)
		}
		return *host, nil
	case *Func:
		if host == nil {
			return HostFunc{}, fmt.Errorf("import %q.%q is nil", imp.Module, imp.Name)
		}
		return exportedFuncHostFunc(host)
	default:
		return HostFunc{}, fmt.Errorf("import %q.%q is not a function", imp.Module, imp.Name)
	}
}

// exportedFuncHostFunc adapts an exported WebAssembly function to the existing
// imported-function callback path used by the internal VM.
func exportedFuncHostFunc(fn *Func) (HostFunc, error) {
	ft, err := fn.inst.vm.FuncType(fn.index)
	if err != nil {
		return HostFunc{}, err
	}
	typeIndex, err := fn.inst.vm.FuncTypeIndex(fn.index)
	if err != nil {
		return HostFunc{}, err
	}
	return HostFunc{
		Params:          ft.Params,
		Results:         ft.Results,
		sourceModule:    fn.inst.module,
		sourceTypeIndex: typeIndex,
		sourceTypeKnown: true,
		Func: func(ctx *Context, args []Value) ([]Value, error) {
			return fn.Call(args...)
		},
	}, nil
}

// checkHostFuncType checks that a supplied host function matches the module's
// declared import type.
func checkHostFuncType(m *wasmir.Module, imp wasmir.Import, host HostFunc) error {
	if int(imp.TypeIdx) >= len(m.Types) || m.Types[imp.TypeIdx].Kind != wasmir.TypeDefKindFunc {
		return fmt.Errorf("import %q.%q has invalid function type", imp.Module, imp.Name)
	}
	if host.sourceTypeKnown {
		if !typeequiv.TypeSubtype(host.sourceModule, host.sourceTypeIndex, m, imp.TypeIdx) {
			return fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
		}
	} else if !typeequiv.FuncTypeAndSignature(m, imp.TypeIdx, host.Params, host.Results) {
		return fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
	}
	if host.Func == nil {
		return fmt.Errorf("import %q.%q has nil function", imp.Module, imp.Name)
	}
	return nil
}

// tagTypeIndex resolves an absolute tag index to the module type index it uses.
func tagTypeIndex(m *wasmir.Module, tagIndex uint32) (uint32, error) {
	var current uint32
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindTag {
			continue
		}
		if current == tagIndex {
			return imp.TypeIdx, nil
		}
		current++
	}
	defIndex := tagIndex - current
	if int(defIndex) >= len(m.Tags) {
		return 0, fmt.Errorf("tag index %d out of range", tagIndex)
	}
	return m.Tags[defIndex].TypeIdx, nil
}

type vmResolver struct {
	inst *ModuleInstance
}

// CallFunc invokes an imported host function at index.
func (r vmResolver) CallFunc(index uint32, args []vm.Value) ([]vm.Value, error) {
	if int(index) >= len(r.inst.hosts) {
		return nil, fmt.Errorf("host function index %d out of range", index)
	}
	return r.inst.hosts[index].Func(&Context{Runtime: r.inst.rt, Instance: r.inst}, args)
}

// Memory resolves an imported memory for the internal VM.
func (r vmResolver) Memory(index uint32, def wasmir.Memory) (*vm.Memory, error) {
	mem, err := resolveMemoryImport(r.inst.imports, def)
	if err != nil {
		return nil, err
	}
	return mem.mem, nil
}

// Table resolves an imported table for the internal VM.
func (r vmResolver) Table(index uint32, def wasmir.Table) (*vm.Table, error) {
	table, err := resolveTableImport(r.inst.imports, def)
	if err != nil {
		return nil, err
	}
	return table.table, nil
}

// Global resolves an imported global for the internal VM.
func (r vmResolver) Global(index uint32, def wasmir.Global) (*vm.Global, error) {
	global, err := resolveGlobalImport(r.inst.imports, def)
	if err != nil {
		return nil, err
	}
	return global.global, nil
}

// Tag resolves an imported tag for the internal VM.
func (r vmResolver) Tag(index uint32, ft wasmir.TypeDef) (*vm.Tag, error) {
	imp, err := tagImportAt(r.inst.module, index)
	if err != nil {
		return nil, err
	}
	tag, err := resolveTagImport(r.inst.imports, imp)
	if err != nil {
		return nil, err
	}
	if tag.sourceTypeKnown {
		if !typeequiv.Types(tag.sourceModule, tag.sourceTypeIndex, r.inst.module, imp.TypeIdx) {
			return nil, fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
		}
	} else if !sameValueTypes(tag.tag.Params(), ft.Params) {
		return nil, fmt.Errorf("import %q.%q type mismatch", imp.Module, imp.Name)
	}
	return tag.tag, nil
}

// tagImportAt returns the tag import at absolute imported-tag index.
func tagImportAt(m *wasmir.Module, index uint32) (wasmir.Import, error) {
	var current uint32
	for _, imp := range m.Imports {
		if imp.Kind != wasmir.ExternalKindTag {
			continue
		}
		if current == index {
			return imp, nil
		}
		current++
	}
	return wasmir.Import{}, fmt.Errorf("tag import index %d out of range", index)
}

// sameValueTypes reports whether a and b have the same runtime value types.
func sameValueTypes(a []wasmir.ValueType, b []wasmir.ValueType) bool {
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
