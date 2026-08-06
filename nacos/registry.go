package nacos

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"log/slog"

	"github.com/go-kratos/kratos/v3/registry"
	"github.com/nacos-group/nacos-sdk-go/v2/model"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// 确保 slog 被保留及 Client 实现了 Kratos 接口
var _ *slog.Logger
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
			c.Logger().Error("Failed to parse endpoint", "err", err, "endpoint", endpoint)
			continue // 跳过这个错误的 endpoint
		}

		host := u.Hostname()
		port, _ := strconv.ParseUint(u.Port(), 10, 64)
		if port == 0 {
			c.Logger().Warn("Endpoint missing port", "endpoint", endpoint)
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
		_, err = c.NamingClient.RegisterInstance(params)
		if err != nil {
			// 注册失败不应阻塞其他 endpoint 注册，但需要返回错误
			c.Logger().Error("Failed to register instance to nacos", "err", err, "params", params)
			return fmt.Errorf("failed to register instance in nacos: %w", err)
		}
		c.Logger().Info("Service registered successfully", "service", nacosServiceName, "host", host, "port", port)
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
			c.Logger().Error("Failed to parse endpoint for deregister", "err", err, "endpoint", endpoint)
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

		_, err = c.NamingClient.DeregisterInstance(params)
		if err != nil {
			c.Logger().Error("Failed to deregister instance from nacos", "err", err, "params", params)
			// 即使一个失败了，也应尝试注销其他的
		} else {
			c.Logger().Info("Service deregistered successfully", "service", nacosServiceName, "host", host, "port", port)
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

		instances, err := c.NamingClient.SelectInstances(params)
		if err != nil {
			// 记录最后一个错误，但不中断查询
			lastErr = err
			c.Logger().Warn("Failed to get service from nacos", "service", nacosServiceName, "err", err)
			continue
		}

		// 转换并合并列表
		kratosInstances := c.nacosInstancesToKratos(instances, serviceName)
		allInstances = append(allInstances, kratosInstances...)
	}

	if len(allInstances) == 0 {
		// 如果 http 和 grpc 都没查到，记录警告但返回空列表
		c.Logger().Warn("No healthy instances found for service", "service", serviceName)
		// 如果有错误且没有找到任何实例，可以考虑返回错误
		if lastErr != nil {
			return nil, fmt.Errorf("failed to discover service %s: %w", serviceName, lastErr)
		}
	}

	return allInstances, nil
}

// GetHealthyInstances 直接根据原始服务名，获取健康的实例 Endpoint。
// 使用随机（Random）机制负载均衡返回单个 Endpoint 地址。
// 该方法无需每次请求 Nacos 注册中心服务端，会自动走 SDK 的本地缓存与动态更新机制。
func (c *Client) GetHealthyInstances(ctx context.Context, serviceName string) (string, error) {
	if serviceName == "" {
		return "", fmt.Errorf("service name cannot be empty")
	}
	params := vo.SelectInstancesParam{
		ServiceName: serviceName,
		GroupName:   c.opts.GroupName,
		Clusters:    c.opts.Clusters,
		HealthyOnly: true, // 仅获取健康的实例
	}
	// Nacos SDK 内部会自动维护该 serviceName 的缓存和 UDP 变更订阅
	instances, err := c.NamingClient.SelectInstances(params)
	if err != nil {
		c.Logger().Warn("Failed to get service from nacos", "service", serviceName, "err", err)
		return "", err
	}

	if len(instances) == 0 {
		return "", fmt.Errorf("no healthy instance found for service: %s", serviceName)
	}

	// 随机选择一个实例
	idx := rand.Intn(len(instances))
	ni := instances[idx]

	return fmt.Sprintf("http://%s:%d", ni.Ip, ni.Port), nil
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

type sharedSubscription struct {
	mu            sync.Mutex
	client        *Client
	kratosSvcName string
	groupName     string
	clusters      []string
	refCount      int
	listeners     map[*NacosWatcher]chan []*registry.ServiceInstance
	instances     []*registry.ServiceInstance
	serviceStore  map[string][]*registry.ServiceInstance
	// subscribeParams 保存 Nacos SDK 注册时使用的原始回调。
	// SDK 注销时必须传入同一个回调，不能重新构造空回调。
	subscribeParams map[string]*vo.SubscribeParam
}

// NacosWatcher 逻辑观察者
type NacosWatcher struct {
	ctx           context.Context
	cancel        context.CancelFunc
	client        *Client
	kratosSvcName string
	eventChan     chan []*registry.ServiceInstance
	sub           *sharedSubscription
	stopOnce      sync.Once
	stopErr       error
}

// newNacosWatcher 创建并返回一个 Nacos 逻辑观察者
func newNacosWatcher(ctx context.Context, c *Client, serviceName string, groupName string, clusters ...string) (registry.Watcher, error) {
	wCtx, wCancel := context.WithCancel(ctx)

	watcher := &NacosWatcher{
		ctx:           wCtx,
		cancel:        wCancel,
		client:        c,
		kratosSvcName: serviceName,
		eventChan:     make(chan []*registry.ServiceInstance, watcherBufferSize),
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	sub, ok := c.activeWatch[serviceName]
	if !ok {
		sub = &sharedSubscription{
			client:          c,
			kratosSvcName:   serviceName,
			groupName:       groupName,
			clusters:        clusters,
			listeners:       make(map[*NacosWatcher]chan []*registry.ServiceInstance),
			serviceStore:    make(map[string][]*registry.ServiceInstance, 2),
			subscribeParams: make(map[string]*vo.SubscribeParam, 2),
		}
		c.activeWatch[serviceName] = sub

		// 创建真实的 Nacos 物理订阅。至少一个协议订阅成功，Watcher 才能返回。
		// 否则 Kratos resolver 会被误判为已创建成功，但永远收不到地址更新。
		var subscribeErrs []error
		protocolsToWatch := c.getSupportedProtocols()
		for _, protocol := range protocolsToWatch {
			nacosServiceName := fmt.Sprintf("%s.%s", serviceName, protocol)

			param := &vo.SubscribeParam{
				ServiceName: nacosServiceName,
				GroupName:   groupName,
				Clusters:    clusters,
				SubscribeCallback: func(services []model.Instance, err error) {
					if err != nil {
						c.Logger().Error("Nacos subscribe callback error", "err", err, "service", nacosServiceName)
						return
					}
					ips := make([]string, 0, len(services))
					for _, s := range services {
						ips = append(ips, fmt.Sprintf("%s:%d", s.Ip, s.Port))
					}
					c.Logger().Debug("Received update for service", "service", nacosServiceName, "count", len(services), "instances", strings.Join(ips, ","))
					kratosInstances := c.nacosInstancesToKratos(services, serviceName)
					sub.updateAndMerge(nacosServiceName, kratosInstances)
				},
			}
			err := c.NamingClient.Subscribe(param)
			if err != nil {
				c.Logger().Error("Failed to subscribe nacos service", "service", nacosServiceName, "err", err)
				subscribeErrs = append(subscribeErrs, fmt.Errorf("subscribe %s: %w", nacosServiceName, err))
				continue
			}
			sub.subscribeParams[nacosServiceName] = param
		}

		if len(sub.subscribeParams) == 0 {
			delete(c.activeWatch, serviceName)
			wCancel()
			return nil, fmt.Errorf("failed to subscribe nacos service %q: %w", serviceName, errors.Join(subscribeErrs...))
		}

		// 订阅成功后立即同步拉取一次实例，避免 resolver 重建后只能被动等待
		// Nacos 的下一次推送，从而长时间没有可用地址。
		for nacosServiceName := range sub.subscribeParams {
			instances, err := c.NamingClient.SelectInstances(vo.SelectInstancesParam{
				ServiceName: nacosServiceName,
				GroupName:   groupName,
				Clusters:    clusters,
				HealthyOnly: true,
			})
			if err != nil {
				c.Logger().Warn("Failed to fetch initial instances", "service", nacosServiceName, "err", err)
				continue
			}
			sub.updateAndMerge(nacosServiceName, c.nacosInstancesToKratos(instances, serviceName))
		}
	}

	sub.mu.Lock()
	sub.refCount++
	sub.listeners[watcher] = watcher.eventChan
	// 如果已存在缓存节点快照，立刻非阻塞推送给该客户端通道，以秒级唤醒，避免等待重连超时
	if len(sub.instances) > 0 {
		select {
		case watcher.eventChan <- sub.instances:
		default:
		}
	}
	sub.mu.Unlock()

	watcher.sub = sub
	return watcher, nil
}

// updateAndMerge 更新服务存储并合并所有协议的实例
// Nacos 的回调是并发的，需要加锁保护 serviceStore
func (s *sharedSubscription) updateAndMerge(nacosServiceName string, instances []*registry.ServiceInstance) []*registry.ServiceInstance {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.serviceStore[nacosServiceName] = instances

	mergedList := make([]*registry.ServiceInstance, 0)
	for _, list := range s.serviceStore {
		mergedList = append(mergedList, list...)
	}

	s.instances = mergedList

	for _, ch := range s.listeners {
		select {
		case ch <- mergedList:
		default:
			if s.client != nil {
				s.client.Logger().Warn("Event channel is full, discarding update", "service", s.kratosSvcName)
			}
		}
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

// Stop 实现了 registry.Watcher 接口 (逻辑监听退出)
func (w *NacosWatcher) Stop() error {
	w.stopOnce.Do(func() {
		w.cancel()

		sub := w.sub
		if sub == nil {
			return
		}

		// 持有 client 锁直到物理注销完成，防止新的 Watch 已订阅后被旧 Watch 的
		// Unsubscribe 反向取消。IDLE 重建会在该锁释放后创建干净的新订阅。
		w.client.mu.Lock()
		defer w.client.mu.Unlock()

		sub.mu.Lock()
		delete(sub.listeners, w)
		sub.refCount--
		if sub.refCount > 0 {
			sub.mu.Unlock()
			return
		}
		delete(w.client.activeWatch, w.kratosSvcName)
		params := make([]*vo.SubscribeParam, 0, len(sub.subscribeParams))
		for _, param := range sub.subscribeParams {
			params = append(params, param)
		}
		sub.mu.Unlock()

		for _, param := range params {
			if err := w.client.NamingClient.Unsubscribe(param); err != nil {
				w.stopErr = errors.Join(w.stopErr, fmt.Errorf("unsubscribe %s: %w", param.ServiceName, err))
				w.client.Logger().Error("Failed to unsubscribe nacos service", "service", param.ServiceName, "err", err)
				continue
			}
			w.client.Logger().Debug("Successfully unsubscribed from nacos service", "service", param.ServiceName)
		}
	})
	return w.stopErr
}
