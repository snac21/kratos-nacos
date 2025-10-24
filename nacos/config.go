package nacos

import (
	"context"
	"fmt"
	"log"

	"github.com/go-kratos/kratos/v2/config"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// ConfigSource 包装 Client 以实现 config.Source 接口
type ConfigSource struct {
	client *Client
}

// NewConfigSource 创建配置源
func NewConfigSource(client *Client) *ConfigSource {
	return &ConfigSource{client: client}
}

// 确保 ConfigSource 实现了 Kratos 接口
var _ config.Source = (*ConfigSource)(nil)

// Load 加载配置
func (cs *ConfigSource) Load() ([]*config.KeyValue, error) {
	if cs.client.opts.ConfigDataID == "" {
		return nil, fmt.Errorf("nacos config DataID is required")
	}

	// 1. 从 Nacos 获取配置内容
	content, err := cs.client.configClient.GetConfig(vo.ConfigParam{
		DataId: cs.client.opts.ConfigDataID,
		Group:  cs.client.opts.ConfigGroup,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get config from nacos: %w", err)
	}

	// 2. Kratos Config 需要 KeyValue 键值对
	// 对于 Nacos，DataID 对应一个完整的配置文件
	// Key 可以 DataID，Value 是文件内容
	// Kratos 的 Config 组件会用 YAML 解码器 (codec) 来解析这个 Value。
	kv := &config.KeyValue{
		Key:    cs.client.opts.ConfigDataID,
		Value:  []byte(content),
		Format: "yaml", // 明确指定格式为 yaml
	}
	return []*config.KeyValue{kv}, nil
}

// Watch 监控配置变更
func (cs *ConfigSource) Watch() (config.Watcher, error) {
	// Nacos SDK 提供了 ListenConfig 方法，
	// 同样需要一个适配器将其转换为 Kratos 的 Watcher (channel)
	watcher, err := newNacosConfigWatcher(cs.client, cs.client.opts.ConfigDataID, cs.client.opts.ConfigGroup)
	if err != nil {
		return nil, err
	}
	return watcher, nil
}

// NacosConfigWatcher
type nacosConfigWatcher struct {
	client *Client
	dataID string
	group  string

	// Nacos 的 ListenConfig 是异步回调，
	// 我们需要一个 channel 来通知 Kratos Config
	events chan []*config.KeyValue
	ctx    context.Context
	cancel context.CancelFunc
}

func newNacosConfigWatcher(c *Client, dataID, group string) (config.Watcher, error) {
	ctx, cancel := context.WithCancel(context.Background())

	w := &nacosConfigWatcher{
		client: c,
		dataID: dataID,
		group:  group,
		events: make(chan []*config.KeyValue, 1), // 使用带缓冲的 channel
		ctx:    ctx,
		cancel: cancel,
	}

	// 启动 Nacos 监听
	err := c.configClient.ListenConfig(vo.ConfigParam{
		DataId: dataID,
		Group:  group,
		OnChange: func(namespace, group, dataId, data string) {
			// 配置发生变更时的回调
			log.Printf("[kratos-nacos] Config changed: %s", dataId)
			kv := &config.KeyValue{
				Key:    dataId,
				Value:  []byte(data),
				Format: "yaml",
			}

			// 发送事件
			// 使用非阻塞发送，防止 Kratos 未及时消费导致 Nacos 回调卡死
			select {
			case w.events <- []*config.KeyValue{kv}:
			default:
				log.Println("[kratos-nacos] Config event channel is full, discarding change event.")
			}
		},
	})

	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to listen nacos config: %w", err)
	}

	// (在 Kratos v2.7+ 中，Kratos 会在启动时先 Load() 一次，
	// 早期版本可能需要在这里手动推送一次初始配置)
	return w, nil
}

// Next 阻塞等待下一次配置变更
func (w *nacosConfigWatcher) Next() ([]*config.KeyValue, error) {
	select {
	case kvs := <-w.events:
		return kvs, nil
	case <-w.ctx.Done(): // 确保 Kratos 停止时 Watcher 也停止
		return nil, context.Canceled
	}
}

// Stop 停止 Watcher
func (w *nacosConfigWatcher) Stop() error {
	w.cancel() // 触发 Next() 中的 context.Canceled

	// 停止 Nacos 监听
	return w.client.configClient.CancelListenConfig(vo.ConfigParam{
		DataId: w.dataID,
		Group:  w.group,
	})
}
