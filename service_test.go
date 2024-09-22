package miniRPC

import (
	"fmt"
	"reflect"
	"testing"
)

type Foo int

type Args struct{ Num1, Num2 int }

// 导出方法
func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

// 非导出方法
func (f Foo) sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

// _assert 是一个内部使用的辅助函数，用于断言某个条件。如果 condition 为 false，则抛出 panic，并输出格式化后的 msg
func _assert(condition bool, msg string, v ...interface{}) {
	if !condition {
		panic(fmt.Sprintf("assertion failed: "+msg, v...))
	}
}

func TestNewService(t *testing.T) {
	var foo Foo
	s := newService(&foo)
	// 使用 _assert 验证 s.method 中的注册方法数量是否为 1，确保 Foo 类型的 Sum 方法被正确注册
	_assert(len(s.method) == 1, "wrong service Method, expect 1, but got %d", len(s.method))
	mType := s.method["Sum"]
	_assert(mType != nil, "wrong Method, Sum shouldn't be nil")
}

func TestMethodType_Call(t *testing.T) {
	// 创建 Foo 实例并获得 Foo.Sum 方法的类型
	var foo Foo
	s := newService(&foo)
	mType := s.method["Sum"]
	argv := mType.newArgv()
	replyv := mType.newReplyv()
	argv.Set(reflect.ValueOf(Args{Num1: 1, Num2: 3}))
	// 调用方法
	err := s.call(mType, argv, replyv)
	_assert(err == nil && *replyv.Interface().(*int) == 4 && mType.NumCalls() == 1, "failed to call Foo.Sum")
}
