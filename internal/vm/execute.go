package vm

import (
	"fmt"
	"math"
	"math/bits"

	"github.com/eliben/watgo/wasmir"
)

const (
	minInt32 = -1 << 31
	minInt64 = -1 << 63
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
}

// CallResolver resolves and invokes function-index calls made by
// ExecuteFunction.
type CallResolver interface {
	// FuncType returns the signature of the function at index.
	FuncType(index uint32) (wasmir.TypeDef, error)

	// CallFunc invokes the function at index with already popped arguments in
	// parameter order.
	CallFunc(index uint32, args []Value) ([]Value, error)
}

// CheckArgs verifies call argument count and value types.
func CheckArgs(params []wasmir.ValueType, args []Value) error {
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

// CheckResults verifies result count and value types.
func CheckResults(want []wasmir.ValueType, got []Value) error {
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

// executor is one active module-defined function frame.
type executor struct {
	// fn is the compiled function being interpreted by this frame.
	fn *Function

	// ft is fn's validated WebAssembly signature.
	ft wasmir.TypeDef

	// calls resolves and dispatches wasm call instructions back to the owner of
	// the module function index space.
	calls CallResolver

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
func ExecuteFunction(fn *Function, ft wasmir.TypeDef, args []Value, calls CallResolver) ([]Value, error) {
	if fn == nil {
		return nil, fmt.Errorf("defined function has no compiled code")
	}

	e := executor{
		fn:    fn,
		ft:    ft,
		calls: calls,
		stack: make([]Value, 0),
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
		case wasmir.InstrBlock:
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
			if v.Type != e.locals[ins.index].Type {
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
			if v.Type != e.locals[ins.index].Type {
				return nil, e.instructionError(fmt.Errorf("local.tee %d got %s, want %s", ins.index, v.Type, e.locals[ins.index].Type))
			}
			e.locals[ins.index] = v
			e.push(v)
		case wasmir.InstrI32Const:
			e.push(Value{Type: wasmir.ValueTypeI32, I32: int32(ins.bits)})
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
		case wasmir.InstrF32Const:
			e.push(Value{Type: wasmir.ValueTypeF32, F32: math.Float32frombits(uint32(ins.bits))})
		case wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div:
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
		case wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div:
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
		case wasmir.InstrCall:
			calleeType, err := e.calls.FuncType(ins.index)
			if err != nil {
				return nil, e.instructionError(err)
			}
			callArgs, err := e.popArgs(calleeType.Params)
			if err != nil {
				return nil, e.instructionError(err)
			}
			results, err := e.calls.CallFunc(ins.index, callArgs)
			if err != nil {
				return nil, e.instructionError(err)
			}
			e.stack = append(e.stack, results...)
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
	default:
		return 0, fmt.Errorf("unsupported f32 binary instruction %s", instrName(kind))
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

// boolI32 converts a WebAssembly i32 condition result to 0 or 1.
func boolI32(v bool) int32 {
	if v {
		return 1
	}
	return 0
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
