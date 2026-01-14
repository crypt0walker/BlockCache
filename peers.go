package blockcache

//基于etcd的分布式服务发现与一致性哈希路由组件
import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/crypt0walker/BlockCache/consistenthash"
	"github.com/crypt0walker/BlockCache/registry"
	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
)

const defaultSvcName = "block-cache"

/*Group（缓存主程序）手里拿着一个 PeerPicker 接口（实际上是 ClientPicker）。
当需要查 key="Tom" 时，Group 问 PeerPicker：“Tom 归谁管？”
ClientPicker 查哈希环，回答：“归 192.168.1.5 管，这是他的联络员（一个实现了
Peer接口的 Client 对象）。”
Group 拿到这个 Client 对象，调用 Client.Get(...) 发起网络请求。
PeerPicker: 选人逻辑（大脑）。
Peer: 具体交互协议（手脚）。
ClientPicker: 完整的集群管理器（结合了大脑和手脚）。*/

// 定义了peer选择器的接口：如何取找到节点
type PeerPicker interface {
	PickPeer(key string) (peer Peer, ok bool, self bool)
	//关闭选择器并释放资源
	Close() error
}

// 定义了缓存节点的结构：一个节点能做什么动作
// Peer 定义了缓存节点的接口
type Peer interface {
	Get(ctx context.Context, group string, key string) ([]byte, error)
	Set(ctx context.Context, group string, key string, value []byte) error
	Delete(group string, key string) (bool, error)
	Close() error
}

// clientpicker：实现了peerpicker接口：核心管理者
type ClientPicker struct {
	//我是谁，我的地址
	selfAddr string
	//我的团队是什么，ETCD中的服务前缀s
	svcName string
	//读写锁
	mu sync.RWMutex
	//一致性哈希，相当于一个路由，由key找到节点地址
	consHash *consistenthash.Map
	//维护着节点到客户端连接对象的映射：map[selfAddr] = Client
	clients map[string]*Client
	//etcd集群发现,ETCD的连接器，保持和ETCD服务的长连接；用来注册自己和监听他人
	etcdCli *clientv3.Client
	//生命周期管理：为了能够优雅地杀死一直在后台运行的etcd监听协程
	ctx    context.Context    //ctx是一个令牌，交给etcd监听协程进行监听
	cancel context.CancelFunc //cancel用于杀死etcd监听协程

}

// Option的函数类型，作为opts的函数签名
type PickerOption func(*ClientPicker)

// 初始化逻辑
// 创建新ClientPicker实例
func NewClientPicker(addr string, opts ...PickerOption) (*ClientPicker, error) {
	//此处创建了带取消的context
	ctx, cancel := context.WithCancel(context.Background())
	picker := &ClientPicker{
		selfAddr: addr,
		svcName:  defaultSvcName,
		clients:  make(map[string]*Client),
		consHash: consistenthash.New(),
		//初始化赋值ctx与cancel
		ctx:    ctx,
		cancel: cancel,
	}
	//函数选项模式：利用opts参数传入的函数选项来设置ClientPicker的属性
	for _, opt := range opts {
		opt(picker)
	}

	//初始化并建立到ETCD集群的连接
	//1. 调用官方库的构造函数 New
	// 与etcd的交互都有这个客户端cli完成
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   registry.DefaultConfig.Endpoints,
		DialTimeout: registry.DefaultConfig.DialTimeout,
	})
	if err != nil {
		return nil, err
	}
	picker.etcdCli = cli

	//启动服务发现，先调用全量更新，此后启动增量更新
	if err := picker.startServiceDiscovery(); err != nil {
		cancel()
		cli.Close()
		return nil, err
	}
	return picker, nil
}

// startServiceDiscovery 启动服务发现
func (p *ClientPicker) startServiceDiscovery() error {
	// 先进行全量更新
	if err := p.fetchAllServices(); err != nil {
		return err
	}

	// 异步开启协程启动增量更新，由于是异步，所以不返回error，因为逻辑上无效
	go p.watchServiceChanges()
	return nil
}

// fetchAllServices 获取所有服务实例
func (p *ClientPicker) fetchAllServices() error {
	// 此ctx是p的子ctx，p.ctx取消时，此ctx也会取消
	ctx, cancel := context.WithTimeout(p.ctx, 3*time.Second)
	defer cancel()

	// ETCD的Get操作
	// 参数 1: "/services/" + p.svcName
	// 假设 svcName 是 "block-cache"，那么这个 Key 前缀就是 "/services/block-cache"
	// 参数 2: clientv3.WithPrefix() -> 以第二个参数为前缀的所有
	resp, err := p.etcdCli.Get(ctx, "/services/"+p.svcName, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("failed to get all services: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	//使用读写锁并发安全地遍历etcd返回的结果中的地址，逐个创建client、地址加入哈希环、加入clients的map
	for _, kv := range resp.Kvs {
		addr := string(kv.Value)
		if addr != "" && addr != p.selfAddr {
			p.set(addr)
			logrus.Infof("Discovered service at %s", addr)
		}
	}
	return nil
}

// set方法：创建client、地址加入哈希环、加入clients的map
func (p *ClientPicker) set(addr string) {
	if client, err := NewClient(addr, p.svcName, p.etcdCli); err == nil {
		p.clients[addr] = client
		p.consHash.Add(addr)
		logrus.Infof("Discovered service at %s", addr)
	} else {
		logrus.Errorf("Failed to create client for %s: %v", addr, err)
	}
}

// watchServiceChanges 监听服务实例变化
func (p *ClientPicker) watchServiceChanges() {
	watcher := clientv3.NewWatcher(p.etcdCli)
	watchChan := watcher.Watch(p.ctx, "/services/"+p.svcName, clientv3.WithPrefix())
	for {
		select {
		case <-p.ctx.Done():
			watcher.Close()
			return
		case resp := <-watchChan:
			p.handleWatchEvents(resp.Events)
		}
	}
}

// handleWatchEvents 处理监听到的事件
func (p *ClientPicker) handleWatchEvents(events []*clientv3.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	//etcd不会改一个就推一次，而是在网络繁忙时将一堆变化打包成一个events列表发送
	for _, event := range events {
		addr := string(event.Kv.Value)
		//过滤自己，以免造成环路（etcd的广播也会发给自己）
		if addr == p.selfAddr {
			continue
		}

		switch event.Type {
		//EventTypePut：上线/更新
		case clientv3.EventTypePut:
			if _, exists := p.clients[addr]; !exists {
				p.set(addr)
				logrus.Infof("New service discovered at %s", addr)
			}
			//EventTypeDelete：下线/故障
		case clientv3.EventTypeDelete:
			if client, exists := p.clients[addr]; exists {
				//显示关闭底层tcp连接，否则会造成连接泄漏
				client.Close()
				//删除clients map里的kv && consHash里的地址
				p.remove(addr)
				logrus.Infof("Service removed at %s", addr)
			}
		}
	}
}

// 删除节点
func (p *ClientPicker) remove(addr string) {
	//consHash的Remove内部有锁
	p.consHash.Remove(addr)
	delete(p.clients, addr)
}

// Close 关闭所有资源
func (p *ClientPicker) Close() error {
	p.cancel()
	p.mu.Lock()
	defer p.mu.Unlock()

	var errs []error
	for addr, client := range p.clients {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close client %s: %v", addr, err))
		}
	}

	if err := p.etcdCli.Close(); err != nil {
		errs = append(errs, fmt.Errorf("failed to close etcd client: %v", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("errors while closing: %v", errs)
	}
	return nil
}

// pickpeer 通过使用key与一致性哈希查找client,从而选择peer节点
func (p *ClientPicker) PickPeer(key string) (Peer, bool, bool) {
	//需要读consHash的map，所以需要加读锁
	p.mu.RLock()
	defer p.mu.RUnlock()
	//一致性哈希查找
	if addr := p.consHash.Get(key); addr != "" {
		if client, ok := p.clients[addr]; ok {
			return client, true, addr == p.selfAddr
		}
	}
	return nil, false, false
}

func (p *ClientPicker) Delete(key string) {
	p.remove(key)
}

// PrintPeers 打印当前已发现的节点
func (p *ClientPicker) PrintPeers() {
	p.mu.RLock()
	defer p.mu.RUnlock()

	fmt.Printf("已发现的节点:\n")
	for addr := range p.clients {
		if addr == p.selfAddr {
			fmt.Printf("  - %s (self)\n", addr)
		} else {
			fmt.Printf("  - %s\n", addr)
		}
	}
}
