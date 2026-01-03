// 基于golang标准库list实现的LRU缓存
package store

import (
	"container/list"
	"time"
)

type lruCache struct {
	maxBytes    int64                    // 最大内存容量
	usedBytes   int64                    // 当前已使用内存
	ll          *list.List               // 双向链表，存储缓存数据的访问顺序
	items       map[string]*list.Element // 键到双向链表节点的映射
	expiredTime map[string]time.Time     // 键到过期时间的映射
	// 回调函数，在条目被移除时作为参数传入使用，执行函数中的内容，如清理资源等
	onEvicted func(key string, value Value) // 某条记录被移除时的回调函数，可为nil
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
	//创建lruCache实例
	c := &lruCache{
		maxBytes:    opts.MaxBytes,
		ll:          list.New(),
		items:       make(map[string]*list.Element),
		expiredTime: make(map[string]time.Time),
		onEvicted:   opts.OnEvicted,
	}
	return c
}

// Get 获取缓存项，如果存在且未过期则返回
func (c *lruCache) Get(key string) (Value, bool) {
	if ele, ok := c.items[key]; ok {
		//检查是否过期
		if expTime, exists := c.expiredTime[key]; exists {
			if time.Now().After(expTime) {
				//已过期，删除缓存项
				c.removeElement(ele)
				return nil, false
			}
		}
	}
	// 检查是否过期
	if expTime, hasExp := c.expiredTime[key]; hasExp && time.Now().After(expTime) {
		//若已经过期，删除该元素
		// 异步删除过期项，避免在读锁内操作
		go c.Delete(key)
		return nil, false
	}
	//存在且未过期，移动到队尾表示最近使用
	c.ll.MoveToFront(c.items[key])
	kv := c.items[key].Value.(*lruEntry)
	return kv.value, true
}

// 从map中删除元素，并更新内存使用量
func (c *lruCache) Delete(key string) bool {
	//暂时不考虑锁
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

func (c *lruCache) Set(key string, value Value) int {
	if ele, ok := c.items[key]; ok {
		// 更新已存在的条目
		entry := ele.Value.(*lruEntry)
		c.usedBytes -= int64(len(key) + entry.value.Len())
		entry.value = value
		c.ll.MoveToFront(ele)
	} else {
		// 添加新条目
		entry := &lruEntry{key: key, value: value}
		c.items[key] = c.ll.PushFront(entry)
		c.usedBytes += int64(len(key) + value.Len())
	}
	return 0
}
