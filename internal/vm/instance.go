package vm

import (
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/eliben/watgo/wasmir"
)

const wasmPageSize = 64 * 1024

// Instance is the VM-owned execution state for one instantiated module.
type Instance struct {
	m        *wasmir.Module
	funcs    []funcInst
	globals  []globalInst
	memories []memoryInst
	tables   []tableInst
	data     []dataInst
	elems    []elemInst
	resolver Resolver
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

// globalInst is one instantiated global in the module's global index space.
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
type memoryInst struct {
	// addressType is the validated address type for this memory. The VM
	// currently supports only i32-addressed memories.
	addressType wasmir.ValueType

	// max is the optional declared maximum size in WebAssembly pages.
	max *uint64

	// data is the mutable linear-memory byte buffer. Its length is always a
	// whole number of WebAssembly pages.
	data []byte
}

// tableInst is one instantiated table in the module's table index space.
type tableInst struct {
	// addressType is the validated index type for this table. The VM currently
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
func Instantiate(m *wasmir.Module, resolver Resolver) (*Instance, error) {
	if m == nil {
		return nil, fmt.Errorf("module is nil")
	}
	inst := &Instance{m: m, resolver: resolver}
	if err := inst.buildMemories(); err != nil {
		return nil, err
	}
	inst.buildDataSegments()
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
	if err := inst.applyDataSegments(); err != nil {
		return nil, err
	}
	if err := inst.applyElementSegments(); err != nil {
		return nil, err
	}
	return inst, nil
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
	if fn.imported {
		if inst.resolver == nil {
			return nil, fmt.Errorf("resolver is nil")
		}
		results, err := inst.resolver.CallFunc(index, args)
		if err != nil {
			return nil, err
		}
		if err := checkResults(ft.Results, results); err != nil {
			return nil, fmt.Errorf("func[%d]: %w", index, err)
		}
		return results, nil
	}
	return executeFunction(fn.code, ft, args, inst)
}

// FuncType returns the signature of the function at index.
func (inst *Instance) FuncType(index uint32) (wasmir.TypeDef, error) {
	if int(index) >= len(inst.funcs) {
		return wasmir.TypeDef{}, fmt.Errorf("call function index %d out of range", index)
	}
	return inst.funcType(inst.funcs[index].typeIdx)
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
			return fmt.Errorf("unsupported import %q.%q kind %d", imp.Module, imp.Name, imp.Kind)
		}
		if _, err := inst.funcType(imp.TypeIdx); err != nil {
			return fmt.Errorf("import %q.%q has invalid function type: %w", imp.Module, imp.Name, err)
		}
		inst.funcs = append(inst.funcs, funcInst{typeIdx: imp.TypeIdx, imported: true})
	}
	for i := range inst.m.Funcs {
		f := &inst.m.Funcs[i]
		code, err := compileFunction(f)
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
		if g.ImportModule != "" || g.ImportName != "" {
			return fmt.Errorf("unsupported global import %q.%q", g.ImportModule, g.ImportName)
		}
		value, err := inst.evalConstExpr(g.Init, true)
		if err != nil {
			return fmt.Errorf("global[%d]: %w", i, err)
		}
		if err := checkResults([]wasmir.ValueType{g.Type}, []Value{value}); err != nil {
			return fmt.Errorf("global[%d]: initializer type mismatch: %w", i, err)
		}
		inst.globals = append(inst.globals, globalInst{typ: g.Type, mutable: g.Mutable, value: value})
	}
	return nil
}

// buildMemories creates the instance memory address space.
func (inst *Instance) buildMemories() error {
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
func (inst *Instance) buildTables() error {
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
func (inst *Instance) tableInitialValue(t wasmir.Table) (Value, error) {
	if len(t.Init) == 0 {
		if !t.RefType.Nullable {
			return Value{}, fmt.Errorf("non-nullable table requires initializer")
		}
		return Value{Type: t.RefType, Ref: Reference{Kind: RefKindNull}}, nil
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

// dataSegmentOffset evaluates the active data segment offset as an i32 memory
// address.
func (inst *Instance) dataSegmentOffset(seg wasmir.DataSegment) (uint64, error) {
	if len(seg.OffsetExpr) > 0 {
		v, err := inst.evalConstExpr(seg.OffsetExpr, true)
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

// elementSegmentOffset evaluates the active element segment offset as an i32
// table index.
func (inst *Instance) elementSegmentOffset(seg wasmir.ElementSegment) (uint64, error) {
	if len(seg.OffsetExpr) > 0 {
		v, err := inst.evalConstExpr(seg.OffsetExpr, true)
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
func (inst *Instance) elementSegmentValues(seg wasmir.ElementSegment) ([]Value, error) {
	if len(seg.FuncIndices) > 0 {
		values := make([]Value, len(seg.FuncIndices))
		for i, funcIndex := range seg.FuncIndices {
			if _, err := inst.FuncType(funcIndex); err != nil {
				return nil, err
			}
			values[i] = Value{Type: wasmir.RefTypeFunc(false), Ref: Reference{Kind: RefKindFunc, FuncIndex: funcIndex}}
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

// globalGetValue returns the current value of the global at index.
func (inst *Instance) globalGetValue(index uint32) (Value, error) {
	return inst.globalGet(index, false)
}

// globalGet returns the current value of the global at index.
func (inst *Instance) globalGet(index uint32, constExpr bool) (Value, error) {
	if int(index) >= len(inst.globals) {
		return Value{}, fmt.Errorf("global index %d out of range", index)
	}
	g := inst.globals[index]
	if constExpr && g.mutable {
		return Value{}, fmt.Errorf("global %d is mutable", index)
	}
	return g.value, nil
}

// globalSet updates the global at index with value.
func (inst *Instance) globalSet(index uint32, value Value) error {
	if int(index) >= len(inst.globals) {
		return fmt.Errorf("global index %d out of range", index)
	}
	g := &inst.globals[index]
	if !g.mutable {
		return fmt.Errorf("global %d is immutable", index)
	}
	if err := checkArgs([]wasmir.ValueType{g.typ}, []Value{value}); err != nil {
		return fmt.Errorf("global.set %d: %w", index, err)
	}
	g.value = value
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
	if data.dropped {
		return fmt.Errorf("data segment %d is dropped", dataIndex)
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
	if table.max != nil && newSize > *table.max {
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
	if elem.dropped {
		return fmt.Errorf("element segment %d is dropped", elemIndex)
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

// memoryInst resolves a memory index to the mutable instantiated memory state.
func (inst *Instance) memoryInst(index uint32) (*memoryInst, error) {
	if int(index) >= len(inst.memories) {
		return nil, fmt.Errorf("memory index %d out of range", index)
	}
	return &inst.memories[index], nil
}

// tableInst resolves a table index to the mutable instantiated table state.
func (inst *Instance) tableInst(index uint32) (*tableInst, error) {
	if int(index) >= len(inst.tables) {
		return nil, fmt.Errorf("table index %d out of range", index)
	}
	return &inst.tables[index], nil
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
