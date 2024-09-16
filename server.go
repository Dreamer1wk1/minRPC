package miniRPC

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"miniRPC/codec"
	"net"
	"reflect"
	"sync"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber int        // 用来标识某些特定的请求或数据流，这里用于标记 geerpc 请求
	CodecType   codec.Type // 编码类型
}

// 配置默认的请求设置
var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
}

// 空结构体表示 RPC 服务器，没有任何字段，通过方法提供功能
type Server struct{}

// 创建 Server
func NewServer() *Server {
	return &Server{}
}

// 默认 Server 实例，可以直接使用
var DefaultServer = NewServer()

// Accept 是 Server 类型的方法，用来接受传入的网络连接
// lis 是 net.Listener 类型，表示服务器在监听的网络端口
func (server *Server) Accept(lis net.Listener) {
	// 无限循环
	for {
		// 多重返回值，返回 conn 和 err
		conn, err := lis.Accept()
		if err != nil {
			// 返回的 err 不为空，接受连接时发生错误
			log.Println("rpc server: accept error:", err)
			return
		}
		// 通过子协程并发处理多个连接
		go server.ServeConn(conn)
	}
}

// 独立的函数，不属于任何结构体
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// 用于在一个连接 conn 上启动 RPC 服务，阻塞直到客户端断开连接
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	// 通过 conn 读取客户端发来的数据，并解码到 opt 变量中
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	// 校验请求类型是否为 RPC
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	// 检验编码类型是否支持并获取对应的构造函数f
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	// 使用指定的编解码器处理请求
	server.serveCodec(f(conn))
}

// 空结构体，用于在请求处理出错时作为响应的占位符
var invalidRequest = struct{}{}

// 具体处理请求的逻辑，不断从连接中读取请求并进行处理，直到所有请求处理完毕
func (server *Server) serveCodec(cc codec.Codec) {
	sending := new(sync.Mutex) // 互斥锁确保每次发送响应时不会出现并发冲突
	wg := new(sync.WaitGroup)  // 等待组用于等待所有的请求处理完成
	// 循环处理多个请求
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break // 请求为空，关闭连接
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1) // 处理请求后计数器自增
		// 使用子协程处理请求（一个连接中可能有多个）
		go server.handleRequest(cc, req, sending, wg)
	}
	wg.Wait() //等待所有请求处理完成
	_ = cc.Close()
}

type request struct {
	h            *codec.Header // 请求头
	argv, replyv reflect.Value // 请求参数和响应值
}

// *codec.Header, error 为返回值类型
func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	// 创建 request 结构体实例，并将 h 字段初始化为 h 变量的值，返回实例的指针
	req := &request{h: h}
	// TODO: 现在我们暂时不知道 body 的类型，先当字符串来处理
	req.argv = reflect.New(reflect.TypeOf("")) // 通过反射获取类型
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock() // 方法返回前释放锁
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	// TODO, 调用请求的方法并返回结果
	// day 1, 先打印请求参数演示
	// wg.Done() 表示请求处理完成时通知 WaitGroup
	defer wg.Done()
	log.Println(req.h, req.argv.Elem())
	req.replyv = reflect.ValueOf(fmt.Sprintf("minirpc response %d", req.h.Seq))
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}
