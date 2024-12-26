package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type miniRegistryDiscovery struct {
	*MultiServersDiscovery               // 负责本地存储和管理服务地址
	registry               string        // 注册中心的地址
	timeout                time.Duration // 多久更新一次服务列表
	lastUpdate             time.Time     // 上次更新服务列表的时间
}

const defaultUpdateTimeout = time.Second * 10

func NewminiRegistryDiscovery(registerAddr string, timeout time.Duration) *miniRegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout // 设置默认超时时间
	}
	d := &miniRegistryDiscovery{
		MultiServersDiscovery: NewMultiServerDiscovery(make([]string, 0)), // 初始化服务发现
		registry:              registerAddr,                               // 注册中心地址
		timeout:               timeout,                                    // 超时时间
	}
	return d
}

// 更新服务列表，更新完将上次更新时间设为当前时间
func (d *miniRegistryDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	d.lastUpdate = time.Now()
	return nil
}

// 自动刷新服务列表（超时判断）
func (d *miniRegistryDiscovery) Refresh() error {
	d.mu.Lock() // 加锁保护数据
	defer d.mu.Unlock()

	// 如果未超时，不需要刷新服务列表
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}

	// 超时后，从注册中心获取最新服务列表
	log.Println("rpc registry: refresh servers from registry", d.registry)
	resp, err := http.Get(d.registry) // 发送 HTTP GET 请求
	if err != nil {
		log.Println("rpc registry refresh err:", err)
		return err
	}

	// 解析返回的服务地址列表
	servers := strings.Split(resp.Header.Get("X-miniRPC-Servers"), ",")
	d.servers = make([]string, 0, len(servers)) // 初始化服务列表
	for _, server := range servers {
		if strings.TrimSpace(server) != "" { // 去掉空白字符
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}

	// 更新最后一次更新时间
	d.lastUpdate = time.Now()
	return nil
}

func (d *miniRegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := d.Refresh(); err != nil { // 判断服务是否过期
		return "", err
	}
	return d.MultiServersDiscovery.Get(mode)
}

func (d *miniRegistryDiscovery) GetAll() ([]string, error) {
	if err := d.Refresh(); err != nil {
		return nil, err
	}
	return d.MultiServersDiscovery.GetAll()
}
