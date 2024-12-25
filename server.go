package miniRPC

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"miniRPC/codec"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int           // 用来标识某些特定的请求或数据流，这里用于标记 geerpc 请求
	CodecType      codec.Type    // 编码类型
	ConnectTimeout time.Duration // 0 means no limit
	HandleTimeout  time.Duration
}

// 配置默认的请求设置
var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10, // lian'jie
}

// 空结构体表示 RPC 服务器，没有任何字段，通过方法提供功能
type Server struct {
	serviceMap sync.Map // 并发安全的 Map
}

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
	server.serveCodec(f(conn), &opt)
}

// 空结构体，用于在请求处理出错时作为响应的占位符
var invalidRequest = struct{}{}

// 具体处理请求的逻辑，不断从连接中读取请求并进行处理，直到所有请求处理完毕
func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
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
		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait() //等待所有请求处理完成
	_ = cc.Close()
}

func (server *Server) Register(rcvr interface{}) error {
	// 通过 newService 将传入的服务实例包装成服务对象，提取出该实例的所有可导出方法
	s := newService(rcvr)
	// 将提取出的服务注册到 Server 中
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined: " + s.name)
	}
	return nil
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	// 格式为 Service.Method，所以从最后一个点分割出方法名
	dot := strings.LastIndex(serviceMethod, ".")
	// 没有点说明格式不正确
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	// 分别查找服务名和方法名
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}
	// 从对应的服务中获取方法，然后根据 methodName 查找对应的 methodType
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

// DefaultServer 是一个全局的 Server 实例，方便用户直接用这个函数注册服务，而不需要手动创建 Server 实例
func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }

// request 结构体保存一次调用中的所有信息
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request
	mtype        *methodType
	svc          *service
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
	// // 创建 request 结构体实例，并将 h 字段初始化为 h 变量的值，返回实例的指针
	req := &request{h: h}
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	// 检查 argv 类型是否为指针，如果不是，就使用 Addr() 方法获取其地址的反射值
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read body err:", err)
		return req, err
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

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	// 创建两个无缓冲通道 called 和 sent，分别用于通知主 goroutine 方法已被调用和响应已被发送
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{} // 发送信号
		if err != nil {
			req.h.Error = err.Error() // 设置错误信息
			server.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{} // 发送信号
			return
		}
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{} // 发送信号
	}()
	// 如果设置了超时时间，主协程会使用 select 语句监听超时事件或 called 信号
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	// 超时，发送错误响应
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
		// 若在超时前服务方法被调用（即接收到 called 信号），则继续等待 sent 信号，确保响应已发送
	case <-called:
		<-sent
	}
}

const (
	connected        = "200 Connected to miniRPC" // 连接成功的响应
	defaultRPCPath   = "/_miniRPC_"               // 定义 RPC 请求的默认 HTTP 路径
	defaultDebugPath = "/debug/miniRPC"           // 定义调试信息的默认 HTTP 路径
)

// ServeHTTP implements a http.Handler that answers RPC requests
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// 检查请求的方法
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		// 只接受 CONNECT 方法，否则返回 405
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}
	// 用于从 HTTP 连接中获取底层 TCP 连接，使之脱离 HTTP 的控制，进行原生的 TCP 通信
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking ", req.RemoteAddr, ": ", err.Error())
		return
	}
	// 没有错误，连接成功，向客户端写入连接成功的响应信息
	_, _ = io.WriteString(conn, "HTTP/1.0 "+connected+"\n\n")
	server.ServeConn(conn)
}

// HandleHTTP registers an HTTP handler for RPC messages on rpcPath.
// 此方法不会启动 HTTP 服务，启动服务需要通过 http.Serve() 函数
// So it is still necessary to invoke http.Serve(), typically in a go statement.
func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

// 全局辅助方法，用于默认的 DefaultServer 实例注册 HTTP 处理程序
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
