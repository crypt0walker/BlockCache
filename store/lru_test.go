package store

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// ==========================================
// 1. 辅助测试结构与 Mock 数据
// ==========================================

// 假设 Options 在你的项目中已定义，如果没有，请取消下面注释
// type Options struct {
// 	MaxBytes        int64
// 	OnEvicted       func(key string, value Value)
// 	CleanupInterval time.Duration
// }

// String 类型实现 Value 接口，用于测试
type String string

func (d String) Len() int {
	return len(d)
}

// ==========================================
// 2. 单元测试用例
// ==========================================

// TestLRU_Basic_GetSet 测试基本的设置和获取功能
func TestLRU_Basic_GetSet(t *testing.T) {
	cache := newLRUCache(Options{MaxBytes: 100})

	k1, v1 := "key1", "value1"
	cache.Set(k1, String(v1))

	// 测试命中
	if v, ok := cache.Get(k1); !ok || string(v.(String)) != v1 {
		t.Fatalf("cache hit key1 failed, expect %s got %v", v1, v)
	}

	// 测试未命中
	if _, ok := cache.Get("key2"); ok {
		t.Fatalf("cache miss key2 failed")
	}
}

// TestLRU_Expiration 测试过期策略
func TestLRU_Expiration(t *testing.T) {
	cache := newLRUCache(Options{MaxBytes: 100})

	k, v := "expireKey", "123"
	// 设置 50毫秒后过期
	err := cache.SetWithExpire(k, String(v), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 立即获取，应该存在
	if _, ok := cache.Get(k); !ok {
		t.Fatal("key should exist immediately")
	}

	// 2. 等待 100毫秒（超过过期时间）
	time.Sleep(100 * time.Millisecond)

	// 3. 再次获取，应该不存在 (Get 内部会触发异步删除)
	if _, ok := cache.Get(k); ok {
		t.Fatal("key should be expired")
	}

	// 4. 验证异步删除是否最终清除了 map (稍微等待异步 goroutine 执行)
	time.Sleep(10 * time.Millisecond)
	cache.mu.RLock()
	if _, ok := cache.items[k]; ok {
		t.Fatal("key should be removed from internal map")
	}
	cache.mu.RUnlock()
}

// TestLRU_Memory_And_Callback 测试内存计算准确性和删除回调
func TestLRU_Memory_And_Callback(t *testing.T) {
	var evictedKey string
	var evictedVal Value

	// 定义回调
	onEvicted := func(key string, value Value) {
		evictedKey = key
		evictedVal = value
	}

	cache := newLRUCache(Options{
		MaxBytes:  1000,
		OnEvicted: onEvicted,
	})

	k, v := "k1", "v1" // len(k)=2, len(v)=2, total=4
	cache.Set(k, String(v))

	// 验证内存占用
	expectedUsage := int64(len(k) + len(v))
	if cache.usedBytes != expectedUsage {
		t.Fatalf("expected usedBytes %d, got %d", expectedUsage, cache.usedBytes)
	}

	// 更新同一个 Key，值变长
	v2 := "value2" // len=6
	cache.Set(k, String(v2))

	expectedUsage = int64(len(k) + len(v2))
	if cache.usedBytes != expectedUsage {
		t.Fatalf("expected usedBytes after update %d, got %d", expectedUsage, cache.usedBytes)
	}

	// 主动删除
	cache.Delete(k)

	// 验证回调是否执行
	if evictedKey != k || string(evictedVal.(String)) != v2 {
		t.Fatalf("callback failed, expected %s-%s, got %s-%v", k, v2, evictedKey, evictedVal)
	}

	// 验证内存是否归零
	if cache.usedBytes != 0 {
		t.Fatalf("memory leak, expected 0, got %d", cache.usedBytes)
	}
}

// TestLRU_Order 验证 LRU 特性：Get 操作是否将元素移到队尾
func TestLRU_Order(t *testing.T) {
	cache := newLRUCache(Options{MaxBytes: 100})

	cache.Set("k1", String("v1"))
	cache.Set("k2", String("v2"))
	cache.Set("k3", String("v3"))

	// 当前顺序 (Front -> Back): k1, k2, k3

	// 访问 k1，k1 应该变成最新的（移动到 Back）
	cache.Get("k1")

	// 预期顺序 (Front -> Back): k2, k3, k1
	// 验证最老的元素 (Front)
	if e := cache.ll.Front(); e.Value.(*lruEntry).key != "k2" {
		t.Fatalf("expected front to be k2, got %s", e.Value.(*lruEntry).key)
	}
	// 验证最新的元素 (Back)
	if e := cache.ll.Back(); e.Value.(*lruEntry).key != "k1" {
		t.Fatalf("expected back to be k1, got %s", e.Value.(*lruEntry).key)
	}
}

// TestLRU_Concurrency 验证并发安全性 (必须配合 go test -race 使用)
func TestLRU_Concurrency(t *testing.T) {
	cache := newLRUCache(Options{MaxBytes: 1000})
	var wg sync.WaitGroup

	// 开启 100 个写协程
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			key := fmt.Sprintf("%d", val%10) // Key 范围 0-9，制造冲突
			cache.Set(key, String(fmt.Sprintf("val-%d", val)))
		}(i)
	}

	// 开启 100 个读协程
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			key := fmt.Sprintf("%d", val%10)
			cache.Get(key)
		}(i)
	}

	// 开启 50 个删除协程
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			key := fmt.Sprintf("%d", val%10)
			cache.Delete(key)
		}(i)
	}

	wg.Wait()
}

// TestLRU_SetWithExpire_Overlap 测试覆盖 Key 时的过期时间处理
func TestLRU_SetWithExpire_Overlap(t *testing.T) {
	cache := newLRUCache(Options{MaxBytes: 100})

	// 1. 设置带过期的 Key
	cache.SetWithExpire("k1", String("v1"), 100*time.Millisecond)

	// 2. 覆盖该 Key，但不设置过期 (duration=0)
	cache.Set("k1", String("v2"))

	// 3. 等待之前的过期时间过去
	time.Sleep(150 * time.Millisecond)

	// 4. 应该依然能获取到（因为 Set 会清除过期时间）
	if v, ok := cache.Get("k1"); !ok || string(v.(String)) != "v2" {
		t.Fatal("key should survive after being updated with no expiration")
	}
}
