package vm

import (
	"fmt"
	"math"
	"math/bits"

	"github.com/eliben/watgo/wasmir"
)

const (
	// minInt32/maxInt32 and minInt64/maxInt64 are used when saturating
	// float-to-signed-integer conversions clamp out-of-range values.
	minInt32 = -1 << 31
	maxInt32 = 1<<31 - 1
	minInt64 = -1 << 63
	maxInt64 = 1<<63 - 1

	// The float thresholds below describe the half-open valid ranges for
	// trapping float-to-integer truncation:
	//
	//   - signed i32: [minInt32Float, two31Float)
	//   - unsigned i32: [0, two32Float)
	//   - signed i64: [minInt64Float, two63Float)
	//   - unsigned i64: [0, two64Float)
	//
	// They are also used as saturation cutoffs. Powers of two are exactly
	// representable in binary floating point, which makes these comparisons
	// stable across f32 and f64 inputs after promotion to float64.
	minInt32Float = -2147483648.0
	two31Float    = 2147483648.0
	two32Float    = 4294967296.0
	minInt64Float = -9223372036854775808.0
	two63Float    = 9223372036854775808.0
	two64Float    = 18446744073709551616.0
)

// instructionError adds interpreter location to low-level execution errors.
//
// The helpers below report compact errors such as stack underflow or operand
// type mismatch. Wrapping them with pc and opcode here makes those errors
// useful at the VM boundary, while keeping the no-error path free of formatting
// or allocation work.
type instructionError struct {
	pc   int
	kind wasmir.InstrKind
	err  error
}

func (e instructionError) Error() string {
	return fmt.Sprintf("pc %d %s: %v", e.pc, instrName(e.kind), e.err)
}

func (e instructionError) Unwrap() error {
	return e.err
}

// instructionErrorAt is called only on instruction failure paths.
func instructionErrorAt(pc int, kind wasmir.InstrKind, err error) error {
	return instructionError{pc: pc, kind: kind, err: err}
}

// Value is one runtime WebAssembly value.
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

	// Ref is the payload for reference-typed values.
	Ref Reference
}

// RefKind classifies the reference payload carried by a runtime Value.
type RefKind uint8

const (
	// RefKindNull is the null reference.
	RefKindNull RefKind = iota

	// RefKindFunc is a reference to a function in the instance function index
	// space.
	RefKindFunc
)

// Reference is one runtime reference value.
type Reference struct {
	// Kind identifies the concrete reference payload.
	Kind RefKind

	// FuncIndex is set when Kind is RefKindFunc.
	FuncIndex uint32
}

// Resolver is the VM's view of the instantiated module environment.
//
// The VM owns execution mechanics for compiled module-defined functions, but
// it does not own host-visible instance state. Any instruction that may cross
// that boundary, such as calls, global access, or memory access, goes through
// Resolver.
type Resolver interface {
	// FuncType returns the signature of the function at index.
	FuncType(index uint32) (wasmir.TypeDef, error)

	// CallFunc invokes the function at index with already popped arguments in
	// parameter order.
	CallFunc(index uint32, args []Value) ([]Value, error)

	// GlobalGet returns the current value of the global at index.
	GlobalGet(index uint32) (Value, error)

	// GlobalSet updates the global at index with value.
	GlobalSet(index uint32, value Value) error

	// MemoryLoad reads size bytes from memory at address and returns them as a
	// little-endian integer in the low bits.
	MemoryLoad(index uint32, address uint64, size uint32) (uint64, error)

	// MemoryStore writes size low-order bytes of value to memory at address in
	// little-endian order.
	MemoryStore(index uint32, address uint64, size uint32, value uint64) error

	// MemorySize returns the current memory size in WebAssembly pages.
	MemorySize(index uint32) (uint64, error)

	// MemoryGrow grows memory by delta pages. It returns the old memory size in
	// pages when growth succeeds, and ok=false when growth is rejected.
	MemoryGrow(index uint32, delta uint64) (oldPages uint64, ok bool, err error)

	// MemoryCopy copies size bytes between instantiated memories. The copy must
	// have memmove semantics when the source and destination overlap.
	MemoryCopy(dstIndex uint32, dstAddress uint64, srcIndex uint32, srcAddress uint64, size uint64) error

	// MemoryFill writes value to size bytes of an instantiated memory.
	MemoryFill(index uint32, address uint64, size uint64, value byte) error

	// MemoryInit copies size bytes from a passive data segment into memory.
	MemoryInit(memoryIndex uint32, dataIndex uint32, dstAddress uint64, srcOffset uint64, size uint64) error

	// DataDrop marks a passive data segment unavailable for future memory.init
	// operations.
	DataDrop(index uint32) error

	// TableGet returns the reference at elemIndex in table index.
	TableGet(index uint32, elemIndex uint64) (Value, error)

	// TableSet updates the reference at elemIndex in table index.
	TableSet(index uint32, elemIndex uint64, value Value) error

	// TableSize returns the current table size in elements.
	TableSize(index uint32) (uint64, error)

	// TableGrow grows a table by delta elements, initializing new slots with
	// init. It returns the old table size when growth succeeds, and ok=false
	// when growth is rejected.
	TableGrow(index uint32, init Value, delta uint64) (oldSize uint64, ok bool, err error)

	// TableFill writes value to size elements of an instantiated table.
	TableFill(index uint32, elemIndex uint64, size uint64, value Value) error

	// TableCopy copies size elements between instantiated tables. The copy must
	// have memmove semantics when the source and destination overlap.
	TableCopy(dstIndex uint32, dstElemIndex uint64, srcIndex uint32, srcElemIndex uint64, size uint64) error

	// TableInit copies size elements from an element segment into a table.
	TableInit(tableIndex uint32, elemIndex uint32, dstElemIndex uint64, srcOffset uint64, size uint64) error

	// ElemDrop marks an element segment unavailable for future table.init
	// operations.
	ElemDrop(index uint32) error
}

// CheckArgs verifies call argument count and value types.
func CheckArgs(params []wasmir.ValueType, args []Value) error {
	if len(args) != len(params) {
		return fmt.Errorf("got %d arguments, want %d", len(args), len(params))
	}
	for i, want := range params {
		if !runtimeTypeMatches(args[i].Type, want) {
			return fmt.Errorf("argument %d has type %s, want %s", i, args[i].Type, want)
		}
	}
	return nil
}

// CheckResults verifies result count and value types.
func CheckResults(want []wasmir.ValueType, got []Value) error {
	if len(got) != len(want) {
		return fmt.Errorf("got %d results, want %d", len(got), len(want))
	}
	for i := range want {
		if !runtimeTypeMatches(got[i].Type, want[i]) {
			return fmt.Errorf("result %d has type %s, want %s", i, got[i].Type, want[i])
		}
	}
	return nil
}

// runtimeTypeMatches checks runtime value compatibility after validation has
// already enforced the full WebAssembly static typing rules.
func runtimeTypeMatches(got, want wasmir.ValueType) bool {
	if got == want {
		return true
	}
	return got.IsRef() && want.IsRef()
}

// executor is one active module-defined function frame.
type executor struct {
	// fn is the compiled function being interpreted by this frame.
	fn *Function

	// ft is fn's validated WebAssembly signature.
	ft wasmir.TypeDef

	// resolver connects this VM frame to the instantiated module environment:
	// function index space, globals, memories, and eventually other
	// host-visible state such as tables.
	resolver Resolver

	// pc is the current instruction index in fn.code. It is stored on the frame
	// so error wrapping and control-flow instructions can share the same
	// location state.
	pc int

	// locals is the function's local array: parameters first, followed by
	// zero-initialized non-parameter locals.
	locals []Value

	// stack is the operand stack for this frame.
	stack []Value
}

// ExecuteFunction interprets one compiled module-defined function body.
func ExecuteFunction(fn *Function, ft wasmir.TypeDef, args []Value, resolver Resolver) ([]Value, error) {
	if fn == nil {
		return nil, fmt.Errorf("defined function has no compiled code")
	}

	e := executor{
		fn:       fn,
		ft:       ft,
		resolver: resolver,
		stack:    make([]Value, 0),
	}
	if err := e.initLocals(args); err != nil {
		return nil, err
	}
	return e.run()
}

// initLocals builds the frame's local array from call arguments and declared
// non-parameter locals.
func (e *executor) initLocals(args []Value) error {
	e.locals = append([]Value{}, args...)
	for _, vt := range e.fn.locals {
		v, err := zeroValue(vt)
		if err != nil {
			return err
		}
		e.locals = append(e.locals, v)
	}
	return nil
}

// run interprets fn.code until it reaches return, the final end instruction, or
// an execution error.
func (e *executor) run() ([]Value, error) {
	for e.pc = 0; e.pc < len(e.fn.code); e.pc++ {
		ins := e.fn.code[e.pc]
		switch ins.kind {
		case wasmir.InstrBlock, wasmir.InstrLoop, wasmir.InstrNop:
		case wasmir.InstrUnreachable:
			return nil, e.instructionError(fmt.Errorf("unreachable executed"))
		case wasmir.InstrIf:
			// The condition has already been validated as i32. A true condition
			// enters the then arm. A false condition skips to the else marker
			// if present, or to the matching end otherwise.
			cond, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if cond == 0 {
				e.pc = ins.target
				continue
			}
		case wasmir.InstrElse:
			// Reaching else normally means the then arm completed without
			// branching. Skip the else arm.
			e.pc = ins.target
		case wasmir.InstrLocalGet:
			if int(ins.index) >= len(e.locals) {
				return nil, e.instructionError(fmt.Errorf("local index %d out of range", ins.index))
			}
			e.push(e.locals[ins.index])
		case wasmir.InstrLocalSet:
			if int(ins.index) >= len(e.locals) {
				return nil, e.instructionError(fmt.Errorf("local index %d out of range", ins.index))
			}
			v, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if !runtimeTypeMatches(v.Type, e.locals[ins.index].Type) {
				return nil, e.instructionError(fmt.Errorf("local.set %d got %s, want %s", ins.index, v.Type, e.locals[ins.index].Type))
			}
			e.locals[ins.index] = v
		case wasmir.InstrLocalTee:
			if int(ins.index) >= len(e.locals) {
				return nil, e.instructionError(fmt.Errorf("local index %d out of range", ins.index))
			}
			v, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if !runtimeTypeMatches(v.Type, e.locals[ins.index].Type) {
				return nil, e.instructionError(fmt.Errorf("local.tee %d got %s, want %s", ins.index, v.Type, e.locals[ins.index].Type))
			}
			e.locals[ins.index] = v
			e.push(v)
		case wasmir.InstrGlobalGet:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			v, err := e.resolver.GlobalGet(ins.index)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(v)
		case wasmir.InstrGlobalSet:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			v, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.GlobalSet(ins.index, v); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrI32Const:
			e.push(Value{Type: wasmir.ValueTypeI32, I32: int32(ins.bits)})
		case wasmir.InstrI32Load, wasmir.InstrI32Load8S, wasmir.InstrI32Load8U,
			wasmir.InstrI32Load16S, wasmir.InstrI32Load16U:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			size := memoryAccessSize(ins.kind)
			raw, err := e.resolver.MemoryLoad(ins.index, effective, size)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: extendI32Load(ins.kind, raw)})
		case wasmir.InstrI32Store, wasmir.InstrI32Store8, wasmir.InstrI32Store16:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			value, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryStore(ins.index, effective, memoryAccessSize(ins.kind), uint64(uint32(value))); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrI64Load, wasmir.InstrI64Load8S, wasmir.InstrI64Load8U,
			wasmir.InstrI64Load16S, wasmir.InstrI64Load16U,
			wasmir.InstrI64Load32S, wasmir.InstrI64Load32U:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			size := memoryAccessSize(ins.kind)
			raw, err := e.resolver.MemoryLoad(ins.index, effective, size)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI64, I64: extendI64Load(ins.kind, raw)})
		case wasmir.InstrI64Store, wasmir.InstrI64Store8, wasmir.InstrI64Store16, wasmir.InstrI64Store32:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			value, err := e.popI64()
			if err != nil {
				return nil, e.instructionError(err)
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryStore(ins.index, effective, memoryAccessSize(ins.kind), uint64(value)); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrF32Load:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			raw, err := e.resolver.MemoryLoad(ins.index, effective, 4)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeF32, F32: math.Float32frombits(uint32(raw))})
		case wasmir.InstrF32Store:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			value, err := e.popF32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryStore(ins.index, effective, 4, uint64(math.Float32bits(value))); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrF64Load:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			raw, err := e.resolver.MemoryLoad(ins.index, effective, 8)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeF64, F64: math.Float64frombits(raw)})
		case wasmir.InstrF64Store:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			value, err := e.popF64()
			if err != nil {
				return nil, e.instructionError(err)
			}
			addr, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			effective, err := memoryAddress(addr, uint64(ins.bits))
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryStore(ins.index, effective, 8, math.Float64bits(value)); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrMemorySize:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			pages, err := e.resolver.MemorySize(ins.index)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: int32(uint32(pages))})
		case wasmir.InstrMemoryGrow:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			delta, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			oldPages, ok, err := e.resolver.MemoryGrow(ins.index, uint64(uint32(delta)))
			if err != nil {
				return nil, e.instructionError(err)
			}
			if !ok {
				e.push(Value{Type: wasmir.ValueTypeI32, I32: -1})
				continue
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: int32(uint32(oldPages))})
		case wasmir.InstrMemoryCopy:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			src, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			dst, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryCopy(ins.index, uint64(uint32(dst)), uint32(ins.bits), uint64(uint32(src)), uint64(uint32(size))); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrMemoryFill:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			value, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			dst, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryFill(ins.index, uint64(uint32(dst)), uint64(uint32(size)), byte(value)); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrMemoryInit:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			src, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			dst, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.MemoryInit(ins.index, uint32(ins.bits), uint64(uint32(dst)), uint64(uint32(src)), uint64(uint32(size))); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrDataDrop:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			if err := e.resolver.DataDrop(ins.index); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrTableSize:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.resolver.TableSize(ins.index)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: int32(uint32(size))})
		case wasmir.InstrTableGet:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			elemIndex, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			v, err := e.resolver.TableGet(ins.index, uint64(uint32(elemIndex)))
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(v)
		case wasmir.InstrTableSet:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			v, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			elemIndex, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.TableSet(ins.index, uint64(uint32(elemIndex)), v); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrTableGrow:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			delta, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			init, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			oldSize, ok, err := e.resolver.TableGrow(ins.index, init, uint64(uint32(delta)))
			if err != nil {
				return nil, e.instructionError(err)
			}
			if !ok {
				e.push(Value{Type: wasmir.ValueTypeI32, I32: -1})
				continue
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: int32(uint32(oldSize))})
		case wasmir.InstrTableFill:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			value, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			dst, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.TableFill(ins.index, uint64(uint32(dst)), uint64(uint32(size)), value); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrTableCopy:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			src, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			dst, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.TableCopy(ins.index, uint64(uint32(dst)), uint32(ins.bits), uint64(uint32(src)), uint64(uint32(size))); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrTableInit:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			size, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			src, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			dst, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := e.resolver.TableInit(ins.index, uint32(ins.bits), uint64(uint32(dst)), uint64(uint32(src)), uint64(uint32(size))); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrElemDrop:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			if err := e.resolver.ElemDrop(ins.index); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul,
			wasmir.InstrI32DivS, wasmir.InstrI32DivU, wasmir.InstrI32RemS, wasmir.InstrI32RemU,
			wasmir.InstrI32And, wasmir.InstrI32Or, wasmir.InstrI32Xor,
			wasmir.InstrI32Shl, wasmir.InstrI32ShrS, wasmir.InstrI32ShrU,
			wasmir.InstrI32Rotl, wasmir.InstrI32Rotr,
			wasmir.InstrI32Eq, wasmir.InstrI32Ne,
			wasmir.InstrI32LtS, wasmir.InstrI32LtU, wasmir.InstrI32LeS, wasmir.InstrI32LeU,
			wasmir.InstrI32GtS, wasmir.InstrI32GtU, wasmir.InstrI32GeS, wasmir.InstrI32GeU:
			v, err := e.evalI32Binary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrI32Eqz:
			v, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: boolI32(v == 0)})
		case wasmir.InstrI32Clz, wasmir.InstrI32Ctz, wasmir.InstrI32Popcnt,
			wasmir.InstrI32Extend8S, wasmir.InstrI32Extend16S:
			v, err := e.evalI32Unary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrI64Const:
			e.push(Value{Type: wasmir.ValueTypeI64, I64: ins.bits})
		case wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul,
			wasmir.InstrI64DivS, wasmir.InstrI64DivU, wasmir.InstrI64RemS, wasmir.InstrI64RemU,
			wasmir.InstrI64And, wasmir.InstrI64Or, wasmir.InstrI64Xor,
			wasmir.InstrI64Shl, wasmir.InstrI64ShrS, wasmir.InstrI64ShrU,
			wasmir.InstrI64Rotl, wasmir.InstrI64Rotr:
			v, err := e.evalI64Binary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI64, I64: v})
		case wasmir.InstrI64Eq, wasmir.InstrI64Ne,
			wasmir.InstrI64LtS, wasmir.InstrI64LtU, wasmir.InstrI64LeS, wasmir.InstrI64LeU,
			wasmir.InstrI64GtS, wasmir.InstrI64GtU, wasmir.InstrI64GeS, wasmir.InstrI64GeU:
			v, err := e.evalI64Compare(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrI64Eqz:
			v, err := e.popI64()
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: boolI32(v == 0)})
		case wasmir.InstrI64Clz, wasmir.InstrI64Ctz, wasmir.InstrI64Popcnt,
			wasmir.InstrI64Extend8S, wasmir.InstrI64Extend16S, wasmir.InstrI64Extend32S:
			v, err := e.evalI64Unary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI64, I64: v})
		case wasmir.InstrI32WrapI64,
			wasmir.InstrI32TruncF32S, wasmir.InstrI32TruncF32U,
			wasmir.InstrI32TruncF64S, wasmir.InstrI32TruncF64U,
			wasmir.InstrI32TruncSatF32S, wasmir.InstrI32TruncSatF32U,
			wasmir.InstrI32TruncSatF64S, wasmir.InstrI32TruncSatF64U,
			wasmir.InstrI64ExtendI32S, wasmir.InstrI64ExtendI32U,
			wasmir.InstrI64TruncF32S, wasmir.InstrI64TruncF32U,
			wasmir.InstrI64TruncF64S, wasmir.InstrI64TruncF64U,
			wasmir.InstrI64TruncSatF32S, wasmir.InstrI64TruncSatF32U,
			wasmir.InstrI64TruncSatF64S, wasmir.InstrI64TruncSatF64U,
			wasmir.InstrF32ConvertI32S, wasmir.InstrF32ConvertI32U,
			wasmir.InstrF32ConvertI64S, wasmir.InstrF32ConvertI64U,
			wasmir.InstrF32DemoteF64,
			wasmir.InstrF64ConvertI32S, wasmir.InstrF64ConvertI32U,
			wasmir.InstrF64ConvertI64S, wasmir.InstrF64ConvertI64U,
			wasmir.InstrF64PromoteF32,
			wasmir.InstrI32ReinterpretF32, wasmir.InstrI64ReinterpretF64,
			wasmir.InstrF32ReinterpretI32, wasmir.InstrF64ReinterpretI64:
			v, err := e.evalConversion(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(v)
		case wasmir.InstrF32Const:
			e.push(Value{Type: wasmir.ValueTypeF32, F32: math.Float32frombits(uint32(ins.bits))})
		case wasmir.InstrF32Abs, wasmir.InstrF32Neg, wasmir.InstrF32Sqrt,
			wasmir.InstrF32Ceil, wasmir.InstrF32Floor, wasmir.InstrF32Trunc, wasmir.InstrF32Nearest:
			v, err := e.evalF32Unary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeF32, F32: v})
		case wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div,
			wasmir.InstrF32Min, wasmir.InstrF32Max, wasmir.InstrF32Copysign:
			v, err := e.evalF32Binary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeF32, F32: v})
		case wasmir.InstrF32Eq, wasmir.InstrF32Ne,
			wasmir.InstrF32Lt, wasmir.InstrF32Le, wasmir.InstrF32Gt, wasmir.InstrF32Ge:
			v, err := e.evalF32Compare(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrF64Const:
			e.push(Value{Type: wasmir.ValueTypeF64, F64: math.Float64frombits(uint64(ins.bits))})
		case wasmir.InstrF64Abs, wasmir.InstrF64Neg, wasmir.InstrF64Sqrt,
			wasmir.InstrF64Ceil, wasmir.InstrF64Floor, wasmir.InstrF64Trunc, wasmir.InstrF64Nearest:
			v, err := e.evalF64Unary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeF64, F64: v})
		case wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div,
			wasmir.InstrF64Min, wasmir.InstrF64Max, wasmir.InstrF64Copysign:
			v, err := e.evalF64Binary(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeF64, F64: v})
		case wasmir.InstrF64Eq, wasmir.InstrF64Ne,
			wasmir.InstrF64Lt, wasmir.InstrF64Le, wasmir.InstrF64Gt, wasmir.InstrF64Ge:
			v, err := e.evalF64Compare(ins.kind)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrDrop:
			if _, err := e.pop(); err != nil {
				return nil, e.instructionError(err)
			}
		case wasmir.InstrSelect:
			cond, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			v2, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			v1, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if v1.Type != v2.Type {
				return nil, e.instructionError(fmt.Errorf("select got %s and %s operands", v1.Type, v2.Type))
			}
			if cond != 0 {
				e.push(v1)
			} else {
				e.push(v2)
			}
		case wasmir.InstrRefNull:
			refTypeIndex := int(ins.index)
			if refTypeIndex >= len(e.fn.refTypes) {
				return nil, e.instructionError(fmt.Errorf("ref.null type index %d out of range", ins.index))
			}
			e.push(Value{Type: e.fn.refTypes[refTypeIndex], Ref: Reference{Kind: RefKindNull}})
		case wasmir.InstrRefFunc:
			if e.resolver == nil {
				return nil, e.instructionError(fmt.Errorf("resolver is nil"))
			}
			if _, err := e.resolver.FuncType(ins.index); err != nil {
				return nil, e.instructionError(err)
			}
			e.push(Value{Type: wasmir.RefTypeFunc(false), Ref: Reference{Kind: RefKindFunc, FuncIndex: ins.index}})
		case wasmir.InstrRefIsNull:
			v, err := e.pop()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if !v.Type.IsRef() {
				return nil, e.instructionError(fmt.Errorf("ref.is_null got %s operand", v.Type))
			}
			e.push(Value{Type: wasmir.ValueTypeI32, I32: boolI32(v.Ref.Kind == RefKindNull)})
		case wasmir.InstrCall:
			results, err := e.callFunction(ins.index)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.stack = append(e.stack, results...)
		case wasmir.InstrReturnCall:
			results, err := e.callFunction(ins.index)
			if err != nil {
				return nil, e.instructionError(err)
			}
			if err := CheckResults(e.ft.Results, results); err != nil {
				return nil, e.instructionError(err)
			}
			return results, nil
		case wasmir.InstrBr:
			e.pc = ins.target
		case wasmir.InstrBrIf:
			// br_if consumes only the condition. Any branch result values are
			// already below it on the operand stack and are left there for the
			// target block's end to consume.
			cond, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			if cond != 0 {
				e.pc = ins.target
			}
		case wasmir.InstrBrTable:
			// br_table consumes only the i32 selector. Branch result values, if
			// any, are already below it on the operand stack and are left there
			// for the selected target block's end to consume.
			selector, err := e.popI32()
			if err != nil {
				return nil, e.instructionError(err)
			}
			tableIndex := int(ins.index)
			if tableIndex >= len(e.fn.branchTables) {
				return nil, e.instructionError(fmt.Errorf("br_table index %d out of range", ins.index))
			}
			targets := e.fn.branchTables[tableIndex]
			if len(targets) == 0 {
				return nil, e.instructionError(fmt.Errorf("br_table has no default target"))
			}
			targetIndex := uint32(selector)
			defaultIndex := len(targets) - 1
			if uint64(targetIndex) < uint64(defaultIndex) {
				e.pc = targets[int(targetIndex)]
			} else {
				e.pc = targets[defaultIndex]
			}
		case wasmir.InstrReturn:
			results, err := e.popResults(e.ft.Results)
			if err != nil {
				return nil, e.instructionError(err)
			}
			return results, nil
		case wasmir.InstrEnd:
			if e.pc != len(e.fn.code)-1 {
				// Non-final end closes structured control. Branch targets skip
				// over it, and ordinary fallthrough can treat it as a no-op
				// because validation has already established the operand stack
				// contract.
				continue
			}
			results, err := e.popResults(e.ft.Results)
			if err != nil {
				return nil, e.instructionError(err)
			}
			return results, nil
		default:
			return nil, e.instructionError(fmt.Errorf("unsupported instruction"))
		}
	}
	return nil, fmt.Errorf("function ended without end")
}

// zeroValue constructs the default local value for a numeric value type.
func zeroValue(vt wasmir.ValueType) (Value, error) {
	switch vt {
	case wasmir.ValueTypeI32:
		return Value{Type: wasmir.ValueTypeI32}, nil
	case wasmir.ValueTypeI64:
		return Value{Type: wasmir.ValueTypeI64}, nil
	case wasmir.ValueTypeF32:
		return Value{Type: wasmir.ValueTypeF32}, nil
	case wasmir.ValueTypeF64:
		return Value{Type: wasmir.ValueTypeF64}, nil
	default:
		if vt.IsRef() {
			return Value{Type: vt, Ref: Reference{Kind: RefKindNull}}, nil
		}
		return Value{}, fmt.Errorf("unsupported local type %s", vt)
	}
}

// instructionError wraps err with the current program counter and opcode.
func (e *executor) instructionError(err error) error {
	return instructionErrorAt(e.pc, e.fn.code[e.pc].kind, err)
}

// push appends v to the operand stack.
func (e *executor) push(v Value) {
	e.stack = append(e.stack, v)
}

// pop removes and returns the top operand stack value.
func (e *executor) pop() (Value, error) {
	if len(e.stack) == 0 {
		return Value{}, fmt.Errorf("operand stack underflow")
	}
	v := e.stack[len(e.stack)-1]
	e.stack = e.stack[:len(e.stack)-1]
	return v, nil
}

// callFunction pops arguments for the target function and invokes it through
// the resolver.
func (e *executor) callFunction(index uint32) ([]Value, error) {
	if e.resolver == nil {
		return nil, fmt.Errorf("resolver is nil")
	}
	calleeType, err := e.resolver.FuncType(index)
	if err != nil {
		return nil, err
	}
	callArgs, err := e.popArgs(calleeType.Params)
	if err != nil {
		return nil, err
	}
	return e.resolver.CallFunc(index, callArgs)
}

// evalI64Binary pops two i64 operands and evaluates an i64 binary instruction.
func (e *executor) evalI64Binary(kind wasmir.InstrKind) (int64, error) {
	rhs, err := e.popI64()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popI64()
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
	case wasmir.InstrI64DivS:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		if lhs == minInt64 && rhs == -1 {
			return 0, fmt.Errorf("integer overflow")
		}
		return lhs / rhs, nil
	case wasmir.InstrI64DivU:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		return int64(uint64(lhs) / uint64(rhs)), nil
	case wasmir.InstrI64RemS:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		return lhs % rhs, nil
	case wasmir.InstrI64RemU:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		return int64(uint64(lhs) % uint64(rhs)), nil
	case wasmir.InstrI64And:
		return lhs & rhs, nil
	case wasmir.InstrI64Or:
		return lhs | rhs, nil
	case wasmir.InstrI64Xor:
		return lhs ^ rhs, nil
	case wasmir.InstrI64Shl:
		return int64(uint64(lhs) << (uint64(rhs) & 63)), nil
	case wasmir.InstrI64ShrS:
		return lhs >> (uint64(rhs) & 63), nil
	case wasmir.InstrI64ShrU:
		return int64(uint64(lhs) >> (uint64(rhs) & 63)), nil
	case wasmir.InstrI64Rotl:
		return int64(bits.RotateLeft64(uint64(lhs), int(uint64(rhs)&63))), nil
	case wasmir.InstrI64Rotr:
		return int64(bits.RotateLeft64(uint64(lhs), -int(uint64(rhs)&63))), nil
	default:
		return 0, fmt.Errorf("unsupported i64 binary instruction %s", instrName(kind))
	}
}

// evalI64Unary pops one i64 operand and evaluates an i64 unary instruction.
func (e *executor) evalI64Unary(kind wasmir.InstrKind) (int64, error) {
	v, err := e.popI64()
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrI64Clz:
		return int64(bits.LeadingZeros64(uint64(v))), nil
	case wasmir.InstrI64Ctz:
		return int64(bits.TrailingZeros64(uint64(v))), nil
	case wasmir.InstrI64Popcnt:
		return int64(bits.OnesCount64(uint64(v))), nil
	case wasmir.InstrI64Extend8S:
		return int64(int8(v)), nil
	case wasmir.InstrI64Extend16S:
		return int64(int16(v)), nil
	case wasmir.InstrI64Extend32S:
		return int64(int32(v)), nil
	default:
		return 0, fmt.Errorf("unsupported i64 unary instruction %s", instrName(kind))
	}
}

// evalI64Compare pops two i64 operands and evaluates an i64 comparison,
// returning the WebAssembly i32 boolean result.
func (e *executor) evalI64Compare(kind wasmir.InstrKind) (int32, error) {
	rhs, err := e.popI64()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popI64()
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
	case wasmir.InstrI64LtU:
		return boolI32(uint64(lhs) < uint64(rhs)), nil
	case wasmir.InstrI64LeS:
		return boolI32(lhs <= rhs), nil
	case wasmir.InstrI64LeU:
		return boolI32(uint64(lhs) <= uint64(rhs)), nil
	case wasmir.InstrI64GtS:
		return boolI32(lhs > rhs), nil
	case wasmir.InstrI64GtU:
		return boolI32(uint64(lhs) > uint64(rhs)), nil
	case wasmir.InstrI64GeS:
		return boolI32(lhs >= rhs), nil
	case wasmir.InstrI64GeU:
		return boolI32(uint64(lhs) >= uint64(rhs)), nil
	default:
		return 0, fmt.Errorf("unsupported i64 comparison instruction %s", instrName(kind))
	}
}

// evalF32Binary pops two f32 operands and evaluates an f32 arithmetic
// instruction.
func (e *executor) evalF32Binary(kind wasmir.InstrKind) (float32, error) {
	rhs, err := e.popF32()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popF32()
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
	case wasmir.InstrF32Min:
		return float32(math.Min(float64(lhs), float64(rhs))), nil
	case wasmir.InstrF32Max:
		return float32(math.Max(float64(lhs), float64(rhs))), nil
	case wasmir.InstrF32Copysign:
		return math.Float32frombits((math.Float32bits(lhs) &^ (1 << 31)) | (math.Float32bits(rhs) & (1 << 31))), nil
	default:
		return 0, fmt.Errorf("unsupported f32 binary instruction %s", instrName(kind))
	}
}

// evalF32Unary pops one f32 operand and evaluates an f32 unary instruction.
func (e *executor) evalF32Unary(kind wasmir.InstrKind) (float32, error) {
	v, err := e.popF32()
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrF32Abs:
		return math.Float32frombits(math.Float32bits(v) &^ (1 << 31)), nil
	case wasmir.InstrF32Neg:
		return math.Float32frombits(math.Float32bits(v) ^ (1 << 31)), nil
	case wasmir.InstrF32Sqrt:
		return float32(math.Sqrt(float64(v))), nil
	case wasmir.InstrF32Ceil:
		return float32(math.Ceil(float64(v))), nil
	case wasmir.InstrF32Floor:
		return float32(math.Floor(float64(v))), nil
	case wasmir.InstrF32Trunc:
		return float32(math.Trunc(float64(v))), nil
	case wasmir.InstrF32Nearest:
		return float32(math.RoundToEven(float64(v))), nil
	default:
		return 0, fmt.Errorf("unsupported f32 unary instruction %s", instrName(kind))
	}
}

// evalF32Compare pops two f32 operands and evaluates an f32 comparison,
// returning the WebAssembly i32 boolean result.
func (e *executor) evalF32Compare(kind wasmir.InstrKind) (int32, error) {
	rhs, err := e.popF32()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popF32()
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

// evalF64Unary pops one f64 operand and evaluates an f64 unary instruction.
func (e *executor) evalF64Unary(kind wasmir.InstrKind) (float64, error) {
	v, err := e.popF64()
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrF64Abs:
		return math.Float64frombits(math.Float64bits(v) &^ (1 << 63)), nil
	case wasmir.InstrF64Neg:
		return math.Float64frombits(math.Float64bits(v) ^ (1 << 63)), nil
	case wasmir.InstrF64Sqrt:
		return math.Sqrt(v), nil
	case wasmir.InstrF64Ceil:
		return math.Ceil(v), nil
	case wasmir.InstrF64Floor:
		return math.Floor(v), nil
	case wasmir.InstrF64Trunc:
		return math.Trunc(v), nil
	case wasmir.InstrF64Nearest:
		return math.RoundToEven(v), nil
	default:
		return 0, fmt.Errorf("unsupported f64 unary instruction %s", instrName(kind))
	}
}

// evalF64Binary pops two f64 operands and evaluates an f64 arithmetic
// instruction.
func (e *executor) evalF64Binary(kind wasmir.InstrKind) (float64, error) {
	rhs, err := e.popF64()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popF64()
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
	case wasmir.InstrF64Min:
		return math.Min(lhs, rhs), nil
	case wasmir.InstrF64Max:
		return math.Max(lhs, rhs), nil
	case wasmir.InstrF64Copysign:
		return math.Float64frombits((math.Float64bits(lhs) &^ (1 << 63)) | (math.Float64bits(rhs) & (1 << 63))), nil
	default:
		return 0, fmt.Errorf("unsupported f64 binary instruction %s", instrName(kind))
	}
}

// evalF64Compare pops two f64 operands and evaluates an f64 comparison,
// returning the WebAssembly i32 boolean result.
func (e *executor) evalF64Compare(kind wasmir.InstrKind) (int32, error) {
	rhs, err := e.popF64()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popF64()
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

// evalI32Binary pops two i32 operands and evaluates an i32 binary instruction.
func (e *executor) evalI32Binary(kind wasmir.InstrKind) (int32, error) {
	rhs, err := e.popI32()
	if err != nil {
		return 0, err
	}
	lhs, err := e.popI32()
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
	case wasmir.InstrI32DivS:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		if lhs == minInt32 && rhs == -1 {
			return 0, fmt.Errorf("integer overflow")
		}
		return lhs / rhs, nil
	case wasmir.InstrI32DivU:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		return int32(uint32(lhs) / uint32(rhs)), nil
	case wasmir.InstrI32RemS:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		return lhs % rhs, nil
	case wasmir.InstrI32RemU:
		if rhs == 0 {
			return 0, fmt.Errorf("integer divide by zero")
		}
		return int32(uint32(lhs) % uint32(rhs)), nil
	case wasmir.InstrI32And:
		return lhs & rhs, nil
	case wasmir.InstrI32Or:
		return lhs | rhs, nil
	case wasmir.InstrI32Xor:
		return lhs ^ rhs, nil
	case wasmir.InstrI32Shl:
		return int32(uint32(lhs) << (uint32(rhs) & 31)), nil
	case wasmir.InstrI32ShrS:
		return lhs >> (uint32(rhs) & 31), nil
	case wasmir.InstrI32ShrU:
		return int32(uint32(lhs) >> (uint32(rhs) & 31)), nil
	case wasmir.InstrI32Rotl:
		return int32(bits.RotateLeft32(uint32(lhs), int(uint32(rhs)&31))), nil
	case wasmir.InstrI32Rotr:
		return int32(bits.RotateLeft32(uint32(lhs), -int(uint32(rhs)&31))), nil
	case wasmir.InstrI32Eq:
		return boolI32(lhs == rhs), nil
	case wasmir.InstrI32Ne:
		return boolI32(lhs != rhs), nil
	case wasmir.InstrI32LtS:
		return boolI32(lhs < rhs), nil
	case wasmir.InstrI32LtU:
		return boolI32(uint32(lhs) < uint32(rhs)), nil
	case wasmir.InstrI32LeS:
		return boolI32(lhs <= rhs), nil
	case wasmir.InstrI32LeU:
		return boolI32(uint32(lhs) <= uint32(rhs)), nil
	case wasmir.InstrI32GtS:
		return boolI32(lhs > rhs), nil
	case wasmir.InstrI32GtU:
		return boolI32(uint32(lhs) > uint32(rhs)), nil
	case wasmir.InstrI32GeS:
		return boolI32(lhs >= rhs), nil
	case wasmir.InstrI32GeU:
		return boolI32(uint32(lhs) >= uint32(rhs)), nil
	default:
		return 0, fmt.Errorf("unsupported i32 binary instruction %s", instrName(kind))
	}
}

// evalI32Unary pops one i32 operand and evaluates an i32 unary instruction.
func (e *executor) evalI32Unary(kind wasmir.InstrKind) (int32, error) {
	v, err := e.popI32()
	if err != nil {
		return 0, err
	}

	switch kind {
	case wasmir.InstrI32Clz:
		return int32(bits.LeadingZeros32(uint32(v))), nil
	case wasmir.InstrI32Ctz:
		return int32(bits.TrailingZeros32(uint32(v))), nil
	case wasmir.InstrI32Popcnt:
		return int32(bits.OnesCount32(uint32(v))), nil
	case wasmir.InstrI32Extend8S:
		return int32(int8(v)), nil
	case wasmir.InstrI32Extend16S:
		return int32(int16(v)), nil
	default:
		return 0, fmt.Errorf("unsupported i32 unary instruction %s", instrName(kind))
	}
}

// evalConversion pops the source operand for a numeric conversion or
// reinterpret instruction and returns the converted runtime value.
func (e *executor) evalConversion(kind wasmir.InstrKind) (Value, error) {
	switch kind {
	case wasmir.InstrI32WrapI64:
		v, err := e.popI64()
		return Value{Type: wasmir.ValueTypeI32, I32: int32(v)}, err
	case wasmir.InstrI32TruncF32S:
		v, err := e.popF32()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI32S(float64(v))
		return Value{Type: wasmir.ValueTypeI32, I32: out}, err
	case wasmir.InstrI32TruncF32U:
		v, err := e.popF32()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI32U(float64(v))
		return Value{Type: wasmir.ValueTypeI32, I32: out}, err
	case wasmir.InstrI32TruncF64S:
		v, err := e.popF64()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI32S(v)
		return Value{Type: wasmir.ValueTypeI32, I32: out}, err
	case wasmir.InstrI32TruncF64U:
		v, err := e.popF64()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI32U(v)
		return Value{Type: wasmir.ValueTypeI32, I32: out}, err
	case wasmir.InstrI32TruncSatF32S:
		v, err := e.popF32()
		return Value{Type: wasmir.ValueTypeI32, I32: truncSatFloatToI32S(float64(v))}, err
	case wasmir.InstrI32TruncSatF32U:
		v, err := e.popF32()
		return Value{Type: wasmir.ValueTypeI32, I32: truncSatFloatToI32U(float64(v))}, err
	case wasmir.InstrI32TruncSatF64S:
		v, err := e.popF64()
		return Value{Type: wasmir.ValueTypeI32, I32: truncSatFloatToI32S(v)}, err
	case wasmir.InstrI32TruncSatF64U:
		v, err := e.popF64()
		return Value{Type: wasmir.ValueTypeI32, I32: truncSatFloatToI32U(v)}, err
	case wasmir.InstrI64ExtendI32S:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeI64, I64: int64(v)}, err
	case wasmir.InstrI64ExtendI32U:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeI64, I64: int64(uint32(v))}, err
	case wasmir.InstrI64TruncF32S:
		v, err := e.popF32()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI64S(float64(v))
		return Value{Type: wasmir.ValueTypeI64, I64: out}, err
	case wasmir.InstrI64TruncF32U:
		v, err := e.popF32()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI64U(float64(v))
		return Value{Type: wasmir.ValueTypeI64, I64: out}, err
	case wasmir.InstrI64TruncF64S:
		v, err := e.popF64()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI64S(v)
		return Value{Type: wasmir.ValueTypeI64, I64: out}, err
	case wasmir.InstrI64TruncF64U:
		v, err := e.popF64()
		if err != nil {
			return Value{}, err
		}
		out, err := truncFloatToI64U(v)
		return Value{Type: wasmir.ValueTypeI64, I64: out}, err
	case wasmir.InstrI64TruncSatF32S:
		v, err := e.popF32()
		return Value{Type: wasmir.ValueTypeI64, I64: truncSatFloatToI64S(float64(v))}, err
	case wasmir.InstrI64TruncSatF32U:
		v, err := e.popF32()
		return Value{Type: wasmir.ValueTypeI64, I64: truncSatFloatToI64U(float64(v))}, err
	case wasmir.InstrI64TruncSatF64S:
		v, err := e.popF64()
		return Value{Type: wasmir.ValueTypeI64, I64: truncSatFloatToI64S(v)}, err
	case wasmir.InstrI64TruncSatF64U:
		v, err := e.popF64()
		return Value{Type: wasmir.ValueTypeI64, I64: truncSatFloatToI64U(v)}, err
	case wasmir.InstrF32ConvertI32S:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeF32, F32: float32(v)}, err
	case wasmir.InstrF32ConvertI32U:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeF32, F32: float32(uint32(v))}, err
	case wasmir.InstrF32ConvertI64S:
		v, err := e.popI64()
		return Value{Type: wasmir.ValueTypeF32, F32: float32(v)}, err
	case wasmir.InstrF32ConvertI64U:
		v, err := e.popI64()
		return Value{Type: wasmir.ValueTypeF32, F32: float32(uint64(v))}, err
	case wasmir.InstrF32DemoteF64:
		v, err := e.popF64()
		return Value{Type: wasmir.ValueTypeF32, F32: float32(v)}, err
	case wasmir.InstrF64ConvertI32S:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeF64, F64: float64(v)}, err
	case wasmir.InstrF64ConvertI32U:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeF64, F64: float64(uint32(v))}, err
	case wasmir.InstrF64ConvertI64S:
		v, err := e.popI64()
		return Value{Type: wasmir.ValueTypeF64, F64: float64(v)}, err
	case wasmir.InstrF64ConvertI64U:
		v, err := e.popI64()
		return Value{Type: wasmir.ValueTypeF64, F64: float64(uint64(v))}, err
	case wasmir.InstrF64PromoteF32:
		v, err := e.popF32()
		return Value{Type: wasmir.ValueTypeF64, F64: float64(v)}, err
	case wasmir.InstrI32ReinterpretF32:
		v, err := e.popF32()
		return Value{Type: wasmir.ValueTypeI32, I32: int32(math.Float32bits(v))}, err
	case wasmir.InstrI64ReinterpretF64:
		v, err := e.popF64()
		return Value{Type: wasmir.ValueTypeI64, I64: int64(math.Float64bits(v))}, err
	case wasmir.InstrF32ReinterpretI32:
		v, err := e.popI32()
		return Value{Type: wasmir.ValueTypeF32, F32: math.Float32frombits(uint32(v))}, err
	case wasmir.InstrF64ReinterpretI64:
		v, err := e.popI64()
		return Value{Type: wasmir.ValueTypeF64, F64: math.Float64frombits(uint64(v))}, err
	default:
		return Value{}, fmt.Errorf("unsupported conversion instruction %s", instrName(kind))
	}
}

// checkedTruncFloat truncates x toward zero and verifies the truncated value is
// in [lower, upper).
func checkedTruncFloat(x, lower, upper float64) (float64, error) {
	if math.IsNaN(x) {
		return 0, fmt.Errorf("invalid conversion to integer")
	}
	if math.IsInf(x, 0) {
		return 0, fmt.Errorf("integer overflow")
	}
	t := math.Trunc(x)
	if t < lower || t >= upper {
		return 0, fmt.Errorf("integer overflow")
	}
	return t, nil
}

// truncFloatToI32S implements trapping signed float-to-i32 truncation.
func truncFloatToI32S(x float64) (int32, error) {
	t, err := checkedTruncFloat(x, minInt32Float, two31Float)
	return int32(t), err
}

// truncFloatToI32U implements trapping unsigned float-to-i32 truncation.
func truncFloatToI32U(x float64) (int32, error) {
	t, err := checkedTruncFloat(x, 0, two32Float)
	return int32(uint32(t)), err
}

// truncFloatToI64S implements trapping signed float-to-i64 truncation.
func truncFloatToI64S(x float64) (int64, error) {
	t, err := checkedTruncFloat(x, minInt64Float, two63Float)
	return int64(t), err
}

// truncFloatToI64U implements trapping unsigned float-to-i64 truncation.
func truncFloatToI64U(x float64) (int64, error) {
	t, err := checkedTruncFloat(x, 0, two64Float)
	return int64(uint64(t)), err
}

// truncSatFloatToI32S implements saturating signed float-to-i32 truncation.
func truncSatFloatToI32S(x float64) int32 {
	if math.IsNaN(x) {
		return 0
	}
	t := math.Trunc(x)
	if t < minInt32Float {
		return minInt32
	}
	if t >= two31Float {
		return maxInt32
	}
	return int32(t)
}

// truncSatFloatToI32U implements saturating unsigned float-to-i32 truncation.
func truncSatFloatToI32U(x float64) int32 {
	if math.IsNaN(x) {
		return 0
	}
	t := math.Trunc(x)
	if t <= 0 {
		return 0
	}
	if t >= two32Float {
		v := ^uint32(0)
		return int32(v)
	}
	return int32(uint32(t))
}

// truncSatFloatToI64S implements saturating signed float-to-i64 truncation.
func truncSatFloatToI64S(x float64) int64 {
	if math.IsNaN(x) {
		return 0
	}
	t := math.Trunc(x)
	if t < minInt64Float {
		return minInt64
	}
	if t >= two63Float {
		return maxInt64
	}
	return int64(t)
}

// truncSatFloatToI64U implements saturating unsigned float-to-i64 truncation.
func truncSatFloatToI64U(x float64) int64 {
	if math.IsNaN(x) {
		return 0
	}
	t := math.Trunc(x)
	if t <= 0 {
		return 0
	}
	if t >= two64Float {
		v := ^uint64(0)
		return int64(v)
	}
	return int64(uint64(t))
}

// boolI32 converts a WebAssembly i32 condition result to 0 or 1.
func boolI32(v bool) int32 {
	if v {
		return 1
	}
	return 0
}

// memoryAddress computes an i32-memory effective address from the dynamic base
// operand and the static memory offset immediate.
func memoryAddress(base int32, offset uint64) (uint64, error) {
	addr := uint64(uint32(base))
	if addr > ^uint64(0)-offset {
		return 0, fmt.Errorf("memory address overflow")
	}
	return addr + offset, nil
}

// memoryAccessSize returns the byte width used by a supported memory
// load/store instruction.
func memoryAccessSize(kind wasmir.InstrKind) uint32 {
	switch kind {
	case wasmir.InstrI32Load8S, wasmir.InstrI32Load8U, wasmir.InstrI32Store8,
		wasmir.InstrI64Load8S, wasmir.InstrI64Load8U, wasmir.InstrI64Store8:
		return 1
	case wasmir.InstrI32Load16S, wasmir.InstrI32Load16U, wasmir.InstrI32Store16,
		wasmir.InstrI64Load16S, wasmir.InstrI64Load16U, wasmir.InstrI64Store16:
		return 2
	case wasmir.InstrI32Load, wasmir.InstrI32Store,
		wasmir.InstrI64Load32S, wasmir.InstrI64Load32U, wasmir.InstrI64Store32,
		wasmir.InstrF32Load, wasmir.InstrF32Store:
		return 4
	default:
		return 8
	}
}

// extendI32Load applies the sign-extension or zero-extension behavior required
// by kind to the raw little-endian memory value.
func extendI32Load(kind wasmir.InstrKind, raw uint64) int32 {
	switch kind {
	case wasmir.InstrI32Load8S:
		return int32(int8(raw))
	case wasmir.InstrI32Load8U:
		return int32(uint8(raw))
	case wasmir.InstrI32Load16S:
		return int32(int16(raw))
	case wasmir.InstrI32Load16U:
		return int32(uint16(raw))
	default:
		return int32(uint32(raw))
	}
}

// extendI64Load applies the sign-extension or zero-extension behavior required
// by kind to the raw little-endian memory value.
func extendI64Load(kind wasmir.InstrKind, raw uint64) int64 {
	switch kind {
	case wasmir.InstrI64Load8S:
		return int64(int8(raw))
	case wasmir.InstrI64Load8U:
		return int64(uint8(raw))
	case wasmir.InstrI64Load16S:
		return int64(int16(raw))
	case wasmir.InstrI64Load16U:
		return int64(uint16(raw))
	case wasmir.InstrI64Load32S:
		return int64(int32(raw))
	case wasmir.InstrI64Load32U:
		return int64(uint32(raw))
	default:
		return int64(raw)
	}
}

// popWant pops the top operand and verifies it has the expected value type.
func (e *executor) popWant(want wasmir.ValueType) (Value, error) {
	v, err := e.pop()
	if err != nil {
		return Value{}, err
	}
	if v.Type != want {
		return Value{}, fmt.Errorf("got %s operand, want %s", v.Type, want)
	}
	return v, nil
}

// popI32 pops the top operand and returns its i32 payload.
func (e *executor) popI32() (int32, error) {
	v, err := e.popWant(wasmir.ValueTypeI32)
	return v.I32, err
}

// popI64 pops the top operand and returns its i64 payload.
func (e *executor) popI64() (int64, error) {
	v, err := e.popWant(wasmir.ValueTypeI64)
	return v.I64, err
}

// popF32 pops the top operand and returns its f32 payload.
func (e *executor) popF32() (float32, error) {
	v, err := e.popWant(wasmir.ValueTypeF32)
	return v.F32, err
}

// popF64 pops the top operand and returns its f64 payload.
func (e *executor) popF64() (float64, error) {
	v, err := e.popWant(wasmir.ValueTypeF64)
	return v.F64, err
}

// popArgs removes a call's arguments from the operand stack and returns them in
// parameter order.
func (e *executor) popArgs(params []wasmir.ValueType) ([]Value, error) {
	// Wasm evaluates arguments left-to-right and leaves them on the operand
	// stack in parameter order, so the call argument list is the top
	// len(params) values without reversing.
	if len(e.stack) < len(params) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(e.stack) - len(params)
	args := e.stack[base:]
	e.stack = e.stack[:base]
	if err := CheckArgs(params, args); err != nil {
		return nil, err
	}
	return args, nil
}

// popResults removes function result values from the operand stack and returns
// them in result order.
func (e *executor) popResults(results []wasmir.ValueType) ([]Value, error) {
	if len(e.stack) < len(results) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(e.stack) - len(results)
	out := e.stack[base:]
	e.stack = e.stack[:base]
	if err := CheckResults(results, out); err != nil {
		return nil, err
	}
	return out, nil
}
