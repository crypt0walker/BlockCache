package registry

//服务注册
import (
	"context"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Config 定义etcd客户端配置
type Config struct {
	Endpoints   []string      // 集群地址
	DialTimeout time.Duration // 连接超时时间
}

// DefaultConfig 提供默认配置
var DefaultConfig = &Config{
	Endpoints:   []string{"localhost:2379"},
	DialTimeout: 5 * time.Second,
}

// Register 注册服务到etcd
func Register(svcName, addr string, stopCh chan error) error {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   DefaultConfig.Endpoints,
		DialTimeout: DefaultConfig.DialTimeout,
	})
	if err != nil {
		return err
	}

	// 创建租约
	lease, err := cli.Grant(context.Background(), 10) // 10秒TTL
	if err != nil {
		cli.Close()
		return err
	}

	// 注册服务
	key := "/services/" + svcName + "/" + addr
	_, err = cli.Put(context.Background(), key, addr, clientv3.WithLease(lease.ID))
	if err != nil {
		cli.Close()
		return err
	}

	// 保持租约活跃
	keepAliveCh, err := cli.KeepAlive(context.Background(), lease.ID)
	if err != nil {
		cli.Close()
		return err
	}

	// 监听停止信号
	go func() {
		for {
			select {
			case <-stopCh:
				cli.Revoke(context.Background(), lease.ID)
				cli.Close()
				return
			case <-keepAliveCh:
				// 保持连接
			}
		}
	}()

	return nil
}
