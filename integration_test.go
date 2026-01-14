package blockcache_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	blockcache "github.com/crypt0walker/BlockCache"
)

// TestMultiNodeInteraction 测试多节点交互
func TestMultiNodeInteraction(t *testing.T) {
	// 定义多个节点的地址
	addrs := []string{":50051", ":50052", ":50053"}

	// 用于存储服务器和组
	var servers []*blockcache.Server
	var pickers []*blockcache.ClientPicker
	var groups []*blockcache.Group

	// 启动多个节点
	for i, addr := range addrs {
		nodeID := fmt.Sprintf("node%d", i+1)

		// 创建服务器
		server, err := blockcache.NewServer(addr, "test-cache",
			blockcache.WithEtcdEndpoints([]string{"localhost:2379"}),
			blockcache.WithDialTimeout(5*time.Second),
		)
		if err != nil {
			t.Fatalf("创建节点 %s 失败: %v", nodeID, err)
		}
		servers = append(servers, server)

		// 启动服务器
		go func(s *blockcache.Server) {
			if err := s.Start(); err != nil {
				t.Logf("服务器启动失败: %v", err)
			}
		}(server)

		// 创建节点选择器
		picker, err := blockcache.NewClientPicker(addr)
		if err != nil {
			t.Fatalf("创建节点选择器失败: %v", err)
		}
		pickers = append(pickers, picker)

		// 创建缓存组
		group := blockcache.NewGroup(fmt.Sprintf("test-group-%d", i), 2<<20,
			blockcache.GetterFunc(func(ctx context.Context, key string) ([]byte, error) {
				return []byte(fmt.Sprintf("data-from-%s-%s", nodeID, key)), nil
			}),
		)
		group.RegisterPeers(picker)
		groups = append(groups, group)
	}

	// 等待节点注册完成
	t.Log("等待节点注册...")
	time.Sleep(3 * time.Second)

	// 测试1: 验证节点发现
	t.Run("NodeDiscovery", func(t *testing.T) {
		t.Log("测试节点发现...")
		for i, picker := range pickers {
			picker.PrintPeers()
			t.Logf("节点 %d 已发现对等节点", i+1)
		}
	})

	// 测试2: 跨节点数据访问
	t.Run("CrossNodeDataAccess", func(t *testing.T) {
		t.Log("测试跨节点数据访问...")
		ctx := context.Background()

		// 在第一个节点设置数据
		key := "test-key-1"
		value := []byte("test-value-1")
		if err := groups[0].Set(ctx, key, value); err != nil {
			t.Fatalf("设置数据失败: %v", err)
		}

		// 等待同步
		time.Sleep(1 * time.Second)

		// 从其他节点获取数据
		for i := 1; i < len(groups); i++ {
			val, err := groups[i].Get(ctx, key)
			if err != nil {
				t.Logf("节点 %d 获取数据时出错: %v", i+1, err)
			} else {
				t.Logf("节点 %d 成功获取数据: %s", i+1, val.String())
			}
		}
	})

	// 测试3: 并发访问
	t.Run("ConcurrentAccess", func(t *testing.T) {
		t.Log("测试并发访问...")
		ctx := context.Background()
		var wg sync.WaitGroup

		// 并发写入多个键
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				key := fmt.Sprintf("concurrent-key-%d", idx)
				value := []byte(fmt.Sprintf("concurrent-value-%d", idx))

				nodeIdx := idx % len(groups)
				if err := groups[nodeIdx].Set(ctx, key, value); err != nil {
					t.Logf("并发写入失败 (key=%s): %v", key, err)
				}
			}(i)
		}
		wg.Wait()

		time.Sleep(1 * time.Second)

		// 验证数据
		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("concurrent-key-%d", i)
			nodeIdx := (i + 1) % len(groups) // 从不同节点读取

			val, err := groups[nodeIdx].Get(ctx, key)
			if err != nil {
				t.Logf("读取 %s 失败: %v", key, err)
			} else {
				t.Logf("成功读取 %s = %s", key, val.String())
			}
		}
	})

	// 测试4: 超时场景
	t.Run("TimeoutScenario", func(t *testing.T) {
		t.Log("测试超时场景...")

		// 创建一个短超时的context
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		// 创建一个慢速getter
		slowGroup := blockcache.NewGroup("slow-group", 2<<20,
			blockcache.GetterFunc(func(ctx context.Context, key string) ([]byte, error) {
				time.Sleep(200 * time.Millisecond) // 模拟慢速操作
				return []byte("slow-data"), nil
			}),
		)
		slowGroup.RegisterPeers(pickers[0])

		_, err := slowGroup.Get(ctx, "slow-key")
		if err == nil {
			t.Error("期望超时错误，但操作成功")
		} else if ctx.Err() == context.DeadlineExceeded {
			t.Logf("正确处理超时: %v", err)
		} else {
			t.Logf("获得错误: %v", err)
		}
	})

	// 测试5: 统计信息
	t.Run("Statistics", func(t *testing.T) {
		t.Log("测试统计信息...")
		for i, group := range groups {
			stats := group.Stats()
			t.Logf("节点 %d 统计: Loads=%d, LocalHits=%d, PeerHits=%d",
				i+1, stats.Loads, stats.LocalHits, stats.PeerHits)
		}
	})

	// 清理
	t.Cleanup(func() {
		for _, server := range servers {
			server.Stop()
		}
		for _, picker := range pickers {
			picker.Close()
		}
	})
}
