package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type miniRegistry struct {
	timeout time.Duration // 注册中心设置的超时时间
	mu      sync.Mutex    // protect following
	servers map[string]*ServerItem
}
type ServerItem struct {
	Addr  string    // 服务器的地址
	start time.Time // 服务器最近一次心跳时间
}

const (
	defaultPath    = "/_miniRPC_/registry"
	defaultTimeout = time.Minute * 5
)

// 注册中心的构造函数
func New(timeout time.Duration) *miniRegistry {
	return &miniRegistry{
		servers: make(map[string]*ServerItem), // 初始化为空字典
		timeout: timeout,
	}
}

var DefaultminiRegister = New(defaultTimeout) // 使用默认超时时间创建的默认实例

func (r *miniRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{Addr: addr, start: time.Now()}
	} else {
		s.start = time.Now() // 如果服务存在更新心跳时间
	}
}

func (r *miniRegistry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string
	for addr, s := range r.servers {
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}
	sort.Strings(alive)
	return alive
}

// 处理请求
func (r *miniRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		// keep it simple, server is in req.Header
		w.Header().Set("X-miniRPC-Servers", strings.Join(r.aliveServers(), ","))
	case "POST":
		// keep it simple, server is in req.Header
		addr := req.Header.Get("X-miniRPC-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// 将 miniRegistry 实例绑定到指定的 HTTP 路径上，作为 HTTP 请求的处理器
func (r *miniRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultminiRegister.HandleHTTP(defaultPath) // 绑定默认路径
}

// 4 分钟发送一次心跳到注册中心。保活
func Heartbeat(registry, addr string, duration time.Duration) {
	if duration == 0 {
		// // 确保心跳间隔足够短，以便在服务超时被移除前发送新的心跳
		duration = defaultTimeout - time.Duration(1)*time.Minute // 4 min 一次
	}
	var err error
	err = sendHeartbeat(registry, addr) // 立即发送一次心跳
	go func() {
		t := time.NewTicker(duration) // 创建定时器，周期为 duration
		for err == nil {
			<-t.C                               // 每次定时器触发
			err = sendHeartbeat(registry, addr) // 再次发送心跳
		}
	}()
}

// 发送心跳
func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil) // 创建 HTTP POST 请求
	req.Header.Set("X-Geerpc-Server", addr)          // 设置服务地址到请求头
	if _, err := httpClient.Do(req); err != nil {    // 执行
		log.Println("rpc server: heart beat err:", err)
		return err
	}
	return nil
}
