package vm

import (
	"fmt"
	"math"

	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value.
//
// wasmvm re-exports this type as wasmvm.Value. internal/vm keeps the concrete
// representation because the interpreter needs to push, pop, and type-check
// values directly.
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
//
// ExecuteFunction intentionally does not know about wasmvm.ModuleInstance,
// host imports, or export tables. When it executes a call instruction, it uses
// this interface to ask the owner of the function index space for the callee's
// signature and then for the actual call. In wasmvm, a small adapter implements
// this by re-entering ModuleInstance.callFunc.
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

// ExecuteFunction interprets one compiled module-defined function body.
//
// args are the function parameters in order, and ft is the function's validated
// signature. calls is used only for wasm call instructions. For a function A
// calling function B calling a host import, execution looks like this:
//
//   - wasmvm.callFunc(A) calls ExecuteFunction(A).
//   - ExecuteFunction(A) reaches call B, pops B's arguments, and calls
//     calls.CallFunc(B).
//   - wasmvm.callFunc(B) calls ExecuteFunction(B).
//   - ExecuteFunction(B) reaches call host, pops host arguments, and calls
//     calls.CallFunc(host).
//   - wasmvm.callFunc(host) invokes the HostFunc Go callback and returns its
//     results back through the two ExecuteFunction frames.
func ExecuteFunction(fn *Function, ft wasmir.TypeDef, args []Value, calls CallResolver) ([]Value, error) {
	if fn == nil {
		return nil, fmt.Errorf("defined function has no compiled code")
	}

	locals := append([]Value{}, args...)
	for _, vt := range fn.locals {
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

	for pc := 0; pc < len(fn.code); pc++ {
		ins := fn.code[pc]
		switch ins.kind {
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
				pc = ins.target
				continue
			}
		case wasmir.InstrElse:
			// Reaching else normally means the then arm completed without
			// branching. Skip the else arm.
			pc = ins.target
		case wasmir.InstrLocalGet:
			if int(ins.index) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.index)
			}
			stack = append(stack, locals[ins.index])
		case wasmir.InstrLocalSet:
			if int(ins.index) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.index)
			}
			v, err := pop()
			if err != nil {
				return nil, err
			}
			if v.Type != locals[ins.index].Type {
				return nil, fmt.Errorf("local.set %d got %s, want %s", ins.index, v.Type, locals[ins.index].Type)
			}
			locals[ins.index] = v
		case wasmir.InstrLocalTee:
			if int(ins.index) >= len(locals) {
				return nil, fmt.Errorf("local index %d out of range", ins.index)
			}
			v, err := pop()
			if err != nil {
				return nil, err
			}
			if v.Type != locals[ins.index].Type {
				return nil, fmt.Errorf("local.tee %d got %s, want %s", ins.index, v.Type, locals[ins.index].Type)
			}
			locals[ins.index] = v
			stack = append(stack, v)
		case wasmir.InstrI32Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: int32(ins.bits)})
		case wasmir.InstrI32Add, wasmir.InstrI32Sub, wasmir.InstrI32Mul,
			wasmir.InstrI32Eq, wasmir.InstrI32Ne,
			wasmir.InstrI32LtS, wasmir.InstrI32LeS, wasmir.InstrI32GtS, wasmir.InstrI32GeS:
			v, err := evalI32Binary(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrI32Eqz:
			v, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: boolI32(v == 0)})
		case wasmir.InstrI64Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeI64, I64: ins.bits})
		case wasmir.InstrI64Add, wasmir.InstrI64Sub, wasmir.InstrI64Mul:
			v, err := evalI64Binary(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI64, I64: v})
		case wasmir.InstrI64Eq, wasmir.InstrI64Ne,
			wasmir.InstrI64LtS, wasmir.InstrI64LeS, wasmir.InstrI64GtS, wasmir.InstrI64GeS:
			v, err := evalI64Compare(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrI64Eqz:
			v, err := popI64(pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: boolI32(v == 0)})
		case wasmir.InstrF32Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeF32, F32: math.Float32frombits(uint32(ins.bits))})
		case wasmir.InstrF32Add, wasmir.InstrF32Sub, wasmir.InstrF32Mul, wasmir.InstrF32Div:
			v, err := evalF32Binary(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeF32, F32: v})
		case wasmir.InstrF32Eq, wasmir.InstrF32Ne,
			wasmir.InstrF32Lt, wasmir.InstrF32Le, wasmir.InstrF32Gt, wasmir.InstrF32Ge:
			v, err := evalF32Compare(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrF64Const:
			stack = append(stack, Value{Type: wasmir.ValueTypeF64, F64: math.Float64frombits(uint64(ins.bits))})
		case wasmir.InstrF64Add, wasmir.InstrF64Sub, wasmir.InstrF64Mul, wasmir.InstrF64Div:
			v, err := evalF64Binary(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeF64, F64: v})
		case wasmir.InstrF64Eq, wasmir.InstrF64Ne,
			wasmir.InstrF64Lt, wasmir.InstrF64Le, wasmir.InstrF64Gt, wasmir.InstrF64Ge:
			v, err := evalF64Compare(ins.kind, pop)
			if err != nil {
				return nil, err
			}
			stack = append(stack, Value{Type: wasmir.ValueTypeI32, I32: v})
		case wasmir.InstrDrop:
			if _, err := pop(); err != nil {
				return nil, err
			}
		case wasmir.InstrCall:
			// A call instruction is the one place where execution leaves this
			// package. Function indices include imports before module-defined
			// functions, so only wasmvm.ModuleInstance can decide whether this
			// is a host callback or another compiled wasm function.
			calleeType, err := calls.FuncType(ins.index)
			if err != nil {
				return nil, err
			}
			callArgs, err := popArgs(&stack, calleeType.Params)
			if err != nil {
				return nil, err
			}
			results, err := calls.CallFunc(ins.index, callArgs)
			if err != nil {
				return nil, err
			}
			stack = append(stack, results...)
		case wasmir.InstrBr:
			pc = ins.target
		case wasmir.InstrBrIf:
			// br_if consumes only the condition. Any branch result values are
			// already below it on the operand stack and are left there for the
			// target block's end to consume.
			cond, err := popI32(pop)
			if err != nil {
				return nil, err
			}
			if cond != 0 {
				pc = ins.target
			}
		case wasmir.InstrReturn:
			return popResults(&stack, ft.Results)
		case wasmir.InstrEnd:
			if pc != len(fn.code)-1 {
				// Non-final end closes structured control. Branch targets skip over
				// it, and ordinary fallthrough can treat it as a no-op because
				// validation has already established the operand stack contract.
				continue
			}
			return popResults(&stack, ft.Results)
		default:
			return nil, fmt.Errorf("unsupported instruction %s", instrName(ins.kind))
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

func popArgs(stack *[]Value, params []wasmir.ValueType) ([]Value, error) {
	// Wasm evaluates arguments left-to-right and leaves them on the operand
	// stack in parameter order, so the call argument list is the top
	// len(params) values without reversing.
	if len(*stack) < len(params) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(*stack) - len(params)
	args := (*stack)[base:]
	*stack = (*stack)[:base]
	if err := CheckArgs(params, args); err != nil {
		return nil, err
	}
	return args, nil
}

func popResults(stack *[]Value, results []wasmir.ValueType) ([]Value, error) {
	if len(*stack) < len(results) {
		return nil, fmt.Errorf("operand stack underflow")
	}
	base := len(*stack) - len(results)
	out := (*stack)[base:]
	*stack = (*stack)[:base]
	if err := CheckResults(results, out); err != nil {
		return nil, err
	}
	return out, nil
}
