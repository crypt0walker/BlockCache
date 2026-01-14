package blockcache

//分布式缓存
import (
	"context"
	"fmt"
	"time"

	pb "github.com/crypt0walker/BlockCache/pb"
	"github.com/crypt0walker/BlockCache/registry"
	"github.com/sirupsen/logrus"
	clientv3 "go.etcd.io/etcd/client/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// client结构体代表了一个到远程节点的连接实例
type Client struct {
	// 基础身份信息
	addr    string
	svcName string
	// 依赖的组件
	// etcdCli 用于服务发现，指向ETCD的客户端指针
	etcdCli *clientv3.Client
	// 3. 核心通讯连接
	// grpc底层的tcp连接对象
	conn *grpc.ClientConn
	// 4. 功能接口存根 stub
	grpcCli pb.BlockCacheClient // grpc自动生成的客户端接口实现，是conn的包装，我们一般直接使用它
}

// GetFromPeer implements [Peer].
func (c *Client) GetFromPeer(ctx context.Context, key string) ([]byte, error) {
	panic("unimplemented")
}

// 防御性编程：将Client类型断言为Peer接口
// 写法为：var _：我要声明一个丢弃的变量（不占内存，只为了触发类型检查）。
// 接口名：可以是你自己的 Peer，也可以是标准库的 io.Reader 等。
// =：赋值。
// (*实现类结构体)(nil)：构造一个该结构体的空指针。
var _ Peer = (*Client)(nil)

func NewClient(addr string, svcName string, etcdCli *clientv3.Client) (*Client, error) {
	var err error
	//兜底创建etcd
	if etcdCli == nil {
		etcdCli, err = clientv3.New(clientv3.Config{
			Endpoints:   registry.DefaultConfig.Endpoints,
			DialTimeout: registry.DefaultConfig.DialTimeout,
		})
		if err != nil {
			return nil, err
		}
	}
	//1.创建一个短增的context用于连接超时控制
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	//2.使用DialContext（内部就不需要使用WithTimeout选项了，时间由ctx控制）
	//gRPC使用的核心知识
	conn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %v", addr, err)
	}

	grpcClient := pb.NewBlockCacheClient(conn)

	client := &Client{
		addr:    addr,
		svcName: svcName,
		etcdCli: etcdCli,
		conn:    conn,
		grpcCli: grpcClient,
	}
	return client, nil
}

func (c *Client) Get(ctx context.Context, group, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := c.grpcCli.Get(ctx, &pb.Request{
		Group: group,
		Key:   key,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get value from blockcache: %v", err)
	}

	return resp.GetValue(), nil
}

func (c *Client) Delete(group, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resp, err := c.grpcCli.Delete(ctx, &pb.Request{
		Group: group,
		Key:   key,
	})
	if err != nil {
		return false, fmt.Errorf("failed to delete value from blockcache: %v", err)
	}

	return resp.GetValue(), nil
}

func (c *Client) Set(ctx context.Context, group, key string, value []byte) error {
	resp, err := c.grpcCli.Set(ctx, &pb.Request{
		Group: group,
		Key:   key,
		Value: value,
	})
	if err != nil {
		return fmt.Errorf("failed to set value to blockcache: %v", err)
	}
	logrus.Infof("grpc set request resp: %+v", resp)

	return nil
}

// 中断该client的TCP conn
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
