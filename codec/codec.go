package codec

import "io"

type Header struct {
	ServiceMethod string // 格式 "Service.Method"
	Seq           uint64 // 序列号
	Error         string // 错误信息
}

// 定义通用的编解码器行为
type Codec interface {
	io.Closer                         // 嵌入 io.Closer 接口
	ReadHeader(*Header) error         // 读取消息头
	ReadBody(interface{}) error       // 读取消息体
	Write(*Header, interface{}) error // 写入消息头和消息体
}

// NewCodecFunc 是一个函数类型，它接收一个 io.ReadWriteCloser 作为参数，并返回一个 Codec 接口的实现
type NewCodecFunc func(io.ReadWriteCloser) Codec

// 自定义字符串类型的新类型 Type，表示编解码器的类型
type Type string

const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json" // 未实现
)

// NewCodecFuncMap 是一个全局变量，定义为 map[Type]NewCodecFunc，用于将编解码器类型（Type）映射到创建该类型编解码器的工厂方法（NewCodecFunc）
var NewCodecFuncMap map[Type]NewCodecFunc

// 包初始化时自动执行，用于初始化全局变量 NewCodecFuncMap
func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
