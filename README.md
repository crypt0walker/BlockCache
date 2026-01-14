# BlockCache

[![Go Version](https://img.shields.io/badge/Go-1.25%2B-blue)](https://golang.org)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Tests](https://img.shields.io/badge/tests-passing-brightgreen)](integration_test.go)

一个高性能、分布式、生产级的缓存系统，使用 Go 实现。支持多种缓存策略、自动服务发现、一致性哈希、防缓存击穿等特性。

## ✨ 特性

- 🚀 **高性能**: LRU2 多级缓存 + 分桶并发控制
- 🌐 **分布式**: 基于 etcd 的服务发现和一致性哈希
- 🛡️ **防击穿**: Singleflight 防止缓存击穿
- 📊 **可观测**: 内置统计信息和性能指标
- 🔧 **易扩展**: 插件化设计，支持自定义缓存策略
- ⚡ **gRPC**: 高效的节点间通信

## 🏗️ 架构

BlockCache 采用分层架构设计:

```
┌─────────────────────────────────────────────────────┐
│              Application Layer (用户应用)             │
└─────────────────┬───────────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────────┐
│          Business Layer (Group + Getter)           │
│  - 命名空间管理                                      │
│  - 回源机制                                         │
│  - Singleflight 防击穿                              │
└─────────────────┬───────────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────────┐
│          Cache Layer (Cache)                       │
│  - 并发安全的缓存封装                                 │
│  - 延迟初始化                                        │
│  - 统计信息收集                                      │
└─────────────────┬───────────────────────────────────┘
                  │
┌─────────────────▼───────────────────────────────────┐
│          Storage Layer (Store)                     │
│  - LRU: 最近最少使用                                 │
│  - LRU2: 二级LRU (防缓存污染)                         │
└─────────────────┬───────────────────────────────────┘
                  │
    ┌─────────────▼─────────────┐
    │    Distributed Layer      │
    ├───────────────────────────┤
    │ • Consistent Hash (一致性哈希) 
    │ • Service Discovery (etcd) 
    │ • gRPC Communication       
    └───────────────────────────┘
```

## 📦 安装

```bash
go get github.com/crypt0walker/BlockCache
```

## 🚀 快速开始

### 单机模式

```go
package main

import (
    "context"
    "fmt"
    blockcache "github.com/crypt0walker/BlockCache"
)

func main() {
    // 创建缓存组
    group := blockcache.NewGroup("users", 1<<20, 
        blockcache.GetterFunc(func(ctx context.Context, key string) ([]byte, error) {
            // 从数据库或其他数据源加载数据
            return []byte(fmt.Sprintf("user-%s-data", key)), nil
        }),
    )

    // 获取数据
    ctx := context.Background()
    value, err := group.Get(ctx, "user123")
    if err != nil {
        panic(err)
    }
    
    fmt.Println(value.String()) // Output: user-user123-data
}
```

### 分布式模式

#### 1. 启动 etcd

```bash
brew install etcd
brew services start etcd
```

#### 2. 启动多个节点

**节点 1**:
```bash
go run example/test.go -port 8001 -node A
```

**节点 2**:
```bash
go run example/test.go -port 8002 -node B
```

**节点 3**:
```bash
go run example/test.go -port 8003 -node C
```

#### 3. 代码示例

```go
package main

import (
    "context"
    "time"
    blockcache "github.com/crypt0walker/BlockCache"
)

func main() {
    addr := ":8001"
    
    // 创建服务器
    server, _ := blockcache.NewServer(addr, "my-cache",
        blockcache.WithEtcdEndpoints([]string{"localhost:2379"}),
        blockcache.WithDialTimeout(5*time.Second),
    )
    
    // 启动服务器
    go server.Start()
    
    // 创建节点选择器
    picker, _ := blockcache.NewClientPicker(addr)
    
    // 创建缓存组并注册节点选择器
    group := blockcache.NewGroup("users", 2<<20, 
        blockcache.GetterFunc(func(ctx context.Context, key string) ([]byte, error) {
            return loadFromDB(key)
        }),
    )
    group.RegisterPeers(picker)
    
    // 使用缓存 - 会自动路由到对应节点
    ctx := context.Background()
    value, _ := group.Get(ctx, "user123")
    
    // 设置数据 - 会自动同步到对应节点
    group.Set(ctx, "user456", []byte("data"))
}
```

## 📊 性能指标

```bash
# 运行性能测试
go test -bench=. -benchmem ./store/

# 示例输出
BenchmarkLRU2Cache-10     1000000    1234 ns/op    256 B/op    2 allocs/op
```

### 基准测试结果

| 场景 | QPS | 延迟 (P99) | 内存 |
|------|-----|-----------|------|
| 本地缓存命中 | ~1M/s | <1ms | 低 |
| 远程节点访问 | ~100K/s | ~10ms | 中 |
| 数据源回源 | ~10K/s | ~100ms | 高 |

## 🧪 测试

```bash
# 运行所有测试
go test ./...

# 运行集成测试
go test -v -run TestMultiNodeInteraction

# 查看覆盖率
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## 📖 核心概念

### ByteView - 不可变视图
保证缓存数据不被外部修改，所有读取都返回数据拷贝。

### Group - 缓存命名空间
隔离不同业务的缓存数据，每个 Group 有独立的配置和统计。

### Getter - 数据源
当缓存未命中时，通过 Getter 从数据源加载数据。

### Singleflight - 防击穿
确保同一时刻只有一个请求去加载数据，其他请求等待结果。

### 一致性哈希
节点增减时，只需重新分配部分数据，减少数据迁移。

### 服务发现
基于 etcd 实现自动服务注册和发现，节点动态上下线。

## 🔧 配置选项

### Cache 配置

```go
opts := blockcache.CacheOptions{
    CacheType:    store.LRU2,        // 缓存类型
    MaxBytes:     8 * 1024 * 1024,   // 最大内存
    BucketCount:  16,                 // 分桶数量
    CapPerBucket: 512,                // 每桶容量
    Level2Cap:    256,                // 二级缓存容量
    CleanupTime:  time.Minute,        // 清理间隔
}
cache := blockcache.NewCache(opts)
```

### Server 配置

```go
server, _ := blockcache.NewServer(addr, svcName,
    blockcache.WithEtcdEndpoints([]string{"localhost:2379"}),
    blockcache.WithDialTimeout(5*time.Second),
    blockcache.WithTLS(certFile, keyFile),
)
```

## 📂 项目结构

```
BlockCache/
├── byteview.go          # 不可变数据视图
├── cache.go             # 缓存封装层
├── group.go             # 业务逻辑层
├── server.go            # gRPC 服务端
├── client.go            # gRPC 客户端
├── peers.go             # 节点管理
│
├── store/               # 存储层
│   ├── store.go         # 存储接口
│   ├── lru.go           # LRU 实现
│   └── lru2.go          # LRU2 实现
│
├── consistenthash/      # 一致性哈希
│   ├── con_hash.go      # 哈希实现
│   └── config.go        # 配置
│
├── registry/            # 服务注册
│   └── register.go      # etcd 注册
│
├── singleflight/        # 防击穿
│   └── singleflight.go  
│
├── pb/                  # gRPC 定义
│   ├── blockcache.proto
│   ├── blockcache.pb.go
│   └── blockcache_grpc.pb.go
│
├── example/             # 示例代码
│   └── test.go
│
└── integration_test.go  # 集成测试
```

## 🎓 学习资源

- [LEARNING.md](LEARNING.md) - 从0到1构建指南
- [example/test.go](example/test.go) - 完整示例
- [integration_test.go](integration_test.go) - 集成测试

## 🤝 贡献

欢迎提交 Issue 和 Pull Request！

1. Fork 项目
2. 创建特性分支 (`git checkout -b feature/AmazingFeature`)
3. 提交更改 (`git commit -m 'Add some AmazingFeature'`)
4. 推送到分支 (`git push origin feature/AmazingFeature`)
5. 开启 Pull Request

## 📋 TODO

- [ ] 支持更多缓存策略 (LFU, ARC)
- [ ] 添加 Prometheus 监控
- [ ] 支持 Redis 作为后端存储
- [ ] 实现热key检测
- [ ] 添加管理界面

## 📄 License

本项目采用 MIT 许可证 - 详见 [LICENSE](LICENSE) 文件

**致谢原始作者**:
- 原始项目版权归 **程序员Carl** (2025) 所有
- 本项目的修改和增强部分版权归 **crypt0walker** (2026) 所有
- 更多信息请参阅 [NOTICE](NOTICE) 文件

## 🙏 致谢

本项目基于 [程序员Carl](https://github.com/) 的原始工作进行开发和增强。

特别感谢:
- **程序员Carl** - 原始项目作者和核心设计
- **GroupCache** - 分布式缓存设计理念
- **etcd** - 服务发现机制
- **gRPC** - 高效RPC框架

## 📮 联系方式

- GitHub: [@crypt0walker](https://github.com/crypt0walker)
- Email: your-email@example.com

---

⭐ 如果这个项目对你有帮助,请给个 Star!