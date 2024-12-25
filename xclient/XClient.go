package xclient

import (
	"context"
	"io"
	. "miniRPC"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery          // 服务发现的接口，负责获取服务实例列表
	mode    SelectMode         // 选择模式，例如随机选择、轮询选择等
	opt     *Option            // 配置选项，包括序列化方式等
	mu      sync.Mutex         // 互斥锁，用于保护以下字段的并发访问
	clients map[string]*Client // 存储已建立连接的RPC客户端
}

var _ io.Closer = (*XClient)(nil)

// 初始化并返回一个新的 XClient 实例
func NewXClient(d Discovery, mode SelectMode, opt *Option) *XClient {
	return &XClient{
		d:       d,
		mode:    mode,
		opt:     opt,
		clients: make(map[string]*Client),
	}
}

// 关闭所有存储在 clients 字典中的 Client 实例，并从字典中删除这些实例
func (xc *XClient) Close() error {
	xc.mu.Lock()         // 互斥锁
	defer xc.mu.Unlock() // 方法执行完后释放
	for key, client := range xc.clients {
		// 忽略关闭时的错误
		_ = client.Close()
		delete(xc.clients, key)
	}
	return nil
}

func (xc *XClient) dial(rpcAddr string) (*Client, error) {
	xc.mu.Lock() // 加锁以保护 xc.clients 的并发访问
	defer xc.mu.Unlock()

	client, ok := xc.clients[rpcAddr] // 检查是否已经有该地址的 Client
	if ok && !client.IsAvailable() {  // 如果有，但不可用
		_ = client.Close()          // 关闭不可用的 Client
		delete(xc.clients, rpcAddr) // 从缓存中移除
		client = nil                // 清空 client 变量
	}
	if client == nil { // 如果缓存中没有可用的 Client
		var err error
		client, err = XDial(rpcAddr, xc.opt) // 创建新连接
		if err != nil {                      // 如果连接失败，返回错误
			return nil, err
		}
		xc.clients[rpcAddr] = client // 将新创建的 Client 存入缓存
	}
	return client, nil
}

// 调用指定地址的 RPC 服务
func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	client, err := xc.dial(rpcAddr) // 获取可用的 Client（或新建）
	if err != nil {                 // 如果无法获取 Client，返回错误
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply) // 调用 Client 的 Call 方法执行远程调用
}

// 对外提供的核心调用接口，会自动选择一个可用的服务器地址
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode) // 从服务发现机制中获取一个服务器地址
	if err != nil {                   // 如果无法获取地址，返回错误
		return err
	}
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply) // 调用指定地址的服务
}

// Broadcast 将请求广播到所有的服务实例
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	// 获取所有注册的服务地址
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}
	// 初始化同步工具
	var wg sync.WaitGroup
	var mu sync.Mutex // 保护 e 和 replyDone
	var e error
	replyDone := reply == nil // 如果 reply 是 nil，不需要设置结果
	// 如果任意一个调用失败，会通过取消上下文立即终止其他尚未完成的调用
	ctx, cancel := context.WithCancel(ctx) // fast fail
	// 对每个服务器地址启动 goroutine
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			defer wg.Done()
			// 如果 reply 不为空，则使用反射动态创建一个与 reply 类型相同的变量 clonedReply
			// 以确保每个服务器的返回值不会互相覆盖
			var clonedReply interface{}
			if reply != nil {
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			// 执行
			err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			mu.Lock()
			if err != nil && e == nil {
				e = err
				cancel() // 如果有错误，取消未完成的调用
			}
			// 如果调用成功，并且还没有设置返回值，则将当前调用的返回值设置到 reply 中
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	wg.Wait() // 等待所有 goroutine 执行完成
	return e
}
