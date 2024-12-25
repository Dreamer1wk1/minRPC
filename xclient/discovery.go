package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

const (
	RandomSelect     SelectMode = iota // 随机
	RoundRobinSelect                   // 轮询
)

type Discovery interface {
	Refresh() error                      // 从远程注册中心刷新服务列表
	Update(servers []string) error       // 手动更新服务列表
	Get(mode SelectMode) (string, error) // 根据模式获取一个服务
	GetAll() ([]string, error)           // 获取所有服务
}

type MultiServersDiscovery struct {
	r       *rand.Rand   // generate random number
	mu      sync.RWMutex // 读写锁，保护对 servers 和 index 的并发访问
	servers []string     // 记录服务的地址
	index   int          // 记录当前轮询算法的位置
}

// NewMultiServerDiscovery creates a MultiServersDiscovery instance
func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r:       rand.New(rand.NewSource(time.Now().UnixNano())), // 随机数生成器
	}
	d.index = d.r.Intn(math.MaxInt32 - 1) //  生成一个小于 math.MaxInt32 - 1 的随机数
	return d
}

var _ Discovery = (*MultiServersDiscovery)(nil) // _ 是变量名

// Refresh doesn't make sense for MultiServersDiscovery, so ignore it
func (d *MultiServersDiscovery) Refresh() error {
	return nil
}

// 更新当前的服务器列表，可以在运行时动态修改服务地址
func (d *MultiServersDiscovery) Update(servers []string) error {
	d.mu.Lock() // 写锁
	defer d.mu.Unlock()
	d.servers = servers // 更新列表
	return nil
}

// 根据指定的选择模式从服务列表中选择一个服务器
func (d *MultiServersDiscovery) Get(mode SelectMode) (string, error) {
	d.mu.Lock() // 写锁
	defer d.mu.Unlock()
	n := len(d.servers) // 服务为空，返回错误信息
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	switch mode {
	case RandomSelect: // 随机
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect: // 轮询
		s := d.servers[d.index%n] // 通过取模确保不会越界
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

// 获取所有可用的服务器地址列表
func (d *MultiServersDiscovery) GetAll() ([]string, error) {
	d.mu.RLock() // 读锁
	defer d.mu.RUnlock()
	// 创建一个新切片 servers，并将 d.servers 的内容拷贝到新切片中
	// 使用 copy() 函数，确保返回的是副本，而不是直接暴露 d.servers
	// 防止外部调用者修改原始的服务列表，破坏数据完整性
	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
