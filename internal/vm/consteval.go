package vm

import (
	"fmt"
	"math"

	"github.com/eliben/watgo/wasmir"
)

// EvalConstExpr evaluates a lowered WebAssembly constant expression.
//
// The input is the flat wasmir instruction sequence used by module-level
// initializers. resolver is used for global.get instructions; callers own the
// policy for which globals are visible in a specific const-expression context.
// For example, wasmvm uses this to allow only earlier immutable globals while
// instantiating module-defined globals.
func EvalConstExpr(init []wasmir.Instruction, resolver Resolver) (Value, error) {
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
		case wasmir.InstrRefNull:
			stack = append(stack, Value{Type: ins.RefType, Ref: Reference{Kind: RefKindNull}})
		case wasmir.InstrRefFunc:
			if resolver == nil {
				return Value{}, fmt.Errorf("initializer instruction %d: resolver is nil", pc)
			}
			if _, err := resolver.FuncType(ins.FuncIndex); err != nil {
				return Value{}, fmt.Errorf("initializer instruction %d: %w", pc, err)
			}
			stack = append(stack, Value{Type: wasmir.RefTypeFunc(false), Ref: Reference{Kind: RefKindFunc, FuncIndex: ins.FuncIndex}})
		case wasmir.InstrGlobalGet:
			if resolver == nil {
				return Value{}, fmt.Errorf("initializer instruction %d: resolver is nil", pc)
			}
			v, err := resolver.GlobalGet(ins.GlobalIndex)
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

// popConstValue pops the top const-expression stack value and verifies its
// value type.
func popConstValue(stack *[]Value, want wasmir.ValueType) (Value, error) {
	if len(*stack) == 0 {
		return Value{}, fmt.Errorf("initializer stack underflow")
	}
	v := (*stack)[len(*stack)-1]
	*stack = (*stack)[:len(*stack)-1]
	if v.Type != want {
		return Value{}, fmt.Errorf("initializer got %s, want %s", v.Type, want)
	}
	return v, nil
}
