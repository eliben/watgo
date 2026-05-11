package vm

import (
	"fmt"
	"math"

	"github.com/eliben/watgo/wasmir"
)

// Value is one runtime WebAssembly value passed into or returned from a VM
// function.
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

// CallResolver resolves and invokes function-index calls made by ExecuteFunction.
type CallResolver interface {
	FuncType(index uint32) (wasmir.TypeDef, error)
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
// This is deliberately minimal for now: it initializes locals, maintains a
// single operand stack, and executes only the small instruction subset needed
// by the first wasmvm tests.
func ExecuteFunction(fn *Function, ft wasmir.TypeDef, args []Value, calls CallResolver) ([]Value, error) {
	if fn == nil {
		return nil, fmt.Errorf("defined function has no compiled code")
	}

	locals := append([]Value{}, args...)
	for _, vt := range fn.Locals {
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

	for pc := 0; pc < len(fn.Code); pc++ {
		ins := fn.Code[pc]
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
			calleeType, err := calls.FuncType(ins.Index)
			if err != nil {
				return nil, err
			}
			callArgs, err := popArgs(&stack, calleeType.Params)
			if err != nil {
				return nil, err
			}
			results, err := calls.CallFunc(ins.Index, callArgs)
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
			if pc != len(fn.Code)-1 {
				// Non-final end closes structured control. Branch targets skip over
				// it, and ordinary fallthrough can treat it as a no-op because
				// validation has already established the operand stack contract.
				continue
			}
			return popResults(&stack, ft.Results)
		default:
			return nil, fmt.Errorf("unsupported instruction %s", InstrName(ins.Kind))
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
		return 0, fmt.Errorf("unsupported i64 binary instruction %s", InstrName(kind))
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
		return 0, fmt.Errorf("unsupported i64 comparison instruction %s", InstrName(kind))
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
		return 0, fmt.Errorf("unsupported f32 binary instruction %s", InstrName(kind))
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
		return 0, fmt.Errorf("unsupported f32 comparison instruction %s", InstrName(kind))
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
		return 0, fmt.Errorf("unsupported f64 binary instruction %s", InstrName(kind))
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
		return 0, fmt.Errorf("unsupported f64 comparison instruction %s", InstrName(kind))
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
		return 0, fmt.Errorf("unsupported i32 binary instruction %s", InstrName(kind))
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
