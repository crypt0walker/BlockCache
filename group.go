package blockcache

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
)

// 实现业务层操作，管理group命名空间

var (
	//维护group名到实际group实例的映射
	groups = make(map[string]*Group)
	//对groups操作加读写锁，以保证线程安全
	groupsMu sync.RWMutex
)

// Group表示一个命名空间
type Group struct {
	name      string
	getter    Getter
	mainCache *Cache
	//选择具体的节点，是一个节点选择器而不是一个具体节点
	peers      PeerPicker
	loader     *singleflight.Group
	expiration time.Duration
	closed     int32
	stats      groupStats
}

// groupStats 保存组的统计信息
type groupStats struct {
	loads        int64 // 加载次数
	localHits    int64 // 本地缓存命中次数
	localMisses  int64 // 本地缓存未命中次数
	peerHits     int64 // 从对等节点获取成功次数
	peerMisses   int64 // 从对等节点获取失败次数
	loaderHits   int64 // 从加载器获取成功次数
	loaderErrors int64 // 从加载器获取失败次数
	loadDuration int64 // 加载总耗时（纳秒）
}

// 需要有一个回源查询接口
type Getter interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// 需要有一个实现回源查询接口的函数类型
type GetFun func(ctx context.Context, key string) ([]byte, error)

// 有一个Getter的具体实现，receiver为GetFun
func (f GetFun) Get(ctx context.Context, key string) ([]byte, error) {
	return f(ctx, key)
}

// GroupOption 定义Group的配置选项
type GroupOption func(*Group)

// get的具体实现在newGroup中，由用户作为入参传入
func NewGroup(name string, cacheBytes int64, getter Getter, opts ...GroupOption) *Group {
	//一定要设置
	if getter == nil {
		panic("nil getter")
	}
	//新建Cache需要设置CacheOption，我们设置默认配置，代码在cache中
	cacheOpts := DefaultCacheOptions()
	//设置用户传入的最大缓存大小配置
	//防御性编程："如果你想创建一个 Group，其他配置随便你（我都准备了默认值），但唯独『最大允许花多少内存 (MaxBytes)』这件事，你必须显式告诉我！"
	cacheOpts.MaxBytes = cacheBytes

	//在初始化时创建cache
	g := &Group{
		mainCache: NewCache(cacheOpts),
		getter:    getter,
		name:      name,
		// 它的意思是：使用 singleflight 包里的 Group 结构体，创建一个新对象
		loader: &singleflight.Group{},
	}

	//函数选项模式
	for _, opt := range opts {
		opt(g)
	}
	//注册到全局映射,写锁来保证并发安全
	groupsMu.Lock()
	defer groupsMu.Unlock()

	if _, exists := groups[name]; exists {
		logrus.Warnf("Group with name %s already exists, will be replaced", name)
	}
	groups[name] = g
	logrus.Infof("Group %s created, with cacheBytes=%d, expiration=%v", name, cacheBytes, g.expiration)

	return g
}

// 几个用于创建函数选项模式的选项函数
func WithExpiration(expiration time.Duration) GroupOption {
	return func(g *Group) {
		g.expiration = expiration
	}
}
func WIthPeers(peers PeerPicker) GroupOption {
	return func(g *Group) {
		g.peers = peers
	}
}

func WithCacheOptions(opts CacheOptions) GroupOption {
	return func(g *Group) {
		g.mainCache = NewCache(opts)
	}
}

// 注册一个节点选择器，在程序运行时需要开启并给peerpicker
func (g *Group) RegisterPeerPicker(peers PeerPicker) {
	if peers == nil {
		panic("nil peer picker")
	}
	g.peers = peers
	logrus.Infof("Peer picker for group %s registered", g.name)
}

func GetGroup(name string) *Group {
	groupsMu.RLock()
	defer groupsMu.RUnlock()
	//获取逻辑
	// if g, exists := groups[name]; exists {
	// 	return g
	// }
	// return nil
	//可以直接返回，如果不存在则是零值，指针类型零值默认是nill
	return groups[name]
}

// 从缓存中获取数据，返回值使用ByteView而不是[]byte的原因是其是只读视图，否则[]byte返回的是引用，其就能够修改其中的值，不安全
func (g *Group) Get(ctx context.Context, key string) (ByteView, error) {
	//检查group是否已经关闭,原子判断
	if atomic.LoadInt32(&g.closed) == 1 {
		return ByteView{}, errors.New("group is closed")
	}

	if key == "" {
		return ByteView{}, errors.New("key is empty")
	}

	//先尝试从本地缓存中获取数据
	if val, ok := g.mainCache.Get(ctx, key); ok {
		//统计数据记录
		atomic.AddInt64(&g.stats.localHits, 1)
		return val, nil
	}
	//本地缓存未命中，尝试从对等节点获取
	return g.load(ctx, key)
}

// 从远端节点获取数据
func (g *Group) load(ctx context.Context, key string) (ByteView, error) {
	// 使用singlefilight，确保并发请求只加载一次
	startTime := time.Now()
	//此处Do方法需要确保精油一个请求执行laodData，其他请求等待
	//Do方法第一个入参为key，第二个入参为一个函数，该函数返回interface{}和error,interface{}是一个万能接口，可以返回任何类型
	viewi, err := g.loader.Do(key,
		func() (interface{}, error) {
			//传入匿名函数
			return g.loadData(ctx, key)
		})

	//记录加载时间与次数
	loadDuration := time.Since(startTime).Nanoseconds()
	//添加到总耗时
	atomic.AddInt64(&g.stats.loadDuration, loadDuration)
	//添加加载次数
	atomic.AddInt64(&g.stats.loads, 1)
	if err != nil {
		//错误次数
		atomic.AddInt64(&g.stats.loaderErrors, 1)
		return ByteView{}, err
	}
	//类型断言，由于do返回的是interface{}，而loadData返回的是ByteView，因此需要类型断言
	view := viewi.(ByteView)

	//设置到本地缓存
	if g.expiration > 0 {
		g.mainCache.AddWithExpiration(key, view, time.Now().Add(g.expiration))
	} else {
		g.mainCache.Add(key, view)
	}
	return view, nil

}

// 实际加载数据的方法
func (g *Group) loadData(ctx context.Context, key string) (ByteView, error) {
	//尝试从远端节点获取（此前已经尝试过本地缓存）
	if g.peers != nil {
		peer, ok, isSelf := g.peers.PickPeer(key)
		if ok && !isSelf {
			//正常且不是数据存储节点不是自己，则从对等节点获取数据
			value, err := peer.GetFromPeer(ctx, key)
			if err == nil {
				//统计数据记录
				atomic.AddInt64(&g.stats.peerHits, 1)
				return value, nil
			}
			//统计数据记录
			atomic.AddInt64(&g.stats.peerMisses, 1)
			logrus.Errorf("Failed to get from peer: %v", err)
		}
	}
	// 本地节点尝试从数据源加载
	bytes, err := g.getter.Get(ctx, key)
	if err != nil {
		return ByteView{}, fmt.Errorf("failed to get from getter: %v", err)
	}
	//统计数据记录
	atomic.AddInt64(&g.stats.loaderHits, 1)
	//不变封装，实际上就是使用bytes给ByteView中的data做深拷贝
	return ByteView{data: cloneBytes(bytes)}, nil
}

// getFromPeer 从其他节点获取数据
func (g *Group) getFromPeer(ctx context.Context, peer Peer, key string) (ByteView, error) {
	bytes, err := peer.Get(g.name, key)
	if err != nil {
		return ByteView{}, fmt.Errorf("failed to get from peer: %w", err)
	}
	return ByteView{data: bytes}, nil
}

// 若cache功能不仅限于读取，还涉及更新于设置等，以下是扩展方法
func (g *Group) Set(ctx context.Context, key string, value []byte) error {
	//不能简单的：g.mainCache.Add(key, value)，而是要考虑很多问题
	// 1. 防御性编程：原子检查group是否已关闭 & 参数是否为空
	if atomic.LoadInt32(&g.closed) == 1 {
		//如果group已关闭，直接返回错误
		return errors.New("cache group is closed")
	}
	if key == "" || len(value) == 0 {
		return errors.New("key is empty or value is empty")
	}
	// 2. 分布式缓存中需要防止死循环&更新的广播风暴（将广播再广播）
	// 判断是否来自peer节点的set请求
	isPeerRequest := ctx.Value("from_peer") != nil

	// 3. 提供不可变视图
	view := ByteView{data: cloneBytes(value)}

	// 4. 设置到本地缓存，包含过期时间处理逻辑
	if g.expiration > 0 {
		g.mainCache.AddWithExpiration(key, view, time.Now().Add(g.expiration))
	} else {
		g.mainCache.Add(key, view)
	}

	// 2. 防止广播风暴，只有不是来自peer节点set，同时有peerpicker才同步
	if !isPeerRequest && g.peers != nil {
		// 开启一个异步协程进行节点同步
		go g.syncToPeers(ctx, "set", key, value)
	}
	return nil
}
func (g *Group) syncToPeers(ctx context.Context, op string, key string, value []byte) {
	//1. 前置检查：是否有节点选择器
	if g.peers == nil {
		return
	}

	//2. 选择peer节点进行同步：需要计算这个key应该有哪个peer节点管理
	//PickPeer方法会返回一个peer节点，一个bool值，一个bool值表示是否是自己
	peer, ok, isSelf := g.peers.PickPeer(key)
	if !ok || isSelf {
		return
	}
	//有节点选择器，且不是自己负责，则需要将set传递至对应节点
	// 更改数据只需要单播至对应节点

	//3. 创建同步的上下文标记
	syncCtx := context.WithValue(context.Background(), "from_peer", true)

	//4. 执行远程调用
	var err error
	//两种情况
	switch op {
	case "set":
		err = peer.Set(syncCtx, g.name, key, value)
	case "delete":
		_, err = peer.Delete(g.name, key)
	}

	if err != nil {
		logrus.Errorf("Failed to sync %s to peer: %v", op, err)
	}
}
func (g *Group) Delete(ctx context.Context, key string) error {
	//1. 前置检查：group是否已经关闭
	if atomic.LoadInt32(&g.closed) == 1 {
		return errors.New("cache group is closed")
	}

	//2. 参数检查
	if key == "" {
		return errors.New("key is empty")
	}

	//3. 从本地缓存删除
	g.mainCache.Delete(key)

	//4. 检查是否来自其他peer节点同步的请求
	isPeerRequest := ctx.Value("from_peer") != nil
	if !isPeerRequest && g.peers != nil {
		//开启一个异步协程对指定的节点同步
		go g.syncToPeers(ctx, "delete", key, nil)
	}
	return nil
}

// Clear 清空缓存
func (g *Group) Clear() {
	// 检查组是否已关闭
	if atomic.LoadInt32(&g.closed) == 1 {
		return
	}

	g.mainCache.Clear()
	logrus.Infof("[KamaCache] cleared cache for group [%s]", g.name)
}

// Close 关闭组并释放资源
func (g *Group) Close() error {
	// 如果已经关闭，直接返回
	// 原子cas锁逻辑
	if !atomic.CompareAndSwapInt32(&g.closed, 0, 1) {
		return nil
	}

	// 关闭本地缓存（级联关闭，下属也得关闭）
	if g.mainCache != nil {
		g.mainCache.Close()
	}

	// 从全局组映射中移除
	groupsMu.Lock()
	delete(groups, g.name)
	groupsMu.Unlock()

	logrus.Infof("[KamaCache] closed cache group [%s]", g.name)
	return nil
}

// ListGroups 返回所有缓存组的名称
func ListGroups() []string {
	groupsMu.RLock()
	defer groupsMu.RUnlock()

	names := make([]string, 0, len(groups))
	for name := range groups {
		names = append(names, name)
	}

	return names
}

// DestroyGroup 销毁指定名称的缓存组
func DestroyGroup(name string) bool {
	groupsMu.Lock()
	defer groupsMu.Unlock()

	if g, exists := groups[name]; exists {
		g.Close()
		delete(groups, name)
		logrus.Infof("[KamaCache] destroyed cache group [%s]", name)
		return true
	}

	return false
}

// DestroyAllGroups 销毁所有缓存组
func DestroyAllGroups() {
	groupsMu.Lock()
	defer groupsMu.Unlock()

	for name, g := range groups {
		g.Close()
		delete(groups, name)
		logrus.Infof("[KamaCache] destroyed cache group [%s]", name)
	}
}
