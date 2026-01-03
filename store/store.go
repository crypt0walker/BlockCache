package store

import "time"

// Value 定义了缓存中值的接口
type Value interface {
	Len() int
}

// Options 通用缓存配置选项
type Options struct {
	MaxBytes        int64  // 最大的缓存字节数（用于 lru）
	BucketCount     uint16 // 缓存的桶数量（用于 lru-2）
	CapPerBucket    uint16 // 每个桶的容量（用于 lru-2）
	Level2Cap       uint16 // lru-2 中二级缓存的容量（用于 lru-2）
	CleanupInterval time.Duration
	OnEvicted       func(key string, value Value)
}
