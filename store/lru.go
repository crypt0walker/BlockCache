// 基于golang标准库list实现的LRU缓存
package store

import (
	"container/list"
	"sync"
	"time"
)

type lruCache struct {
	//并发安全
	mu          sync.RWMutex
	maxBytes    int64                    // 最大内存容量
	usedBytes   int64                    // 当前已使用内存
	ll          *list.List               // 双向链表，存储缓存数据的访问顺序
	items       map[string]*list.Element // 键到双向链表节点的映射，后端是最新访问的节点
	expiredTime map[string]time.Time     // 键到过期时间的映射
	// 回调函数，在条目被移除时作为参数传入使用，执行函数中的内容，如清理资源等
	onEvicted func(key string, value Value) // 某条记录被移除时的回调函数，可为nil
	//过期策略参数
	cleanupInterval time.Duration
	cleanupTicker   *time.Ticker
	//优雅关闭清理协程
	stopCleanup chan struct{}
	//确保只关闭一次
	stopOnce sync.Once // 保证只关闭一次

}

// 缓存中的一个条目
// 为什么要定义lruEntry结构体，包含key和value，而不是直接使用Value类型？
// 因为在删除缓存条目时（删除最后一个节点），我们需要知道对应的key，以便从cache映射中删除该条目。
// 如果只存储Value类型，我们将无法获取对应的key，导致无法正确维护cache映射的一致性。
type lruEntry struct {
	key string
	// Value是空interface接口类型，可以存储任意类型。但是提取时不知道具体类型，需要类型断言，否则将引发错误
	value Value
}

// newLRUCache 创建一个新的 LRU 缓存实例，Options是预先定义的配置结构体
func newLRUCache(opts Options) *lruCache {
	// 设置默认清理间隔，避免对Tiker传入0值导致错误
	cleanupInterval := opts.CleanupInterval
	if cleanupInterval <= 0 {
		cleanupInterval = time.Minute
	}
	//创建lruCache实例
	c := &lruCache{
		maxBytes:        opts.MaxBytes,
		ll:              list.New(),
		items:           make(map[string]*list.Element),
		expiredTime:     make(map[string]time.Time),
		onEvicted:       opts.OnEvicted,
		cleanupInterval: cleanupInterval,
		stopCleanup:     make(chan struct{}),
	}
	//启动时间轮询器
	c.cleanupTicker = time.NewTicker(c.cleanupInterval)
	//启动定期清理过期条目的协程
	go c.cleanupLoop()
	return c
}

// Get 获取缓存项，如果存在且未过期则返回
// c在此处是：指向lruCache实例的指针，是Receiver，方法的接收者
func (c *lruCache) Get(key string) (value Value, ok bool) {
	//加读锁
	c.mu.RLock()
	ele, ok := c.items[key]
	if !ok {
		c.mu.RUnlock()
		return nil, false
	}
	//存在，检查是否过期
	if expireTime, ok := c.expiredTime[key]; ok {
		if time.Now().After(expireTime) {
			//已过期，删除该元素
			//锁升级
			c.mu.RUnlock()
			//Delete方法会加写锁
			//异步删除，避免阻塞
			go c.Delete(key)
			return nil, false
		}
	}
	//未过期.或者没有设置过期时间
	// //获取值，并释放读锁
	entry := ele.Value.(*lruEntry)
	value = entry.value
	c.mu.RUnlock()
	//将该元素移动到链表后端，表示最近使用过
	c.mu.Lock()
	//再次检查元素是否存在，防止在释放读锁和获取写锁之间被删除
	if _, ok := c.items[key]; ok {
		//注意我们代码中把后端作为最新
		c.ll.MoveToBack(ele)
	}
	c.mu.Unlock()
	return value, true
}

// 从map中删除元素，并更新内存使用量
func (c *lruCache) Delete(key string) bool {
	//加锁
	c.mu.Lock()
	defer c.mu.Unlock()
	//检查元素是否存在
	if ele, ok := c.items[key]; ok {
		c.removeElement(ele)
		return true
	}
	return false
}

// removeElement 从双向链表、哈希表、过期时间map中删除元素，调用此方法前必须持有锁
func (c *lruCache) removeElement(elem *list.Element) {
	//此处为什么要用elem.Value.(*lruEntry)进行类型断言？
	//因为list.Element的Value字段是一个空接口类型(interface{}),它可以存储任何类型的值。
	//在我们的实现中，我们将lruEntry结构体的指针存储在list.Element的Value字段中。
	//为了访问lruEntry的字段（如key和value），我们需要将elem.Value断言为*lruEntry类型。
	entry := elem.Value.(*lruEntry)
	//标准库
	c.ll.Remove(elem)
	//标准库map,key
	delete(c.items, entry.key)
	delete(c.expiredTime, entry.key)
	//更新已使用内存,减去key的大小和Value的大小
	c.usedBytes -= int64(len(entry.key) + entry.value.Len())
	//调用回调函数
	if c.onEvicted != nil {
		c.onEvicted(entry.key, entry.value)
	}
}

// Set 应该区分有设置过期时间和没有设置过期时间的情况
func (c *lruCache) SetWithExpiration(key string, value Value, duration time.Duration) error {
	//加写锁
	c.mu.Lock()
	defer c.mu.Unlock()
	//先设置/更新过期时间
	if duration > 0 {
		c.expiredTime[key] = time.Now().Add(duration)
	} else {
		//如果duration<=0，表示不设置过期时间，删除可能存在的过期时间记录
		delete(c.expiredTime, key)
	}
	//检查是否存在该key
	if ele, ok := c.items[key]; ok {
		// 更新已存在的条目
		entry := ele.Value.(*lruEntry)
		// 需要合理计算当前已经使用的内存大小：减去原有条目的大小，加上新条目的大小
		c.usedBytes += int64(value.Len() - entry.value.Len())
		entry.value = value
		c.ll.MoveToBack(ele)
	} else {
		// 添加新条目
		entry := &lruEntry{key: key, value: value}
		elem := c.ll.PushBack(entry)
		c.items[key] = elem
		// 更新已使用内存大小
		c.usedBytes += int64(len(key) + value.Len())
	}

	// 检查是否需要淘汰旧项，仅在超过最大内存限制时调用
	c.removeOldest()
	return nil
}

func (c *lruCache) Set(key string, value Value) error {
	return c.SetWithExpiration(key, value, 0)
}

// cleanupLoop 定期清理过期条目的协程，接收清理信号或关闭信号
func (c *lruCache) cleanupLoop() {
	//使用for-select结构，监听清理定时器和关闭信号
	for {
		select {
		case <-c.cleanupTicker.C:
			c.mu.Lock()
			//过期清理
			c.deleteExpired()
			c.mu.Unlock()
		case <-c.stopCleanup:
			c.cleanupTicker.Stop()
			return
		}
	}

}

// close 关闭缓存，停止清理协程，同时确保只关闭一次
func (c *lruCache) Close() {
	c.stopOnce.Do(func() {
		if c.cleanupTicker != nil {
			c.cleanupTicker.Stop()
		}
		close(c.stopCleanup)
	})
}

// 仅在 Set 且 usedBytes > maxBytes 时调用
// 调用者已加锁
func (c *lruCache) removeOldest() {
	// 只做这一件事：内存满了踢队头
	for c.maxBytes > 0 && c.usedBytes > c.maxBytes && c.ll.Len() > 0 {
		elem := c.ll.Front()
		if elem != nil {
			c.removeElement(elem)
		}
	}
}

// 仅在后台 cleanupLoop (Ticker) 中调用
// 调用者已加锁（或函数内部加锁）
func (c *lruCache) deleteExpired() {
	// 增加限制，防止一次锁太久 (Stop-The-World)
	const maxScan = 100
	scanned := 0

	now := time.Now()
	for key, expTime := range c.expiredTime {
		if now.After(expTime) {
			if elem, ok := c.items[key]; ok {
				c.removeElement(elem)
			}
		}

		// 保护机制：处理了一部分就停手，释放锁给业务用
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

// Clear 清空缓存
func (c *lruCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 如果设置了回调函数，遍历所有项调用回调
	if c.onEvicted != nil {
		for _, elem := range c.items {
			entry := elem.Value.(*lruEntry)
			c.onEvicted(entry.key, entry.value)
		}
	}

	c.ll.Init()
	c.items = make(map[string]*list.Element)
	c.expiredTime = make(map[string]time.Time)
	c.usedBytes = 0
}

// Len 返回缓存中的项数
func (c *lruCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ll.Len()
}
