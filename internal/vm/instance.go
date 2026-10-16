package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync/atomic"

	"github.com/eliben/watgo/wasmir"
)

const (
	wasmPageSize = 64 * 1024

	// maxCallDepth is the VM's finite abstract call-stack resource. The wasm
	// spec requires unbounded recursion to exhaust in finite time instead of
	// depending on the host language stack limit.
	maxCallDepth = 10000

	// maxWasm32MemoryPages is the architectural wasm32 memory limit. A memory
	// without an explicit max is still bounded by the 32-bit address space.
	maxWasm32MemoryPages = 65536

	// maxTableElements bounds each table's allocation in this interpreter.
	// This is a runtime resource limit, separate from the declared maximum
	// used for import matching. Oversized tables fail to instantiate, and
	// table.grow returns -1 when growth would exceed the limit.
	maxTableElements = 1 << 20
)

var errCallStackExhausted = errors.New("call stack exhausted")

// Instance is the VM-owned execution state for one instantiated module.
type Instance struct {
	m        *wasmir.Module
	funcs    []funcInst
	tags     []*Tag
	globals  []*Global
	memories []*Memory
	tables   []*Table
	data     []dataInst
	elems    []elemInst
	resolver Resolver
	exns     map[uint64]wasmException
	nextExn  uint64

	// callDepth counts active WebAssembly calls in this instance. It is used
	// to turn runaway recursion into a regular VM error before the Go runtime
	// stack overflows.
	callDepth int
}

type funcInst struct {
	// typeIdx indexes inst.m.Types and describes both imported and
	// module-defined functions in the unified function index space.
	typeIdx uint32

	// imported reports whether this function index must be dispatched through
	// Resolver.CallFunc.
	imported bool

	// code is non-nil for module-defined functions. It is compiled once during
	// instantiation from wasmir.Function into the VM's execution form.
	code *function
}

type gcObjectKind uint8

const (
	gcObjectStruct gcObjectKind = iota
	gcObjectArray
)

// gcObject is one VM-owned GC heap allocation.
type gcObject struct {
	// inst is the module instance that allocated this object. The typeIndex
	// below is interpreted in this instance's type space.
	inst *Instance

	kind      gcObjectKind
	typeIndex uint32
	fields    []Value
	elems     []Value
}

// Tag is one instantiated exception tag identity.
type Tag struct {
	id     uint64
	params []wasmir.ValueType
}

var nextTagID atomic.Uint64

// NewTag creates a VM exception tag with a fresh runtime identity.
func NewTag(params []wasmir.ValueType) *Tag {
	return &Tag{id: nextTagID.Add(1), params: slices.Clone(params)}
}

// Params returns the payload value types carried by this tag.
func (t *Tag) Params() []wasmir.ValueType {
	return slices.Clone(t.params)
}

// Global is one instantiated global in the module's global index space.
type Global struct {
	// typ is the validated value type of value. It is kept here so global.set
	// can check writes without looking back into the source module.
	typ wasmir.ValueType

	// mutable records whether global.set is allowed to update value.
	mutable bool

	// value is the current runtime value stored in this global.
	value Value
}

// NewGlobal creates a VM global instance with an initial value.
func NewGlobal(typ wasmir.ValueType, mutable bool, value Value) (*Global, error) {
	if err := checkResults([]wasmir.ValueType{typ}, []Value{value}); err != nil {
		return nil, err
	}
	return &Global{typ: typ, mutable: mutable, value: value}, nil
}

// Type returns the value type stored in g.
func (g *Global) Type() wasmir.ValueType {
	return g.typ
}

// Mutable reports whether global.set can update g.
func (g *Global) Mutable() bool {
	return g.mutable
}

// Value returns the current value stored in g.
func (g *Global) Value() Value {
	return g.value
}

// Set updates g after checking mutability and value type.
func (g *Global) Set(value Value) error {
	if !g.mutable {
		return fmt.Errorf("global is immutable")
	}
	if err := checkArgs([]wasmir.ValueType{g.typ}, []Value{value}); err != nil {
		return err
	}
	g.value = value
	return nil
}

// Memory is one instantiated linear memory in the module's memory index space.
type Memory struct {
	// addressType is the validated address type for this memory. It controls
	// whether memory instructions consume i32 or i64 address operands.
	addressType wasmir.ValueType

	// max is the optional declared maximum size in WebAssembly pages.
	max *uint64

	// data is the mutable linear-memory byte buffer. Its length is always a
	// whole number of WebAssembly pages.
	data []byte
}

// NewMemory creates a VM memory instance for the validated memory definition m.
func NewMemory(m wasmir.Memory) (*Memory, error) {
	if m.AddressType != wasmir.ValueTypeI32 && m.AddressType != wasmir.ValueTypeI64 {
		return nil, fmt.Errorf("unsupported address type %s", m.AddressType)
	}
	if m.Min > uint64(int(^uint(0)>>1))/wasmPageSize {
		return nil, fmt.Errorf("minimum size is too large")
	}
	size := int(m.Min * wasmPageSize)
	return &Memory{
		addressType: m.AddressType,
		max:         m.Max,
		data:        make([]byte, size),
	}, nil
}

// AddressType returns the index operand type used by memory instructions.
func (mem *Memory) AddressType() wasmir.ValueType {
	return mem.addressType
}

// Size returns the current memory size in WebAssembly pages.
func (mem *Memory) Size() uint64 {
	return uint64(len(mem.data) / wasmPageSize)
}

// Table is one instantiated table in the module's table index space.
type Table struct {
	// addressType is the validated index type for this table.
	addressType wasmir.ValueType

	// refType is the reference type accepted by this table's elements.
	refType wasmir.ValueType

	// max is the optional declared maximum size in elements.
	max *uint64

	// elems is the mutable table storage.
	elems []Value
}

// NewTable creates a VM table instance for the validated table definition t.
func NewTable(t wasmir.Table) (*Table, error) {
	if t.AddressType != wasmir.ValueTypeI32 && t.AddressType != wasmir.ValueTypeI64 {
		return nil, fmt.Errorf("unsupported table address type %s", t.AddressType)
	}
	init, err := zeroTableValue(t.RefType)
	if err != nil {
		return nil, err
	}
	return newTable(t, init)
}

// AddressType returns the index operand type used by table instructions.
func (t *Table) AddressType() wasmir.ValueType {
	return t.addressType
}

// RefType returns the reference type stored in table elements.
func (t *Table) RefType() wasmir.ValueType {
	return t.refType
}

// Size returns the current table size in elements.
func (t *Table) Size() uint64 {
	return uint64(len(t.elems))
}

// maxElements returns the effective table element limit.
func (t *Table) maxElements() uint64 {
	if t.max != nil {
		return *t.max
	}
	return uint64(int(^uint(0) >> 1))
}

// dataInst is one instantiated data segment in the module's data index space.
type dataInst struct {
	// init is the byte payload used by memory.init while the segment is live.
	init []byte

	// dropped reports whether data.drop or active-segment initialization has
	// made this segment unavailable.
	dropped bool
}

// elemInst is one instantiated element segment in the module's element index
// space.
type elemInst struct {
	// values is the reference payload used by table.init while the segment is
	// live.
	values []Value

	// dropped reports whether elem.drop, active-segment initialization, or
	// declarative-segment instantiation has made this segment unavailable.
	dropped bool
}

// Instantiate creates VM-owned execution state for m.
//
// m and resolver must be non-nil. The resolver is required for imported
// functions and host-owned imported globals, memories, tables, and tags.
func Instantiate(m *wasmir.Module, resolver Resolver) (*Instance, error) {
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}
	if resolver == nil {
		return nil, fmt.Errorf("resolver is nil")
	}
	inst := &Instance{
		m:        m,
		resolver: resolver,
		exns:     map[uint64]wasmException{},
	}
	if err := inst.buildMemories(); err != nil {
		return nil, err
	}
	inst.buildDataSegments()
	if err := inst.buildTags(); err != nil {
		return nil, err
	}
	if err := inst.buildFuncs(); err != nil {
		return nil, err
	}
	if err := inst.buildGlobals(); err != nil {
		return nil, err
	}
	if err := inst.buildTables(); err != nil {
		return nil, err
	}
	if err := inst.buildElementSegments(); err != nil {
		return nil, err
	}
	if err := inst.applyElementSegments(); err != nil {
		return nil, err
	}
	if err := inst.applyDataSegments(); err != nil {
		return nil, err
	}
	if err := inst.executeStartFunction(); err != nil {
		return nil, err
	}
	return inst, nil
}

// buildTags creates the instance tag address space.
func (inst *Instance) buildTags() error {
	for _, imp := range inst.m.Imports {
		if imp.Kind != wasmir.ExternalKindTag {
			continue
		}
		ft, err := inst.funcType(imp.TypeIdx)
		if err != nil {
			return fmt.Errorf("tag import %q.%q has invalid type: %w", imp.Module, imp.Name, err)
		}
		if len(ft.Results) != 0 {
			return fmt.Errorf("tag import %q.%q has non-empty result type", imp.Module, imp.Name)
		}
		tag, err := inst.resolver.Tag(uint32(len(inst.tags)), ft)
		if err != nil {
			return fmt.Errorf("tag[%d]: %w", len(inst.tags), err)
		}
		if err := checkImportedTag(ft, tag); err != nil {
			return fmt.Errorf("tag[%d]: %w", len(inst.tags), err)
		}
		inst.tags = append(inst.tags, tag)
	}
	for _, tag := range inst.m.Tags {
		ft, err := inst.funcType(tag.TypeIdx)
		if err != nil {
			return fmt.Errorf("tag[%d]: %w", len(inst.tags), err)
		}
		if len(ft.Results) != 0 {
			return fmt.Errorf("tag[%d]: non-empty result type", len(inst.tags))
		}
		inst.tags = append(inst.tags, NewTag(ft.Params))
	}
	return nil
}

// checkImportedTag verifies that tag satisfies the imported tag type.
func checkImportedTag(ft wasmir.TypeDef, tag *Tag) error {
	if tag == nil {
		return fmt.Errorf("import resolved to nil tag")
	}
	if len(tag.params) != len(ft.Params) {
		return fmt.Errorf("parameter count mismatch: got %d, want %d", len(tag.params), len(ft.Params))
	}
	for i := range ft.Params {
		if !runtimeTypeMatches(tag.params[i], ft.Params[i]) {
			return fmt.Errorf("parameter %d type mismatch: got %s, want %s", i, tag.params[i], ft.Params[i])
		}
	}
	return nil
}

// executeStartFunction runs the module's optional start function after
// instance initialization finishes.
func (inst *Instance) executeStartFunction() error {
	if inst.m.StartFuncIndex == nil {
		return nil
	}
	if _, err := inst.CallFunc(*inst.m.StartFuncIndex, nil); err != nil {
		return fmt.Errorf("start function: %w", err)
	}
	return nil
}

// CallFunc dispatches a function-index call.
func (inst *Instance) CallFunc(index uint32, args []Value) ([]Value, error) {
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
	if err := inst.enterCall(); err != nil {
		return nil, err
	}
	if fn.imported {
		results, err := inst.resolver.CallFunc(index, args)
		if err != nil {
			inst.exitCall()
			return nil, err
		}
		inst.exitCall()
		if err := checkResults(ft.Results, results); err != nil {
			return nil, fmt.Errorf("func[%d]: %w", index, err)
		}
		return results, nil
	}
	results, err := executeFunction(fn.code, ft, args, inst)
	inst.exitCall()
	return results, err
}

// enterCall records a new active function call and reports stack exhaustion
// when the instance reaches its finite call-depth limit.
func (inst *Instance) enterCall() error {
	if inst.callDepth >= maxCallDepth {
		return errCallStackExhausted
	}
	inst.callDepth++
	return nil
}

// exitCall records that one active function call has completed.
func (inst *Instance) exitCall() {
	inst.callDepth--
}

// GlobalValue returns the current value of the global at index.
func (inst *Instance) GlobalValue(index uint32) (Value, error) {
	return inst.globalGetValue(index)
}

// Global returns the instantiated global at index.
func (inst *Instance) Global(index uint32) (*Global, error) {
	if int(index) >= len(inst.globals) {
		return nil, fmt.Errorf("global index %d out of range", index)
	}
	return inst.globals[index], nil
}

// FuncType returns the signature of the function at index.
func (inst *Instance) FuncType(index uint32) (wasmir.TypeDef, error) {
	typeIndex, err := inst.FuncTypeIndex(index)
	if err != nil {
		return wasmir.TypeDef{}, err
	}
	return inst.funcType(typeIndex)
}

// FuncTypeIndex returns the module type index of the function at index.
func (inst *Instance) FuncTypeIndex(index uint32) (uint32, error) {
	if int(index) >= len(inst.funcs) {
		return 0, fmt.Errorf("call function index %d out of range", index)
	}
	return inst.funcs[index].typeIdx, nil
}

// Tag returns the instantiated tag at index.
func (inst *Instance) Tag(index uint32) (*Tag, error) {
	if int(index) >= len(inst.tags) {
		return nil, fmt.Errorf("tag index %d out of range", index)
	}
	return inst.tags[index], nil
}

// tagType returns the function type referenced by a tag index.
func (inst *Instance) tagType(index uint32) (wasmir.TypeDef, error) {
	if int(index) >= len(inst.tags) {
		return wasmir.TypeDef{}, fmt.Errorf("tag index %d out of range", index)
	}
	return wasmir.TypeDef{Kind: wasmir.TypeDefKindFunc, Params: inst.tags[index].params}, nil
}

// newExceptionRef stores exn and returns an exnref value for it.
func (inst *Instance) newExceptionRef(exn wasmException) Value {
	inst.nextExn++
	id := inst.nextExn
	inst.exns[id] = wasmException{
		tagID:   exn.tagID,
		payload: slices.Clone(exn.payload),
	}
	return Value{Type: wasmir.RefTypeExn(false), Ref: Reference{Kind: RefKindExn, ExnID: id}}
}

// exceptionFromRef resolves an exnref value into its stored exception.
func (inst *Instance) exceptionFromRef(ref Reference) (wasmException, error) {
	if ref.Kind != RefKindExn {
		return wasmException{}, fmt.Errorf("expected exnref")
	}
	exn, ok := inst.exns[ref.ExnID]
	if !ok {
		return wasmException{}, fmt.Errorf("exception reference %d not found", ref.ExnID)
	}
	return wasmException{tagID: exn.tagID, payload: slices.Clone(exn.payload)}, nil
}

// callType returns the function type referenced by an indirect call type
// immediate.
func (inst *Instance) callType(index uint32) (wasmir.TypeDef, error) {
	if int(index) >= len(inst.m.Types) {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d out of range", index)
	}
	return inst.m.Types[index], nil
}

// funcType returns the function type referenced by typeIdx.
func (inst *Instance) funcType(typeIdx uint32) (wasmir.TypeDef, error) {
	if int(typeIdx) >= len(inst.m.Types) || inst.m.Types[typeIdx].Kind != wasmir.TypeDefKindFunc {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d is not a function type", typeIdx)
	}
	return inst.m.Types[typeIdx], nil
}

// buildFuncs creates the instance function address space.
func (inst *Instance) buildFuncs() error {
	for _, imp := range inst.m.Imports {
		if imp.Kind != wasmir.ExternalKindFunction {
			continue
		}
		if _, err := inst.funcType(imp.TypeIdx); err != nil {
			return fmt.Errorf("import %q.%q has invalid function type: %w", imp.Module, imp.Name, err)
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: imp.TypeIdx, imported: true})
	}
	for i := range inst.m.Funcs {
		f := &inst.m.Funcs[i]
		code, err := compileFunction(inst.m, f)
		if err != nil {
			return fmt.Errorf("func[%d]: %w", len(inst.funcs), err)
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: f.TypeIdx, code: code})
	}
	return nil
}

// buildGlobals creates the instance global address space.
func (inst *Instance) buildGlobals() error {
	for i, g := range inst.m.Globals {
		if g.Imported {
			global, err := inst.resolver.Global(uint32(i), g)
			if err != nil {
				return fmt.Errorf("global[%d]: %w", i, err)
			}
			if err := checkImportedGlobal(g, global); err != nil {
				return fmt.Errorf("global[%d]: %w", i, err)
			}
			inst.globals = append(inst.globals, global)
			continue
		}
		value, err := inst.evalConstExpr(g.Init, true)
		if err != nil {
			return fmt.Errorf("global[%d]: %w", i, err)
		}
		global, err := NewGlobal(g.Type, g.Mutable, value)
		if err != nil {
			return fmt.Errorf("global[%d]: initializer type mismatch: %w", i, err)
		}
		inst.globals = append(inst.globals, global)
	}
	return nil
}

// checkImportedGlobal verifies that global satisfies the imported global type.
func checkImportedGlobal(def wasmir.Global, global *Global) error {
	if global == nil {
		return fmt.Errorf("import resolved to nil global")
	}
	if !runtimeTypeMatches(global.typ, def.Type) {
		return fmt.Errorf("type mismatch: got %s, want %s", global.typ, def.Type)
	}
	if global.mutable != def.Mutable {
		return fmt.Errorf("mutability mismatch")
	}
	return nil
}

// buildMemories creates the instance memory address space.
func (inst *Instance) buildMemories() error {
	for i, m := range inst.m.Memories {
		if m.Imported {
			mem, err := inst.resolver.Memory(uint32(i), m)
			if err != nil {
				return fmt.Errorf("memory[%d]: %w", i, err)
			}
			if err := checkImportedMemory(m, mem); err != nil {
				return fmt.Errorf("memory[%d]: %w", i, err)
			}
			inst.memories = append(inst.memories, mem)
			continue
		}
		mem, err := NewMemory(m)
		if err != nil {
			return fmt.Errorf("memory[%d]: %w", i, err)
		}
		inst.memories = append(inst.memories, mem)
	}
	return nil
}

// checkImportedMemory verifies that mem satisfies the imported memory type.
func checkImportedMemory(def wasmir.Memory, mem *Memory) error {
	if mem == nil {
		return fmt.Errorf("import resolved to nil memory")
	}
	if mem.addressType != def.AddressType {
		return fmt.Errorf("address type mismatch: got %s, want %s", mem.addressType, def.AddressType)
	}
	if mem.Size() < def.Min {
		return fmt.Errorf("minimum size mismatch: got %d pages, want at least %d", mem.Size(), def.Min)
	}
	if def.Max != nil && mem.maxPages() > *def.Max {
		return fmt.Errorf("maximum size mismatch: got %d pages, want at most %d", mem.maxPages(), *def.Max)
	}
	return nil
}

// buildTables creates the instance table address space.
func (inst *Instance) buildTables() error {
	for i, t := range inst.m.Tables {
		if t.Imported {
			table, err := inst.resolver.Table(uint32(i), t)
			if err != nil {
				return fmt.Errorf("table[%d]: %w", i, err)
			}
			if err := checkImportedTable(t, table); err != nil {
				return fmt.Errorf("table[%d]: %w", i, err)
			}
			inst.tables = append(inst.tables, table)
			continue
		}
		init, err := inst.tableInitialValue(t)
		if err != nil {
			return fmt.Errorf("table[%d]: %w", i, err)
		}
		table, err := newTable(t, init)
		if err != nil {
			return fmt.Errorf("table[%d]: %w", i, err)
		}
		inst.tables = append(inst.tables, table)
	}
	return nil
}

// checkImportedTable verifies that table satisfies the imported table type.
func checkImportedTable(def wasmir.Table, table *Table) error {
	if table == nil {
		return fmt.Errorf("import resolved to nil table")
	}
	if table.addressType != def.AddressType {
		return fmt.Errorf("address type mismatch: got %s, want %s", table.addressType, def.AddressType)
	}
	if !runtimeTypeMatches(table.refType, def.RefType) {
		return fmt.Errorf("element type mismatch: got %s, want %s", table.refType, def.RefType)
	}
	if table.Size() < def.Min {
		return fmt.Errorf("minimum size mismatch: got %d elements, want at least %d", table.Size(), def.Min)
	}
	if def.Max != nil && table.maxElements() > *def.Max {
		return fmt.Errorf("maximum size mismatch: got %d elements, want at most %d", table.maxElements(), *def.Max)
	}
	return nil
}

// tableInitialValue returns the value used to initialize every slot of table t.
func (inst *Instance) tableInitialValue(t wasmir.Table) (Value, error) {
	if len(t.Init) == 0 {
		return zeroTableValue(t.RefType)
	}
	value, err := inst.evalConstExpr(t.Init, true)
	if err != nil {
		return Value{}, err
	}
	if err := checkResults([]wasmir.ValueType{t.RefType}, []Value{value}); err != nil {
		return Value{}, fmt.Errorf("initializer type mismatch: %w", err)
	}
	return value, nil
}

// buildDataSegments creates the instance data segment address space.
func (inst *Instance) buildDataSegments() {
	for _, seg := range inst.m.Data {
		inst.data = append(inst.data, dataInst{init: slices.Clone(seg.Init)})
	}
}

// buildElementSegments creates the instance element segment address space.
func (inst *Instance) buildElementSegments() error {
	for i, seg := range inst.m.Elements {
		values, err := inst.elementSegmentValues(seg)
		if err != nil {
			return fmt.Errorf("element[%d]: %w", i, err)
		}
		inst.elems = append(inst.elems, elemInst{
			values:  values,
			dropped: seg.Mode == wasmir.ElemSegmentModeDeclarative,
		})
	}
	return nil
}

// applyDataSegments copies active data segments into instantiated memories.
func (inst *Instance) applyDataSegments() error {
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
		dst, err := inst.memory(seg.MemoryIndex, offset, uint64(len(seg.Init)))
		if err != nil {
			return fmt.Errorf("data[%d]: %w", i, err)
		}
		copy(dst, seg.Init)
		inst.data[i].dropped = true
	}
	return nil
}

// dataSegmentOffset evaluates the active data segment offset in the target
// memory's address type.
func (inst *Instance) dataSegmentOffset(seg wasmir.DataSegment) (uint64, error) {
	mem, err := inst.memoryInst(seg.MemoryIndex)
	if err != nil {
		return 0, err
	}
	if len(seg.OffsetExpr) > 0 {
		v, err := inst.evalConstExpr(seg.OffsetExpr, true)
		if err != nil {
			return 0, err
		}
		if v.Type != mem.addressType {
			return 0, fmt.Errorf("offset expression has type %s, want %s", v.Type, mem.addressType)
		}
		if v.Type == wasmir.ValueTypeI64 {
			return uint64(v.I64), nil
		}
		return uint64(uint32(v.I32)), nil
	}
	if seg.OffsetType != mem.addressType {
		return 0, fmt.Errorf("offset has type %s, want %s", seg.OffsetType, mem.addressType)
	}
	if seg.OffsetType == wasmir.ValueTypeI64 {
		return uint64(seg.OffsetI64), nil
	}
	return uint64(uint32(int32(seg.OffsetI64))), nil
}

// applyElementSegments copies active element segments into instantiated tables
// and then marks them unavailable for table.init.
func (inst *Instance) applyElementSegments() error {
	for i, seg := range inst.m.Elements {
		if seg.Mode != wasmir.ElemSegmentModeActive {
			continue
		}
		offset, err := inst.elementSegmentOffset(seg)
		if err != nil {
			return fmt.Errorf("element[%d]: %w", i, err)
		}
		values := inst.elems[i].values
		if uint64(len(values)) > uint64(^uint32(0)) {
			return fmt.Errorf("element[%d]: segment is too large", i)
		}
		table, err := inst.table(seg.TableIndex, offset, uint64(len(values)))
		if err != nil {
			return fmt.Errorf("element[%d]: %w", i, err)
		}
		copy(table, values)
		inst.elems[i].dropped = true
	}
	return nil
}

// elementSegmentOffset evaluates the active element segment offset using its
// target table's address type.
func (inst *Instance) elementSegmentOffset(seg wasmir.ElementSegment) (uint64, error) {
	table, err := inst.tableInst(seg.TableIndex)
	if err != nil {
		return 0, err
	}
	if len(seg.OffsetExpr) > 0 {
		v, err := inst.evalConstExpr(seg.OffsetExpr, true)
		if err != nil {
			return 0, err
		}
		if v.Type != table.addressType {
			return 0, fmt.Errorf("offset expression has type %s, want %s", v.Type, table.addressType)
		}
		if v.Type == wasmir.ValueTypeI64 {
			return uint64(v.I64), nil
		}
		return uint64(uint32(v.I32)), nil
	}
	if seg.OffsetType != table.addressType {
		return 0, fmt.Errorf("offset has type %s, want %s", seg.OffsetType, table.addressType)
	}
	if seg.OffsetType == wasmir.ValueTypeI64 {
		return uint64(seg.OffsetI64), nil
	}
	return uint64(uint32(int32(seg.OffsetI64))), nil
}

// elementSegmentValues evaluates the element payload into runtime references.
func (inst *Instance) elementSegmentValues(seg wasmir.ElementSegment) ([]Value, error) {
	if len(seg.FuncIndices) > 0 {
		values := make([]Value, len(seg.FuncIndices))
		for i, funcIndex := range seg.FuncIndices {
			if _, err := inst.FuncType(funcIndex); err != nil {
				return nil, err
			}
			values[i] = Value{Type: wasmir.RefTypeFunc(false), Ref: Reference{Kind: RefKindFunc, FuncIndex: funcIndex, funcInst: inst}}
		}
		return values, nil
	}
	values := make([]Value, len(seg.Exprs))
	for i, expr := range seg.Exprs {
		v, err := inst.evalConstExpr(expr, true)
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

// evalConstExpr evaluates init against this instance's const-expression state.
//
// The input is the flat wasmir instruction sequence used by module-level
// initializers. constExpr reports whether mutable globals must be rejected, as
// required by module-level constant-expression contexts.
func (inst *Instance) evalConstExpr(init []wasmir.Instruction, constExpr bool) (Value, error) {
	stack := make([]Value, 0, 1)
	for pc, ins := range init {
		switch ins.Kind {
		case wasmir.InstrI32Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: ins.I32Const})
		case wasmir.InstrI64Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeI64, I64: ins.I64Const})
		case wasmir.InstrF32Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeF32, F32: math.Float32frombits(ins.F32Const)})
		case wasmir.InstrF64Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeF64, F64: math.Float64frombits(ins.F64Const)})
		case wasmir.InstrV128Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeV128, V128: ins.V128Const})
		case wasmir.InstrRefNull:
			stack = append(stack, Value{Type: ins.RefType, Ref: Reference{Kind: RefKindNull}})
		case wasmir.InstrRefFunc:
			if inst == nil {
				return Value{}, fmt.Errorf("initializer instruction %d: instance is nil", pc)
			}
			if _, err := inst.FuncType(ins.FuncIndex); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, Value{Type: wasmir.RefTypeFunc(false), Ref: Reference{Kind: RefKindFunc, FuncIndex: ins.FuncIndex, funcInst: inst}})
		case wasmir.InstrRefI31:
			v, err := popConstI32(&stack)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, Value{Type: wasmir.RefTypeI31(false), Ref: Reference{Kind: RefKindI31, I31: v & 0x7fffffff}})
		case wasmir.InstrStructNew:
			v, err := inst.evalConstStructNew(&stack, ins.TypeIndex)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrStructNewDefault:
			v, err := inst.evalConstStructNewDefault(ins.TypeIndex)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrArrayNew:
			v, err := inst.evalConstArrayNew(&stack, ins.TypeIndex)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrArrayNewDefault:
			v, err := inst.evalConstArrayNewDefault(&stack, ins.TypeIndex)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrArrayNewFixed:
			v, err := inst.evalConstArrayNewFixed(&stack, ins.TypeIndex, ins.FixedCount)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrExternConvertAny:
			v, err := popConst(&stack)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			v, err = externConvertAny(v)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrAnyConvertExtern:
			v, err := popConst(&stack)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			v, err = anyConvertExtern(v)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrGlobalGet:
			if inst == nil {
				return Value{}, fmt.Errorf("initializer instruction %d: instance is nil", pc)
			}
			v, err := inst.globalGet(ins.GlobalIndex, constExpr)
			if err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, v)
		case wasmir.InstrI32Add:
			if err := evalI32ConstBinOp(&stack, func(a, b int32) int32 { return a + b }); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
		case wasmir.InstrI32Sub:
			if err := evalI32ConstBinOp(&stack, func(a, b int32) int32 { return a - b }); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
		case wasmir.InstrI32Mul:
			if err := evalI32ConstBinOp(&stack, func(a, b int32) int32 { return a * b }); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
		case wasmir.InstrI64Add:
			if err := evalI64ConstBinOp(&stack, func(a, b int64) int64 { return a + b }); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
		case wasmir.InstrI64Sub:
			if err := evalI64ConstBinOp(&stack, func(a, b int64) int64 { return a - b }); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
		case wasmir.InstrI64Mul:
			if err := evalI64ConstBinOp(&stack, func(a, b int64) int64 { return a * b }); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
		case wasmir.InstrEnd:
		default:
			return Value{}, fmt.Errorf("initializer instruction %d: unsupported instruction kind %d", pc, ins.Kind)
		}
	}
	if len(stack) != 1 {
		return Value{}, fmt.Errorf("initializer left %d values on stack, want 1", len(stack))
	}
	return stack[0], nil
}

// newStructRef allocates a struct object in this instance and returns its
// reference value.
func (inst *Instance) newStructRef(typeIndex uint32, fields []Value) (Value, error) {
	if inst == nil {
		return Value{}, fmt.Errorf("instance is nil")
	}
	if int(typeIndex) >= len(inst.m.Types) || inst.m.Types[typeIndex].Kind != wasmir.TypeDefKindStruct {
		return Value{}, fmt.Errorf("type index %d is not a struct type", typeIndex)
	}
	obj := &gcObject{
		inst:      inst,
		kind:      gcObjectStruct,
		typeIndex: typeIndex,
		fields:    slices.Clone(fields),
	}
	return Value{
		Type: wasmir.RefTypeIndexed(typeIndex, false),
		Ref:  Reference{Kind: RefKindStruct, obj: obj},
	}, nil
}

// newArrayRef allocates an array object in this instance and returns its
// reference value.
func (inst *Instance) newArrayRef(typeIndex uint32, elems []Value) (Value, error) {
	if inst == nil {
		return Value{}, fmt.Errorf("instance is nil")
	}
	if int(typeIndex) >= len(inst.m.Types) || inst.m.Types[typeIndex].Kind != wasmir.TypeDefKindArray {
		return Value{}, fmt.Errorf("type index %d is not an array type", typeIndex)
	}
	obj := &gcObject{
		inst:      inst,
		kind:      gcObjectArray,
		typeIndex: typeIndex,
		elems:     slices.Clone(elems),
	}
	return Value{
		Type: wasmir.RefTypeIndexed(typeIndex, false),
		Ref:  Reference{Kind: RefKindArray, obj: obj},
	}, nil
}

// objectFromRef resolves a struct or array reference to its Go heap object.
func (inst *Instance) objectFromRef(ref Reference) (*gcObject, error) {
	if ref.obj == nil {
		return nil, fmt.Errorf("object reference is nil")
	}
	return ref.obj, nil
}

// evalConstStructNew pops struct field values from a const-expression stack
// and allocates the corresponding struct object.
func (inst *Instance) evalConstStructNew(stack *[]Value, typeIndex uint32) (Value, error) {
	td, err := inst.constTypeDef(typeIndex, wasmir.TypeDefKindStruct)
	if err != nil {
		return Value{}, err
	}
	fieldTypes := make([]wasmir.ValueType, len(td.Fields))
	for i, field := range td.Fields {
		fieldTypes[i] = fieldValueType(field)
	}
	values, err := popConstArgs(stack, fieldTypes)
	if err != nil {
		return Value{}, err
	}
	return inst.newStructRef(typeIndex, normalizeFieldValues(td.Fields, values))
}

// evalConstStructNewDefault allocates a struct object whose fields all hold
// their default values.
func (inst *Instance) evalConstStructNewDefault(typeIndex uint32) (Value, error) {
	td, err := inst.constTypeDef(typeIndex, wasmir.TypeDefKindStruct)
	if err != nil {
		return Value{}, err
	}
	values := make([]Value, len(td.Fields))
	for i, field := range td.Fields {
		value, err := defaultFieldValue(field)
		if err != nil {
			return Value{}, err
		}
		values[i] = value
	}
	return inst.newStructRef(typeIndex, values)
}

// evalConstArrayNew pops an element value and length from a const-expression
// stack and allocates an array object filled with that element.
func (inst *Instance) evalConstArrayNew(stack *[]Value, typeIndex uint32) (Value, error) {
	td, err := inst.constTypeDef(typeIndex, wasmir.TypeDefKindArray)
	if err != nil {
		return Value{}, err
	}
	length, err := popConstI32(stack)
	if err != nil {
		return Value{}, err
	}
	elem, err := popConstValue(stack, fieldValueType(td.ElemField))
	if err != nil {
		return Value{}, err
	}
	elems, err := repeatedFieldValues(td.ElemField, elem, uint32(length))
	if err != nil {
		return Value{}, err
	}
	return inst.newArrayRef(typeIndex, elems)
}

// evalConstArrayNewDefault pops a length from a const-expression stack and
// allocates an array object filled with default element values.
func (inst *Instance) evalConstArrayNewDefault(stack *[]Value, typeIndex uint32) (Value, error) {
	td, err := inst.constTypeDef(typeIndex, wasmir.TypeDefKindArray)
	if err != nil {
		return Value{}, err
	}
	length, err := popConstI32(stack)
	if err != nil {
		return Value{}, err
	}
	elem, err := defaultFieldValue(td.ElemField)
	if err != nil {
		return Value{}, err
	}
	elems, err := repeatedFieldValues(td.ElemField, elem, uint32(length))
	if err != nil {
		return Value{}, err
	}
	return inst.newArrayRef(typeIndex, elems)
}

// evalConstArrayNewFixed pops count element values from a const-expression
// stack and allocates an array object containing those values.
func (inst *Instance) evalConstArrayNewFixed(stack *[]Value, typeIndex uint32, count uint32) (Value, error) {
	td, err := inst.constTypeDef(typeIndex, wasmir.TypeDefKindArray)
	if err != nil {
		return Value{}, err
	}
	if uint64(count) > uint64(int(^uint(0)>>1)) {
		return Value{}, fmt.Errorf("array length %d is too large", count)
	}
	elemTypes := make([]wasmir.ValueType, int(count))
	for i := range elemTypes {
		elemTypes[i] = fieldValueType(td.ElemField)
	}
	elems, err := popConstArgs(stack, elemTypes)
	if err != nil {
		return Value{}, err
	}
	fields := make([]wasmir.FieldType, len(elems))
	for i := range fields {
		fields[i] = td.ElemField
	}
	return inst.newArrayRef(typeIndex, normalizeFieldValues(fields, elems))
}

// constTypeDef returns a type definition for const-expression GC allocation.
func (inst *Instance) constTypeDef(typeIndex uint32, kind wasmir.TypeDefKind) (wasmir.TypeDef, error) {
	if inst == nil || int(typeIndex) >= len(inst.m.Types) {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d out of range", typeIndex)
	}
	td := inst.m.Types[typeIndex]
	if td.Kind != kind {
		return wasmir.TypeDef{}, fmt.Errorf("type index %d has kind %d, want %d", typeIndex, td.Kind, kind)
	}
	return td, nil
}

// evalI32ConstBinOp pops two i32 const-expression operands and pushes the i32
// result of op.
func evalI32ConstBinOp(stack *[]Value, op func(int32, int32) int32) error {
	rhs, err := popConstValue(stack, wasmir.ValueTypeI32)
	if err != nil {
		return err
	}
	lhs, err := popConstValue(stack, wasmir.ValueTypeI32)
	if err != nil {
		return err
	}
	*stack = append(*stack, Value{Type: wasmir.ValueTypeI32, I32: op(lhs.I32, rhs.I32)})
	return nil
}

// evalI64ConstBinOp pops two i64 const-expression operands and pushes the i64
// result of op.
func evalI64ConstBinOp(stack *[]Value, op func(int64, int64) int64) error {
	rhs, err := popConstValue(stack, wasmir.ValueTypeI64)
	if err != nil {
		return err
	}
	lhs, err := popConstValue(stack, wasmir.ValueTypeI64)
	if err != nil {
		return err
	}
	*stack = append(*stack, Value{Type: wasmir.ValueTypeI64, I64: op(lhs.I64, rhs.I64)})
	return nil
}

// popConstArgs removes values from a const-expression stack and returns them
// in evaluation order.
func popConstArgs(stack *[]Value, params []wasmir.ValueType) ([]Value, error) {
	if len(*stack) < len(params) {
		return nil, fmt.Errorf("initializer stack underflow")
	}
	base := len(*stack) - len(params)
	args := (*stack)[base:]
	*stack = (*stack)[:base]
	if err := checkArgs(params, args); err != nil {
		return nil, err
	}
	return args, nil
}

// popConst pops the top const-expression stack value.
func popConst(stack *[]Value) (Value, error) {
	if len(*stack) == 0 {
		return Value{}, fmt.Errorf("initializer stack underflow")
	}
	v := (*stack)[len(*stack)-1]
	*stack = (*stack)[:len(*stack)-1]
	return v, nil
}

// popConstValue pops the top const-expression stack value and verifies its
// value type.
func popConstValue(stack *[]Value, want wasmir.ValueType) (Value, error) {
	v, err := popConst(stack)
	if err != nil {
		return Value{}, err
	}
	if v.Type != want {
		return Value{}, fmt.Errorf("initializer got %s, want %s", v.Type, want)
	}
	return v, nil
}

// popConstI32 pops an i32 const-expression operand.
func popConstI32(stack *[]Value) (int32, error) {
	v, err := popConstValue(stack, wasmir.ValueTypeI32)
	if err != nil {
		return 0, err
	}
	return v.I32, nil
}

// globalGetValue returns the current value of the global at index.
func (inst *Instance) globalGetValue(index uint32) (Value, error) {
	return inst.globalGet(index, false)
}

// globalGet returns the current value of the global at index.
func (inst *Instance) globalGet(index uint32, constExpr bool) (Value, error) {
	g, err := inst.Global(index)
	if err != nil {
		return Value{}, err
	}
	if constExpr && g.mutable {
		return Value{}, fmt.Errorf("global %d is mutable", index)
	}
	return g.Value(), nil
}

// globalSet updates the global at index with value.
func (inst *Instance) globalSet(index uint32, value Value) error {
	g, err := inst.Global(index)
	if err != nil {
		return err
	}
	if !g.mutable {
		return fmt.Errorf("global %d is immutable", index)
	}
	if err := g.Set(value); err != nil {
		return fmt.Errorf("global.set %d: %w", index, err)
	}
	return nil
}

// memoryLoad reads a little-endian integer from an instantiated memory.
func (inst *Instance) memoryLoad(index uint32, address uint64, size uint32) (uint64, error) {
	mem, err := inst.memory(index, address, uint64(size))
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

// memoryStore writes the low-order bytes of value to an instantiated memory in
// little-endian order.
func (inst *Instance) memoryStore(index uint32, address uint64, size uint32, value uint64) error {
	mem, err := inst.memory(index, address, uint64(size))
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

// memorySize returns the current size of an instantiated memory in WebAssembly
// pages.
func (inst *Instance) memorySize(index uint32) (uint64, error) {
	mem, err := inst.memoryInst(index)
	if err != nil {
		return 0, err
	}
	return uint64(len(mem.data) / wasmPageSize), nil
}

// memoryGrow grows an instantiated memory by delta WebAssembly pages.
func (inst *Instance) memoryGrow(index uint32, delta uint64) (uint64, bool, error) {
	mem, err := inst.memoryInst(index)
	if err != nil {
		return 0, false, err
	}
	oldPages := uint64(len(mem.data) / wasmPageSize)
	if delta > ^uint64(0)-oldPages {
		return oldPages, false, nil
	}
	newPages := oldPages + delta
	if newPages > mem.maxPages() {
		return oldPages, false, nil
	}
	if newPages > uint64(int(^uint(0)>>1))/wasmPageSize {
		return oldPages, false, nil
	}
	newSize := int(newPages * wasmPageSize)
	mem.data = append(mem.data, make([]byte, newSize-len(mem.data))...)
	return oldPages, true, nil
}

// maxPages returns the effective WebAssembly page limit for mem.
func (mem *Memory) maxPages() uint64 {
	if mem.max != nil {
		return *mem.max
	}
	if mem.addressType == wasmir.ValueTypeI32 {
		return maxWasm32MemoryPages
	}
	return uint64(int(^uint(0)>>1)) / wasmPageSize
}

// zeroTableValue returns the default null value for a nullable table type.
func zeroTableValue(refType wasmir.ValueType) (Value, error) {
	if !refType.IsRef() {
		return Value{}, fmt.Errorf("table element type %s is not a reference type", refType)
	}
	if !refType.Nullable {
		return Value{}, fmt.Errorf("non-nullable table requires initializer")
	}
	return Value{Type: refType, Ref: Reference{Kind: RefKindNull}}, nil
}

// newTable creates an initialized table instance after the initial value is
// known.
func newTable(t wasmir.Table, init Value) (*Table, error) {
	if t.AddressType != wasmir.ValueTypeI32 && t.AddressType != wasmir.ValueTypeI64 {
		return nil, fmt.Errorf("unsupported table address type %s", t.AddressType)
	}
	if t.Min > maxTableElements {
		return nil, fmt.Errorf("minimum size %d exceeds table resource limit of %d elements", t.Min, maxTableElements)
	}
	if err := checkResults([]wasmir.ValueType{t.RefType}, []Value{init}); err != nil {
		return nil, fmt.Errorf("initializer type mismatch: %w", err)
	}
	elems := make([]Value, int(t.Min))
	for j := range elems {
		elems[j] = init
	}
	return &Table{
		addressType: t.AddressType,
		refType:     t.RefType,
		max:         t.Max,
		elems:       elems,
	}, nil
}

// memoryCopy copies bytes between instantiated memories.
func (inst *Instance) memoryCopy(dstIndex uint32, dstAddress uint64, srcIndex uint32, srcAddress uint64, size uint64) error {
	dst, err := inst.memory(dstIndex, dstAddress, size)
	if err != nil {
		return err
	}
	src, err := inst.memory(srcIndex, srcAddress, size)
	if err != nil {
		return err
	}
	copy(dst, src)
	return nil
}

// memoryFill writes value to a contiguous byte range in an instantiated memory.
func (inst *Instance) memoryFill(index uint32, address uint64, size uint64, value byte) error {
	dst, err := inst.memory(index, address, size)
	if err != nil {
		return err
	}
	for i := range dst {
		dst[i] = value
	}
	return nil
}

// memoryInit copies bytes from a live data segment into an instantiated memory.
func (inst *Instance) memoryInit(memoryIndex uint32, dataIndex uint32, dstAddress uint64, srcOffset uint64, size uint64) error {
	data, err := inst.dataSegment(dataIndex)
	if err != nil {
		return err
	}
	// A dropped segment is an empty source, so only offset 0 and size 0
	// are in bounds. Destination bounds must still be checked below.
	if data.dropped && (srcOffset != 0 || size != 0) {
		return fmt.Errorf("data segment out of bounds")
	}
	if srcOffset > uint64(len(data.init)) || size > uint64(len(data.init))-srcOffset {
		return fmt.Errorf("data segment access out of bounds")
	}
	dst, err := inst.memory(memoryIndex, dstAddress, size)
	if err != nil {
		return err
	}
	start := int(srcOffset)
	copy(dst, data.init[start:start+int(size)])
	return nil
}

// dataDrop marks a data segment unavailable for future memory.init operations.
func (inst *Instance) dataDrop(index uint32) error {
	data, err := inst.dataSegment(index)
	if err != nil {
		return err
	}
	data.dropped = true
	return nil
}

// tableGet returns one reference from an instantiated table.
func (inst *Instance) tableGet(index uint32, elemIndex uint64) (Value, error) {
	table, err := inst.table(index, elemIndex, 1)
	if err != nil {
		return Value{}, err
	}
	return table[0], nil
}

// tableSet writes one reference to an instantiated table.
func (inst *Instance) tableSet(index uint32, elemIndex uint64, value Value) error {
	tableInst, err := inst.tableInst(index)
	if err != nil {
		return err
	}
	if err := checkArgs([]wasmir.ValueType{tableInst.refType}, []Value{value}); err != nil {
		return err
	}
	table, err := inst.table(index, elemIndex, 1)
	if err != nil {
		return err
	}
	table[0] = value
	return nil
}

// tableSize returns the current size of an instantiated table in elements.
func (inst *Instance) tableSize(index uint32) (uint64, error) {
	table, err := inst.tableInst(index)
	if err != nil {
		return 0, err
	}
	return uint64(len(table.elems)), nil
}

// tableGrow grows an instantiated table by delta elements.
func (inst *Instance) tableGrow(index uint32, init Value, delta uint64) (uint64, bool, error) {
	table, err := inst.tableInst(index)
	if err != nil {
		return 0, false, err
	}
	if err := checkArgs([]wasmir.ValueType{table.refType}, []Value{init}); err != nil {
		return 0, false, err
	}
	oldSize := uint64(len(table.elems))
	if delta > ^uint64(0)-oldSize {
		return oldSize, false, nil
	}
	newSize := oldSize + delta
	if newSize > maxTableElements || newSize > table.maxElements() {
		return oldSize, false, nil
	}
	if table.addressType == wasmir.ValueTypeI32 && newSize > uint64(^uint32(0)) {
		return oldSize, false, nil
	}
	if newSize > uint64(int(^uint(0)>>1)) {
		return oldSize, false, nil
	}
	table.elems = append(table.elems, make([]Value, int(delta))...)
	for i := int(oldSize); i < len(table.elems); i++ {
		table.elems[i] = init
	}
	return oldSize, true, nil
}

// tableFill writes value to a contiguous element range in an instantiated
// table.
func (inst *Instance) tableFill(index uint32, elemIndex uint64, size uint64, value Value) error {
	tableInst, err := inst.tableInst(index)
	if err != nil {
		return err
	}
	if err := checkArgs([]wasmir.ValueType{tableInst.refType}, []Value{value}); err != nil {
		return err
	}
	dst, err := inst.table(index, elemIndex, size)
	if err != nil {
		return err
	}
	for i := range dst {
		dst[i] = value
	}
	return nil
}

// tableCopy copies elements between instantiated tables.
func (inst *Instance) tableCopy(dstIndex uint32, dstElemIndex uint64, srcIndex uint32, srcElemIndex uint64, size uint64) error {
	dst, err := inst.table(dstIndex, dstElemIndex, size)
	if err != nil {
		return err
	}
	src, err := inst.table(srcIndex, srcElemIndex, size)
	if err != nil {
		return err
	}
	copy(dst, src)
	return nil
}

// tableInit copies references from a live element segment into an instantiated
// table.
func (inst *Instance) tableInit(tableIndex uint32, elemIndex uint32, dstElemIndex uint64, srcOffset uint64, size uint64) error {
	elem, err := inst.elemSegment(elemIndex)
	if err != nil {
		return err
	}
	// A dropped segment is an empty source, so only offset 0 and size 0
	// are in bounds. Destination bounds must still be checked below.
	if elem.dropped && (srcOffset != 0 || size != 0) {
		return fmt.Errorf("element segment out of bounds")
	}
	if srcOffset > uint64(len(elem.values)) || size > uint64(len(elem.values))-srcOffset {
		return fmt.Errorf("element segment access out of bounds")
	}
	dst, err := inst.table(tableIndex, dstElemIndex, size)
	if err != nil {
		return err
	}
	start := int(srcOffset)
	copy(dst, elem.values[start:start+int(size)])
	return nil
}

// elemDrop marks an element segment unavailable for future table.init
// operations.
func (inst *Instance) elemDrop(index uint32) error {
	elem, err := inst.elemSegment(index)
	if err != nil {
		return err
	}
	elem.dropped = true
	return nil
}

// dataSegment resolves a data index to the mutable instantiated data segment
// state.
func (inst *Instance) dataSegment(index uint32) (*dataInst, error) {
	if int(index) >= len(inst.data) {
		return nil, fmt.Errorf("data segment index %d out of range", index)
	}
	return &inst.data[index], nil
}

// elemSegment resolves an element index to the mutable instantiated element
// segment state.
func (inst *Instance) elemSegment(index uint32) (*elemInst, error) {
	if int(index) >= len(inst.elems) {
		return nil, fmt.Errorf("element segment index %d out of range", index)
	}
	return &inst.elems[index], nil
}

// Memory returns the instantiated memory at index.
func (inst *Instance) Memory(index uint32) (*Memory, error) {
	if int(index) >= len(inst.memories) {
		return nil, fmt.Errorf("memory index %d out of range", index)
	}
	return inst.memories[index], nil
}

// Table returns the instantiated table at index.
func (inst *Instance) Table(index uint32) (*Table, error) {
	if int(index) >= len(inst.tables) {
		return nil, fmt.Errorf("table index %d out of range", index)
	}
	return inst.tables[index], nil
}

// memoryInst resolves a memory index to the mutable instantiated memory state.
func (inst *Instance) memoryInst(index uint32) (*Memory, error) {
	return inst.Memory(index)
}

// memoryAddressType returns the address operand type of the indexed memory.
func (inst *Instance) memoryAddressType(index uint32) (wasmir.ValueType, error) {
	mem, err := inst.memoryInst(index)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	return mem.addressType, nil
}

// tableInst resolves a table index to the mutable instantiated table state.
func (inst *Instance) tableInst(index uint32) (*Table, error) {
	return inst.Table(index)
}

// tableAddressType returns the address operand type of the indexed table.
func (inst *Instance) tableAddressType(index uint32) (wasmir.ValueType, error) {
	table, err := inst.tableInst(index)
	if err != nil {
		return wasmir.ValueType{}, err
	}
	return table.addressType, nil
}

// table returns the in-bounds element window addressed by a VM table operation.
func (inst *Instance) table(index uint32, elemIndex uint64, size uint64) ([]Value, error) {
	tableInst, err := inst.tableInst(index)
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
func (inst *Instance) memory(index uint32, address uint64, size uint64) ([]byte, error) {
	memInst, err := inst.memoryInst(index)
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
