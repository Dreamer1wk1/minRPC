package main

import (
	"encoding/json"
	"fmt"
	"log"
	"miniRPC"
	"miniRPC/codec"
	"net"
	"time"
)

func startServer(addr chan string) {
	// :0 是一个特殊的端口号，表示由操作系统选择一个空闲端口
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatal("network error:", err)
	}
	log.Println("start rpc server on", l.Addr())
	// 获取监听的地址（包括端口），并将其发送到 addr 通道中，以便客户端能够连接到服务器
	addr <- l.Addr().String()
	miniRPC.Accept(l)
}

func main() {
	// 建一个通道，用于在 startServer 函数中传递服务器的地址
	addr := make(chan string)
	go startServer(addr)

	// 从 addr 通道中读取服务器的地址，并建立 TCP 连接
	conn, _ := net.Dial("tcp", <-addr)
	defer func() { _ = conn.Close() }()

	time.Sleep(time.Second)
	// send options
	// 将 miniRPC.DefaultOption 编码为 JSON 格式并发送到 conn 连接中
	_ = json.NewEncoder(conn).Encode(miniRPC.DefaultOption)
	// 创建一个新的 GobCodec 实例，并将 conn 作为参数传递给它
	cc := codec.NewGobCodec(conn)
	// send request & receive response
	for i := 0; i < 5; i++ {
		h := &codec.Header{
			ServiceMethod: "Foo.Sum",
			Seq:           uint64(i),
		}
		// _ 忽略返回值
		_ = cc.Write(h, fmt.Sprintf("minirpc req %d", h.Seq))
		// 从服务器返回的响应中读取和解码消息的头部和主体
		_ = cc.ReadHeader(h)
		var reply string
		_ = cc.ReadBody(&reply)
		log.Println("reply:", reply)
	}
}
