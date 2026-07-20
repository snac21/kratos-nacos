package nacos

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/nacos-group/nacos-sdk-go/v2/clients"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// Client 封装 Nacos 客户端和选项
type Client struct {
	opts         *Options
	logger       *slog.Logger
	NamingClient naming_client.INamingClient
	ConfigClient config_client.IConfigClient
	mu           sync.Mutex
	activeWatch  map[string]*sharedSubscription
}

// Logger 返回客户端绑定的 slog.Logger
func (c *Client) Logger() *slog.Logger {
	if c == nil || c.logger == nil {
		return slog.Default()
	}
	return c.logger
}

// NewClient 创建一个新的 Nacos 客户端实例
func NewClient(opts ...Option) (*Client, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	if o.Logger == nil {
		o.Logger = slog.Default()
	}

	if len(o.ServerConfigs) == 0 {
		return nil, fmt.Errorf("nacos server address is required")
	}

	// 创建服务注册客户端
	namingClient, err := clients.NewNamingClient(
		vo.NacosClientParam{
			ClientConfig:  o.ClientConfig,
			ServerConfigs: o.ServerConfigs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos naming Client: %w", err)
	}

	// 创建配置中心客户端
	configClient, err := clients.NewConfigClient(
		vo.NacosClientParam{
			ClientConfig:  o.ClientConfig,
			ServerConfigs: o.ServerConfigs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create nacos config Client: %w", err)
	}

	return &Client{
		opts:         o,
		logger:       o.Logger,
		NamingClient: namingClient,
		ConfigClient: configClient,
		activeWatch:  make(map[string]*sharedSubscription),
	}, nil
}
