package nacos

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nacos-group/nacos-sdk-go/v2/common/constant"
)

// Options 封装了 Nacos 客户端和服务注册所需的配置
type Options struct {
	// Nacos 客户端配置
	ClientConfig *constant.ClientConfig
	// Nacos 服务端地址
	ServerConfigs []constant.ServerConfig

	// 服务注册相关配置
	GroupName string   // 服务注册的分组名
	Clusters  []string // 服务注册的集群名
	Weight    float64  // 权重
	Ephemeral bool     // 是否临时节点

	// 配置中心相关配置
	ConfigDataID string // Kratos 应用的 DataID
	ConfigGroup  string // Kratos 应用的配置分组
}

// Option 是一个用于修改 Options 的函数
type Option func(*Options)

// 默认选项
func defaultOptions() *Options {
	return &Options{
		ClientConfig: &constant.ClientConfig{
			// 默认值
			NamespaceId:         "public", // 默认 public 命名空间
			TimeoutMs:           5000,
			NotLoadCacheAtStart: true,
			LogDir:              "tmp/nacos/log",
			CacheDir:            "tmp/nacos/cache",
			LogLevel:            "info",
		},
		ServerConfigs: make([]constant.ServerConfig, 0),
		GroupName:     constant.DEFAULT_GROUP, // 默认 DEFAULT_GROUP
		Weight:        10.0,                   // 默认权重
		Ephemeral:     true,                   // 默认临时节点
		ConfigGroup:   constant.DEFAULT_GROUP, // 默认配置分组
	}
}

// WithHost 设置 Nacos 服务端地址
// 接收一个或多个 "ip:port" 格式的地址
func WithHost(hosts ...string) Option {
	return func(o *Options) {
		serverConfigs := make([]constant.ServerConfig, 0, len(hosts))
		for _, host := range hosts {
			ip, port, err := SplitHostPort(host)
			if err != nil {
				// 应该 panic 或返回 error
				continue
			}
			serverConfigs = append(serverConfigs, *constant.NewServerConfig(ip, uint64(port)))
		}
		o.ServerConfigs = serverConfigs
	}
}

// WithNamespaceId 设置命名空间 ID
func WithNamespaceId(namespaceId string) Option {
	return func(o *Options) {
		o.ClientConfig.NamespaceId = namespaceId
	}
}

// WithRegistryGroup 设置服务注册的分组
func WithRegistryGroup(group string) Option {
	return func(o *Options) {
		o.GroupName = group
	}
}

// WithRegistryClusters 设置服务注册的集群
func WithRegistryClusters(clusters ...string) Option {
	return func(o *Options) {
		o.Clusters = clusters
	}
}

// WithWeight 设置服务实例权重
func WithWeight(weight float64) Option {
	return func(o *Options) {
		o.Weight = weight
	}
}

// WithConfigDataID 设置配置中心的 DataID
func WithConfigDataID(dataID string) Option {
	return func(o *Options) {
		o.ConfigDataID = dataID
	}
}

// WithConfigGroup 设置配置中心的分组
func WithConfigGroup(group string) Option {
	return func(o *Options) {
		o.ConfigGroup = group
	}
}

// WithUsername 设置用户名
func WithUsername(username string) Option {
	return func(o *Options) {
		o.ClientConfig.Username = username
	}
}

// WithPassword 设置密码
func WithPassword(password string) Option {
	return func(o *Options) {
		o.ClientConfig.Password = password
	}
}

// WithLogLevel 设置日志级别
func WithLogLevel(logLevel string) Option {
	return func(o *Options) {
		o.ClientConfig.LogLevel = logLevel
	}
}

// WithLogDir 设置日志目录
func WithLogDir(logDir string) Option {
	return func(o *Options) {
		o.ClientConfig.LogDir = logDir
	}
}

// WithCacheDir 设置缓存目录
func WithCacheDir(cacheDir string) Option {
	return func(o *Options) {
		o.ClientConfig.CacheDir = cacheDir
	}
}

// WithLogger 设置自定义日志器
func WithLogger(logger interface{}) Option {
	return func(o *Options) {
		// 这里可以设置自定义日志器，具体实现根据需要
		// o.ClientConfig.CustomLogger = logger
	}
}

// 解析 "ip:port"
func SplitHostPort(host string) (string, int, error) {
	parts := strings.Split(host, ":")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid host format: %s", host)
	}
	port, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", 0, err
	}
	return parts[0], port, nil
}
