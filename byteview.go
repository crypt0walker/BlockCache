package blockcache

// ByteView 封装了一个不可变的字节切片
// 它的主要作用是支持只读访问，避免外部修改缓存内部的底层数组
type ByteView struct {
	// 使用 data 比 b 语义更清晰，保留你的写法
	data []byte
}

// Len 实现 Value 接口，必须提供
func (b ByteView) Len() int {
	return len(b.data)
}

// ByteSlice 返回数据的【拷贝】
// 关键修正：返回值必须是 []byte，外部才能使用数据
func (b ByteView) ByteSlice() []byte {
	return cloneBytes(b.data)
}

// String 允许将缓存数据当作字符串处理
func (b ByteView) String() string {
	return string(b.data)
}

// cloneBytes 是一个内部辅助函数，专门用于拷贝切片
func cloneBytes(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
