package expr

import (
	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"reflect"
)

type Data struct {
	Model string
}

type BoolVM struct {
	program *vm.Program
}

func NewBoolVM(code string) (*BoolVM, error) {
	v, err := expr.Compile(code, expr.Env(Data{}), expr.AsBool())
	if err != nil {
		return nil, err
	}

	return &BoolVM{program: v}, nil
}

func (v *BoolVM) Run(data Data) (bool, error) {
	ret, err := expr.Run(v.program, data)
	if err != nil {
		return false, err
	}

	return ret.(bool), nil
}

type StringVM struct {
	program *vm.Program
}

func NewStringVM(code string) (*StringVM, error) {
	v, err := expr.Compile(code, expr.Env(Data{}), expr.AsKind(reflect.String))
	if err != nil {
		return nil, err
	}

	return &StringVM{program: v}, nil
}

func (v *StringVM) Run(data Data) (string, error) {
	ret, err := expr.Run(v.program, data)
	if err != nil {
		return "", err
	}

	return ret.(string), nil
}
