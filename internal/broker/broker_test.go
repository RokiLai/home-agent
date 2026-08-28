package broker

import (
	"sync"
	"testing"
	"time"
)

func TestBrokerSubscribePublish(t *testing.T) {
	b := New()
	ch1, unsub1 := b.Subscribe("dev-1")
	defer unsub1()

	if !b.IsConnected("dev-1") {
		t.Fatal("expected dev-1 to be connected")
	}
	if b.IsConnected("dev-2") {
		t.Fatal("expected dev-2 not connected")
	}

	ev := Event{Type: "key_sync", Data: `{"version": 1}`}
	n := b.Publish("dev-1", ev)
	if n != 1 {
		t.Fatalf("expected 1 delivered, got %d", n)
	}

	select {
	case received := <-ch1:
		if received.Type != "key_sync" || received.Data != `{"version": 1}` {
			t.Fatalf("unexpected event: %+v", received)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}

	unsub1()
	if b.IsConnected("dev-1") {
		t.Fatal("expected dev-1 disconnected after unsub")
	}
}

func TestBrokerBroadcast(t *testing.T) {
	b := New()
	ch1, unsub1 := b.Subscribe("dev-1")
	defer unsub1()
	ch2, unsub2 := b.Subscribe("dev-2")
	defer unsub2()

	ev := Event{Type: "ping", Data: `{"timestamp": 123}`}
	n := b.Broadcast(ev)
	if n != 2 {
		t.Fatalf("expected broadcast to 2 clients, got %d", n)
	}

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case r := <-ch:
			if r.Type != "ping" {
				t.Fatalf("unexpected event: %+v", r)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("timeout waiting for broadcast event")
		}
	}

	active := b.ActiveDevices()
	if len(active) != 2 || active[0] != "dev-1" || active[1] != "dev-2" {
		t.Fatalf("unexpected active devices: %v", active)
	}
}

func TestBrokerConcurrentAccess(t *testing.T) {
	b := New()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			devID := "dev"
			ch, unsub := b.Subscribe(devID)
			time.Sleep(10 * time.Millisecond)
			b.Publish(devID, Event{Type: "test", Data: "hello"})
			unsub()
			for range ch {
			}
		}(i)
	}
	wg.Wait()
}
