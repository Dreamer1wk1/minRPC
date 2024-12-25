package miniRPC

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"miniRPC/codec"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Seq           uint64      // RPC 调用的序列号
	ServiceMethod string      // 格式为 "<service>.<method>"
	Args          interface{} // 方法入参
	Reply         interface{} // 方法返回值
	Error         error       // 存储异常
	// Done 是一个通道，用于在多个协程之间传递消息，Call 完成时，会向该通道发送信号，通常是自己
	Done chan *Call // 通知 RPC 调用已完成
}

// 当一个 RPC 调用完成时，done 方法被调用，它会将 call 自身发送到 Done 通道上。这一行为向所有等待该调用完成的代码发出信号，表示调用已经处理完毕
func (call *Call) done() {
	// 将当前 Call 结构体的实例 call 发送到 Done 通道中
	call.Done <- call
}

// 一个客户端可能会关联多个未完成的调用，一个客户端可以被多个协程使用
type Client struct {
	cc       codec.Codec  // 编解码器
	opt      *Option      // 选择的协议
	sending  sync.Mutex   // 互斥锁，保证请求有序发送
	header   codec.Header // 请求头
	mu       sync.Mutex
	seq      uint64           // 发送请求编号
	pending  map[uint64]*Call // 存储未发送完的请求
	closing  bool             // 主动关闭
	shutdown bool             // 错误导致关闭
}

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shut down")

// 关闭连接
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}
	client.closing = true
	return client.cc.Close()
}

// 返回客户端工作状态
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 注册一个新的 RPC 调用
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock() // 加互斥锁
	defer client.mu.Unlock()
	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq // 为调用分配一个唯一的序列号 Seq
	client.pending[call.Seq] = call
	client.seq++ // 自增序列号
	return call.Seq, nil
}

func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	// 根据传入的序列号移除 call 并返回
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 中止所有 RPC 调用
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

func (client *Client) receive() {
	var err error    // 保存可能出现的错误信息
	for err == nil { // 循环接收响应
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break // 读取消息头失败，退出循环
		}
		call := client.removeCall(h.Seq) // 根据接收的序列号移除 map 中的请求
		switch {
		case call == nil:
			// 这种情况通常表示之前的 Write 操作部分失败，导致调用已经被移除。此时尝试读取响应体，但不存储结果，调用 client.cc.ReadBody(nil)
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			//如果消息头中的 Error 字段不为空，表示 RPC 调用出错。设置 call.Error 为相应的错误信息。同样读取响应体，但不存储结果。调用 call.done()，标记这个调用完成。
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			// 如果没有错误，尝试读取响应体并存储到 call.Reply 中。如果读取响应体时发生错误，将错误信息记录到 call.Error。调用 call.done()，标记这个调用完成
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}
	// 出现错误时，调用 client.terminateCalls(err) 终止所有未完成的调用。这会将所有未完成的调用的状态标记为出错，并通知相关的协程
	client.terminateCalls(err)
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	// 根据 opt.CodecType 查找与此编码类型对应的编解码函数
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}
	// 将选择的编解码器通过 JSON 发送给服务端
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	// 使用 f 创建编解码器，并初始化客户端对象
	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1, // seq starts with 1, 0 means invalid call
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call), // make 用于创建 map 和切片等数据结构
	}
	go client.receive() // 启动协程接收服务器的响应
	return client
}

// ...*Option：可变长参数的指针数组
func parseOptions(opts ...*Option) (*Option, error) {
	// 如果没有传入 opts
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

func (client *Client) send(call *Call) {
	// 加锁确保请求完整发送，不会被打断
	client.sending.Lock()
	defer client.sending.Unlock()

	// 注册请求
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// 构建请求头
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	// 编码并发送请求，err != nil 时执行 if 中的内容
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq) // 写入出错，移除请求
		if call != nil {
			// 记录错误信息，结束调用
			call.Error = err
			call.done()
		}
	}
}

// Go 函数用于发起一个异步调用，并返回一个 Call 结构体，表示该调用的状态。调用 Go 函数时，客户端不会等待响应，调用会立即返回，客户端可以通过监听返回的 Call 中的 Done 通道来获取调用的结果
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		// 创建一个容量为 10（平衡性能和资源使用） 的缓冲通道
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// 同步调用超时处理机制
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	// 发起异步远程服务方法调用
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	// 阻塞等待其中一个事件发生
	select {
	// 如果上下文 ctx 被取消或超时，则 ctx.Done() 通道会接收到一个值（通常是 Canceled 或 DeadlineExceeded），这时执行该分支
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	// 如果远程服务方法调用完成，并且 Done 通道中有值可接收，则执行该分支（复用异步调用，但是立即检查 Done）
	case call := <-call.Done:
		return call.Error
	}
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	// net 包函数设置连接超时时间
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	// close the connection if client is nil
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult) // 定义 chan clientResult 类型通道
	go func() {
		client, err := f(conn, opt)
		// 封装在 clientResult 结构体中，并通过通道 ch 发送
		ch <- clientResult{client: client, err: err}
	}()
	// 如果为0，意味着没有设置连接超时，直接通过 <-ch 从 ch 接收一个 clientResult 值，并将其存储在 result 变量中
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	// 否则使用 select 语句来同时等待两个事件
	select {
	// 定时器，在指定的时间（opt.ConnectTimeout）后触发。如果这个事件先发生，那么 select 语句会执行相应的 case，并返回一个超时错误
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
		// 从 ch 接收数据的事件，如果 f 没有超时，select 语句会执行相并返回接收到的 client和 err
	case result := <-ch:
		return result.client, result.err
	}
}

// 简化的dialTimeout调用
func Dial(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewClient, network, address, opts...)
}

// NewHTTPClient new a Client instance via HTTP as transport protocol
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
	// Require successful HTTP response before switching to RPC protocol.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	// 连接成功，创建客户端实例返回
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

// 连接到指定地址的HTTP RPC服务器，并设置默认的RPC路径
// 调用上面的 NewHTTPClient 创建连接
func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

// XDial calls different functions to connect to an RPC server
// according the first parameter rpcAddr.
// rpcAddr is a general format (protocol@addr) to represent a rpc server
// eg, http@10.0.0.1:7001, tcp@10.0.0.1:9999, unix@/tmp/miniRPC.sock
func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		// tcp, unix or other transport protocol
		return Dial(protocol, addr, opts...)
	}
}
