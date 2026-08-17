package broker

import (
	"sort"
	"sync"
	"time"
)

type Event struct {
	Type      string `json:"type"`
	Data      string `json:"data"`
	ID        string `json:"id,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

type Broker struct {
	mu          sync.RWMutex
	subscribers map[string]map[chan Event]struct{}
}

func New() *Broker {
	return &Broker{
		subscribers: make(map[string]map[chan Event]struct{}),
	}
}

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
			// Buffer full, drop or non-blocking to prevent slow clients blocking broker
		}
	}
	return count
}

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

func (b *Broker) IsConnected(deviceID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()

	subs, ok := b.subscribers[deviceID]
	return ok && len(subs) > 0
}

func (b *Broker) SubscribersCount(deviceID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	subs, ok := b.subscribers[deviceID]
	if !ok {
		return 0
	}
	return len(subs)
}
