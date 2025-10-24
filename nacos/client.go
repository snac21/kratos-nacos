package nacos

import (
	"fmt"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/common/logger"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// Client 封装 Nacos 客户端和选项
type Client struct {
	opts         *Options
	namingClient naming_client.INamingClient
	configClient config_client.IConfigClient
}

// NewClient 创建一个新的 Nacos 客户端实例
func NewClient(opts ...Option) (*Client, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	if len(o.ServerConfigs) == 0 {
		return nil, fmt.Errorf("nacos server address is required")
	}

	// 配置 nacos-sdk-go 的日志
	logger.SetLogger(nil)
	loggerConfig := logger.BuildLoggerConfig(*o.ClientConfig)
	_ = logger.InitLogger(loggerConfig)

	// 创建服务注册客户端
	namingClient, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  o.ClientConfig,
			ServerConfigs: o.ServerConfigs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos naming client: %w", err)
	}

	// 创建配置中心客户端
	configClient, err := clients.NewConfigClient(
		vo.NacosClientParam{
			ClientConfig:  o.ClientConfig,
			ServerConfigs: o.ServerConfigs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos config client: %w", err)
	}

	return &Client{
		opts:         o,
		namingClient: namingClient,
		configClient: configClient,
	}, nil
}
