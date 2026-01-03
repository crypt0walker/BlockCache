# KamaCache-Go

一个基于 Go 语言实现的分布式缓存系统，支持一致性哈希、防缓存击穿和 gRPC 通信。

## 系统架构

这张图展示了 KamaCache 的分布式集群架构。左侧是接入节点 (Node A)，右侧是数据持有节点 (Node B)。

图中描绘了数据流的三段式跳跃：

1. 客户端请求首先到达 Node A。
2. Node A 的核心逻辑层通过一致性哈希路由，发现数据归属于 Node B，于是通过 **gRPC 协议** 发起远程调用。
3. Node B 作为数据所有者，在本地缓存未命中时，触发**回源 (Getter)** 逻辑去查询数据库，并将数据返回给 Node A。

![](./README.assets/High-Level.drawio.svg)