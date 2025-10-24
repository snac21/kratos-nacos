package nacos

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// 确保 Client 实现了 Kratos 接口
var _ registry.Registrar = (*Client)(nil)
var _ registry.Discovery = (*Client)(nil)

const (
	// 定义 metadata key 用于 Kratos 协议
	// (Kratos v2.7+ 默认会注入 "protocol" key)
	kratosProtocolKey = "protocol"

	// 支持的协议类型
	protocolHTTP = "http"
	protocolGRPC = "grpc"

	// 默认集群名
	defaultClusterName = "DEFAULT"

	// Watcher 事件缓冲区大小
	watcherBufferSize = 10
)

// Register 注册服务实例 (支持多 Endpoints)
func (c *Client) Register(ctx context.Context, service *registry.ServiceInstance) error {
	if service == nil {
		return fmt.Errorf("service instance is nil")
	}
	if len(service.Endpoints) == 0 {
		return fmt.Errorf("service instance endpoints are required")
	}

	// 遍历 Kratos 提供的所有 Endpoints
	for _, endpoint := range service.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil {
			log.Errorf("[kratos-nacos] Failed to parse endpoint: %v. Endpoint: %s", err, endpoint)
			continue // 跳过这个错误的 endpoint
		}

		host := u.Hostname()
		port, _ := strconv.ParseUint(u.Port(), 10, 64)
		if port == 0 {
			log.Warnf("[kratos-nacos] Endpoint missing port: %s", endpoint)
			continue
		}

		// 协议 (http, grpc)
		protocol := strings.ToLower(u.Scheme)

		// 1. 构建 Nacos 服务名 (ServiceName.protocol)
		nacosServiceName := fmt.Sprintf("%s.%s", service.Name, protocol)

		// 2. 准备 Metadata
		// 复制 Kratos 的 metadata，并确保协议信息存在
		metadata := make(map[string]string, len(service.Metadata)+3) // 预分配容量
		for k, v := range service.Metadata {
			metadata[k] = v
		}
		// 确保 protocol 存在于 metadata 中，以便服务发现时使用
		if _, ok := metadata[kratosProtocolKey]; !ok {
			metadata[kratosProtocolKey] = protocol
		}
		// 在 metadata 中存储原始 Kratos 服务名
		metadata["kratos_service_name"] = service.Name
		metadata["kratos_service_version"] = service.Version

		// 3. 准备 Nacos 注册参数
		params := vo.RegisterInstanceParam{
			Ip:          host,
			Port:        port,
			ServiceName: nacosServiceName, // 使用带协议后缀的服务名
			GroupName:   c.opts.GroupName,
			ClusterName: c.getClusterName(metadata), // 优先从 metadata 获取集群
			Weight:      c.opts.Weight,
			Enable:      true,
			Healthy:     true,
			Ephemeral:   c.opts.Ephemeral,
			Metadata:    metadata, // 将 Kratos metadata 透传给 Nacos
		}

		// 4. 调用 Nacos SDK 注册
		_, err = c.namingClient.RegisterInstance(params)
		if err != nil {
			// 注册失败不应阻塞其他 endpoint 注册，但需要返回错误
			log.Errorf("[kratos-nacos] Failed to register instance to nacos: %v. Params: %+v", err, params)
			return fmt.Errorf("failed to register instance in nacos: %w", err)
		}
		log.Infof("[kratos-nacos] Service registered successfully: %s (%s:%d)", nacosServiceName, host, port)
	}

	return nil
}

// Deregister 注销服务实例 (支持多 Endpoints)
func (c *Client) Deregister(ctx context.Context, service *registry.ServiceInstance) error {
	if service == nil {
		return fmt.Errorf("service instance is nil")
	}
	if len(service.Endpoints) == 0 {
		return fmt.Errorf("service instance endpoints are required")
	}

	// 遍历 Kratos 提供的所有 Endpoints
	for _, endpoint := range service.Endpoints {
		u, err := url.Parse(endpoint)
		if err != nil {
			log.Errorf("[kratos-nacos] Failed to parse endpoint for deregister: %v. Endpoint: %s", err, endpoint)
			continue
		}

		host := u.Hostname()
		port, _ := strconv.ParseUint(u.Port(), 10, 64)
		if port == 0 {
			continue
		}

		protocol := strings.ToLower(u.Scheme)
		nacosServiceName := fmt.Sprintf("%s.%s", service.Name, protocol)

		params := vo.DeregisterInstanceParam{
			Ip:          host,
			Port:        port,
			ServiceName: nacosServiceName,
			GroupName:   c.opts.GroupName,
			Ephemeral:   c.opts.Ephemeral,
		}

		_, err = c.namingClient.DeregisterInstance(params)
		if err != nil {
			log.Errorf("[kratos-nacos] Failed to deregister instance from nacos: %v. Params: %+v", err, params)
			// 即使一个失败了，也应尝试注销其他的
		} else {
			log.Infof("[kratos-nacos] Service deregistered successfully: %s (%s:%d)", nacosServiceName, host, port)
		}
	}

	return nil // Kratos 的 Deregister 通常不关心是否所有注销都成功
}

// GetService 获取服务实例列表
// Kratos Discovery 是按 Kratos 服务名查询的。
// 需要同时查询 "serviceName.http" 和 "serviceName.grpc"
func (c *Client) GetService(ctx context.Context, serviceName string) ([]*registry.ServiceInstance, error) {
	if serviceName == "" {
		return nil, fmt.Errorf("service name cannot be empty")
	}

	protocolsToQuery := c.getSupportedProtocols() // 获取支持的协议
	allInstances := make([]*registry.ServiceInstance, 0)
	var lastErr error

	for _, protocol := range protocolsToQuery {
		nacosServiceName := fmt.Sprintf("%s.%s", serviceName, protocol)

		params := vo.SelectInstancesParam{
			ServiceName: nacosServiceName,
			GroupName:   c.opts.GroupName, // 查询时也使用配置的 group
			Clusters:    c.opts.Clusters,
			HealthyOnly: true, // 只返回健康实例
		}

		instances, err := c.namingClient.SelectInstances(params)
		if err != nil {
			// 记录最后一个错误，但不中断查询
			lastErr = err
			log.Warnf("[kratos-nacos] Failed to get service from nacos: %s. Error: %v", nacosServiceName, err)
			continue
		}

		// 转换并合并列表
		kratosInstances := c.nacosInstancesToKratos(instances, serviceName)
		allInstances = append(allInstances, kratosInstances...)
	}

	if len(allInstances) == 0 {
		// 如果 http 和 grpc 都没查到，记录警告但返回空列表
		log.Warnf("[kratos-nacos] No healthy instances found for service: %s (checked .http and .grpc)", serviceName)
		// 如果有错误且没有找到任何实例，可以考虑返回错误
		if lastErr != nil {
			return nil, fmt.Errorf("failed to discover service %s: %w", serviceName, lastErr)
		}
	}

	return allInstances, nil
}

// Watch 创建一个服务观察者
// (这个实现需要适配器，因为 Kratos Watch 是 pull (channel)，Nacos Subscribe 是 push (callback))
func (c *Client) Watch(ctx context.Context, serviceName string) (registry.Watcher, error) {
	// 鉴于 Kratos Client 会按 serviceName (e.g., "serviceName") 来 Watch,
	// Watcher 内部需要同时 Subscribe "serviceName.http" 和 "serviceName.grpc"

	// (实现 Watcher 接口)
	// 注意：newNacosWatcher 需要被修改以支持 Watch 多个 Nacos Service
	watcher, err := newNacosWatcher(ctx, c, serviceName, c.opts.GroupName, c.opts.Clusters...)
	if err != nil {
		return nil, err
	}
	return watcher, nil
}

// nacosInstancesToKratos 辅助函数：转换实例列表
// (需要传入原始 Kratos serviceName，因为 Nacos instance 中存的是带后缀的)
func (c *Client) nacosInstancesToKratos(nacosInstances []model.Instance, kratosSvcName string) []*registry.ServiceInstance {
	kratosInstances := make([]*registry.ServiceInstance, 0, len(nacosInstances))

	for _, ni := range nacosInstances {
		// Kratos 需要 http/grpc 协议头
		scheme, ok := ni.Metadata[kratosProtocolKey]
		if !ok {
			// 如果 metadata 丢失，尝试从服务名后缀猜测
			scheme = c.extractProtocolFromServiceName(ni.ServiceName)
		}

		// 从 metadata 中恢复 version
		version, _ := ni.Metadata["kratos_service_version"]

		kratosInstances = append(kratosInstances, &registry.ServiceInstance{
			ID:       ni.InstanceId,
			Name:     kratosSvcName, // 关键：返回 Kratos 原始服务名!
			Version:  version,
			Metadata: ni.Metadata,
			Endpoints: []string{ // Kratos Client 会根据这个 Endpoint 的 scheme (http/grpc) 来选择
				fmt.Sprintf("%s://%s:%d", scheme, ni.Ip, ni.Port),
			},
		})
	}
	return kratosInstances
}

// getClusterName: 从 metadata 或 options 获取集群名
func (c *Client) getClusterName(metadata map[string]string) string {
	// 允许 Kratos App 启动时通过 Metadata 指定集群
	// (Kratos 某些版本支持设置 Env)
	if cluster, ok := metadata["cluster"]; ok {
		return cluster
	}
	// 否则使用 Nacos Client options 中配置的默认集群
	if len(c.opts.Clusters) > 0 {
		return c.opts.Clusters[0]
	}
	return defaultClusterName // Nacos 默认
}

// extractProtocolFromServiceName 从服务名中提取协议
func (c *Client) extractProtocolFromServiceName(serviceName string) string {
	if strings.HasSuffix(serviceName, "."+protocolHTTP) {
		return protocolHTTP
	} else if strings.HasSuffix(serviceName, "."+protocolGRPC) {
		return protocolGRPC
	}
	return protocolHTTP // 默认协议
}

// getSupportedProtocols 获取支持的协议列表
func (c *Client) getSupportedProtocols() []string {
	return []string{protocolHTTP, protocolGRPC}
}

// NacosWatcher 实现了 Kratos 的 registry.Watcher
type NacosWatcher struct {
	ctx           context.Context
	cancel        context.CancelFunc
	client        *Client
	kratosSvcName string // e.g., "serviceName"
	groupName     string
	clusters      []string

	// Watcher 需要一个 channel 来推送结果
	// Kratos Client (gRPC/HTTP) 会从这个 channel 接收更新
	eventChan chan []*registry.ServiceInstance

	// 要 Watch 多个 Nacos 服务 (http/grpc)，
	// 需要一个内部状态来合并结果
	mu           sync.RWMutex                           // 保护 serviceStore 的并发访问
	serviceStore map[string][]*registry.ServiceInstance // key: nacosServiceName
}

// newNacosWatcher 创建一个新的 Watcher
func newNacosWatcher(ctx context.Context, c *Client, serviceName string, groupName string, clusters ...string) (registry.Watcher, error) {
	wCtx, wCancel := context.WithCancel(ctx)

	watcher := &NacosWatcher{
		ctx:           wCtx,
		cancel:        wCancel,
		client:        c,
		kratosSvcName: serviceName,
		groupName:     groupName,
		clusters:      clusters,
		eventChan:     make(chan []*registry.ServiceInstance, watcherBufferSize), // 缓冲 channel
		serviceStore:  make(map[string][]*registry.ServiceInstance, 2),           // 预分配 http/grpc 两个协议
	}

	// 启动 Nacos Subscribe
	protocolsToWatch := c.getSupportedProtocols()
	for _, protocol := range protocolsToWatch {
		nacosServiceName := fmt.Sprintf("%s.%s", serviceName, protocol)

		err := watcher.client.namingClient.Subscribe(&vo.SubscribeParam{
			ServiceName: nacosServiceName,
			GroupName:   groupName,
			Clusters:    clusters,
			// Nacos 回调函数
			SubscribeCallback: func(services []model.Instance, err error) {
				if err != nil {
					log.Errorf("[kratos-nacos-watcher] Nacos subscribe callback error: %v (Service: %s)", err, nacosServiceName)
					return
				}

				// Nacos 推送了更新
				log.Debugf("[kratos-nacos-watcher] Received update for %s: %d instances", nacosServiceName, len(services))

				// 1. 转换 Nacos 实例为 Kratos 实例
				kratosInstances := c.nacosInstancesToKratos(services, watcher.kratosSvcName)

				// 2. 更新内部状态并合并
				allInstances := watcher.updateAndMerge(nacosServiceName, kratosInstances)

				// 3. 推送到 Kratos channel
				// (非阻塞发送，防止 Kratos Client 卡住导致 Nacos 回调阻塞)
				select {
				case watcher.eventChan <- allInstances:
				default:
					log.Warnf("[kratos-nacos-watcher] Event channel is full, discarding update for %s", watcher.kratosSvcName)
				}
			},
		})

		if err != nil {
			log.Errorf("[kratos-nacos-watcher] Failed to subscribe nacos service: %s. Error: %v", nacosServiceName, err)
			// 如果订阅 http 失败，不应阻止订阅 grpc。
			// 但如果两个都失败，Next() 将永远阻塞。
		}
	}

	return watcher, nil
}

// updateAndMerge 更新服务存储并合并所有协议的实例
// Nacos 的回调是并发的，需要加锁保护 serviceStore
func (w *NacosWatcher) updateAndMerge(nacosServiceName string, instances []*registry.ServiceInstance) []*registry.ServiceInstance {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 更新指定协议的服务实例
	w.serviceStore[nacosServiceName] = instances

	// 合并所有协议 (http + grpc) 的实例
	mergedList := make([]*registry.ServiceInstance, 0)
	for _, list := range w.serviceStore {
		mergedList = append(mergedList, list...)
	}
	return mergedList
}

// Next 实现了 registry.Watcher 接口
func (w *NacosWatcher) Next() ([]*registry.ServiceInstance, error) {
	select {
	case <-w.ctx.Done():
		return nil, w.ctx.Err() // Kratos 停止时或 Watcher stop 时
	case services := <-w.eventChan:
		return services, nil
	}
}

// Stop 实现了 registry.Watcher 接口
func (w *NacosWatcher) Stop() error {
	// 先取消上下文，防止新的事件产生
	w.cancel()

	// 停止 Nacos 订阅
	var lastErr error
	protocolsToWatch := w.client.getSupportedProtocols()
	for _, protocol := range protocolsToWatch {
		nacosServiceName := fmt.Sprintf("%s.%s", w.kratosSvcName, protocol)

		err := w.client.namingClient.Unsubscribe(&vo.SubscribeParam{
			ServiceName: nacosServiceName,
			GroupName:   w.groupName,
			Clusters:    w.clusters,
		})
		if err != nil {
			lastErr = err
			log.Errorf("[kratos-nacos-watcher] Failed to unsubscribe nacos service: %s. Error: %v", nacosServiceName, err)
		} else {
			log.Debugf("[kratos-nacos-watcher] Successfully unsubscribed from nacos service: %s", nacosServiceName)
		}
	}

	// 清理资源
	w.mu.Lock()
	w.serviceStore = nil // 清空存储
	w.mu.Unlock()

	// 关闭事件通道
	close(w.eventChan)

	return lastErr
}
