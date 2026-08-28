// Package broker 提供服务端与客户端 Agent 之间基于 Server-Sent Events (SSE) 的实时事件分发中枢。
package broker

import (
	"sort"
	"sync"
	"time"
)

// Event 表示通过 SSE 通道推送给 Agent 的事件消息包。
type Event struct {
	Type      string `json:"type"`
	Data      string `json:"data"`
	ID        string `json:"id,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// Broker 维护所有在线 Agent 的 SSE 连接通道，支持精准单播推送与全网广播。
type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan Event]struct{}
}

// New 创建并初始化事件分发 Broker 实例。
func New() *Broker {
	return &Broker{
		subscribers: make(map[string]map[chan Event]struct{}),
	}
}

// Subscribe 注册指定设备 ID 的 SSE 事件接收通道，返回订阅通道及清理注销函数。
func (b *Broker) Subscribe(deviceID string) (<-chan Event, func()) {

	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan Event, 16)
	if b.subscribers[deviceID] == nil {
		b.subscribers[deviceID] = make(map[chan Event]struct{})
	}
	b.subscribers[deviceID][ch] = struct{}{}

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()

			if subs, ok := b.subscribers[deviceID]; ok {
				delete(subs, ch)
				close(ch)
				if len(subs) == 0 {
					delete(b.subscribers, deviceID)
				}
			}
		})
	}

	return ch, unsubscribe
}

// Publish 向指定设备的所有活跃连接推送单播事件。
// 返回成功加入队列的订阅者数量。
func (b *Broker) Publish(deviceID string, event Event) int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if event.Timestamp == 0 {
		event.Timestamp = time.Now().Unix()
	}

	subs, ok := b.subscribers[deviceID]
	if !ok || len(subs) == 0 {
		return 0
	}

	count := 0
	for ch := range subs {
		select {
		case ch <- event:
			count++
		default:
			// 缓冲区已满，非阻塞操作以防止慢消费者阻塞 Broker
		}
	}
	return count
}

// Broadcast 向所有已连接的在线设备广播事件。
// 返回成功加入队列的订阅者总数。
func (b *Broker) Broadcast(event Event) int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if event.Timestamp == 0 {
		event.Timestamp = time.Now().Unix()
	}

	count := 0
	for _, subs := range b.subscribers {
		for ch := range subs {
			select {
			case ch <- event:
				count++
			default:
			}
		}
	}
	return count
}

// ActiveDevices 返回当前有活动 SSE 订阅的所有设备 ID 列表（已排序）。
func (b *Broker) ActiveDevices() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()

	devices := make([]string, 0, len(b.subscribers))
	for devID, subs := range b.subscribers {
		if len(subs) > 0 {
			devices = append(devices, devID)
		}
	}
	sort.Strings(devices)
	return devices
}

// IsConnected 判断指定设备当前是否保持至少一个活跃的 SSE 连接。
func (b *Broker) IsConnected(deviceID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	subs, ok := b.subscribers[deviceID]
	return ok && len(subs) > 0
}

// SubscribersCount 返回指定设备当前的活跃订阅连接数量。
func (b *Broker) SubscribersCount(deviceID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	subs, ok := b.subscribers[deviceID]
	if !ok {
		return 0
	}
	return len(subs)
}

// CloseClient 强制关闭指定设备的所有活跃 SSE 订阅通道（设备删除或授权注销时调用）。
func (b *Broker) CloseClient(deviceID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if subs, ok := b.subscribers[deviceID]; ok {
		for ch := range subs {
			close(ch)
		}
		delete(b.subscribers, deviceID)
	}
}

