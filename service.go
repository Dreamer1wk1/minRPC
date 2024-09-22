package miniRPC

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type methodType struct {
	method    reflect.Method // 方法
	ArgType   reflect.Type   // 参数类型
	ReplyType reflect.Type   // 返回值类型
	numCalls  uint64         //调用次数
}

func (m *methodType) NumCalls() uint64 {
	// 通过原子操作获取，避免并发导致数据不一致
	return atomic.LoadUint64(&m.numCalls)
}

func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	if m.ArgType.Kind() == reflect.Ptr {
		// 创建指针指向的实际类型
		argv = reflect.New(m.ArgType.Elem())
	} else {
		// 创建值类型，并直接获取其值
		argv = reflect.New(m.ArgType).Elem()
	}
	return argv
}

func (m *methodType) newReplyv() reflect.Value {
	// 返回值必须为指针类型
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		// 通过 reflect.MakeMap 创建空的 Map
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		// 通过 reflect.MakeSlice 创建长度为 0 的空 Slice
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}
	return replyv
}

type service struct {
	name   string
	typ    reflect.Type
	rcvr   reflect.Value
	method map[string]*methodType
}

func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	// 获取服务名，如果名字不是导出的（即非大写开头）会报错，因为非导出类型不能被外部访问
	// 在 Go 语言中，"非大写开头" 指的是未导出的标识符
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	// 注册服务
	s.registerMethods()
	return s
}

func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	// 遍历 rcvr 中的方法，并注册符合要求的方法
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		// 方法必须有 3 个参数（第一个是接收者，第二个是请求参数，第三个是响应参数）
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// 方法必须有且仅有 1 个返回值，并且这个返回值的类型必须是 error 类型
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		// 参数类型检查：请求参数和响应参数的类型必须是导出类型（即大写开头）或内建类型（编程语言本身提供），否则不允许注册
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

// 检查类型 t 是否是导出类型或内建类型
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
