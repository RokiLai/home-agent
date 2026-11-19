package routermonitor

import (
	"strings"
	"testing"
	"time"
)

func TestParseEventsOutput(t *testing.T) {
	raw := `generation=gen-123 earliest_seq=1 latest_seq=3
1	1	gen-123	1	10	lan	failed	lan_interface_down			1000
1	1	gen-123	2	10	wan	action_requested	wan_interface_down	act-1	wan	1005
1	1	gen-123	3	11	wan	healthy	ok	act-1	wan	1065
`
	batch, err := ParseEvents(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batch.Generation != "gen-123" || batch.EarliestSeq != 1 || batch.LatestSeq != 3 {
		t.Fatalf("unexpected header: %+v", batch)
	}
	if len(batch.Events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(batch.Events))
	}
	if batch.Events[1].ActionID != "act-1" || batch.Events[1].EventType != "action_requested" {
		t.Fatalf("unexpected event: %+v", batch.Events[1])
	}
}

func TestEventStatusMapping(t *testing.T) {
	// 只有同一动作对应的后续健康复查才能显示「已恢复」
	timeline := []Event{
		{Version: 1, BootID: "b1", Generation: "g1", Seq: 1, Cycle: 10, Component: "wan", EventType: "action_requested", Reason: "wan_down", ActionID: "act-1", RecoveryGroup: "wan", Uptime: 100},
		{Version: 1, BootID: "b1", Generation: "g1", Seq: 2, Cycle: 10, Component: "wan", EventType: "action_success", Reason: "ok", ActionID: "act-1", RecoveryGroup: "wan", Uptime: 102},
		// 同一周期或不带action_id的普通健康检查不能作为恢复证明
		{Version: 1, BootID: "b1", Generation: "g1", Seq: 3, Cycle: 11, Component: "wan", EventType: "recovered", Reason: "ok", ActionID: "act-1", RecoveryGroup: "wan", Uptime: 160},
	}
	items := BuildTimelineItems(timeline)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if items[0].DisplayStatus != "已请求" {
		t.Fatalf("expected 已请求, got %s", items[0].DisplayStatus)
	}
	if items[1].DisplayStatus != "待复查" {
		t.Fatalf("expected 待复查, got %s", items[1].DisplayStatus)
	}
	if items[2].DisplayStatus != "已恢复" {
		t.Fatalf("expected 已恢复, got %s", items[2].DisplayStatus)
	}
}

func TestUnmatchedRecheckCannotRecover(t *testing.T) {
	timeline := []Event{
		{Version: 1, BootID: "b1", Generation: "g1", Seq: 1, Cycle: 10, Component: "wan", EventType: "action_requested", Reason: "wan_down", ActionID: "act-1", RecoveryGroup: "wan", Uptime: 100},
		{Version: 1, BootID: "b1", Generation: "g1", Seq: 2, Cycle: 10, Component: "wan", EventType: "action_success", Reason: "ok", ActionID: "act-1", RecoveryGroup: "wan", Uptime: 102},
		// 不同action_id或非复查健康的事件不能显示已恢复
		{Version: 1, BootID: "b1", Generation: "g1", Seq: 3, Cycle: 11, Component: "wan", EventType: "recovered", Reason: "ok", ActionID: "act-other", RecoveryGroup: "wan", Uptime: 160},
	}
	items := BuildTimelineItems(timeline)
	if items[2].DisplayStatus == "已恢复" {
		t.Fatalf("unmatched action cannot be marked as 已恢复")
	}
}

func TestEventStorePersistenceAndLimits(t *testing.T) {
	dir := t.TempDir()
	store := NewEventStore(dir, "dev1", "router1")

	// 1. 保存第一批事件
	batch1 := EventBatch{
		BootID:      "b1",
		Generation:  "g1",
		EarliestSeq: 1,
		LatestSeq:   2,
		Events: []Event{
			{Version: 1, BootID: "b1", Generation: "g1", Seq: 1, Cycle: 1, Component: "lan", EventType: "observed", Reason: "ok", Uptime: 10},
			{Version: 1, BootID: "b1", Generation: "g1", Seq: 2, Cycle: 1, Component: "wan", EventType: "action_requested", ActionID: "a1", Reason: "down", Uptime: 12},
		},
	}
	ack, err := store.SaveEvents(batch1, time.Now())
	if err != nil {
		t.Fatalf("unexpected save error: %v", err)
	}
	if ack != 2 {
		t.Fatalf("expected ack=2, got %d", ack)
	}

	// 2. 幂等重复提交：返回已提交进度
	ack, err = store.SaveEvents(batch1, time.Now())
	if err != nil {
		t.Fatalf("unexpected save error on duplicate: %v", err)
	}
	if ack != 2 {
		t.Fatalf("expected ack=2 on duplicate, got %d", ack)
	}

	// 3. 序号跳号且未声明 Gap 时拒绝
	badBatch := EventBatch{
		BootID:      "b1",
		Generation:  "g1",
		EarliestSeq: 1, // EarliestSeq 为 1 说明源端缓冲并没有从 4 开始，但事件列表却从 4 开始，存在未声明的缺口
		LatestSeq:   4,
		Events: []Event{
			{Version: 1, BootID: "b1", Generation: "g1", Seq: 4, Cycle: 2, Component: "wan", EventType: "recovered", ActionID: "a1", Reason: "ok", Uptime: 70},
		},
	}
	_, err = store.SaveEvents(badBatch, time.Now())
	if err == nil {
		t.Fatal("expected error on undeclared gap, got nil")
	}

	// 4. 读取查询与重载恢复
	store2 := NewEventStore(dir, "dev1", "router1")
	events, err := store2.QueryEvents(10)
	if err != nil {
		t.Fatalf("query error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func BenchmarkEventStoreWrite(b *testing.B) {
	dir := b.TempDir()
	store := NewEventStore(dir, "dev1", "router1")
	now := time.Now()

	// 预置接近 10,000 条事件以验证性能
	initialEvents := make([]Event, 9900)
	for i := 0; i < 9900; i++ {
		initialEvents[i] = Event{
			Version:    1,
			BootID:     "b1",
			Generation: "g1",
			Seq:        int64(i + 1),
			Cycle:      int64(i / 10),
			Component:  "wan",
			EventType:  "observed",
			Reason:     "ok",
			Uptime:     int64(i * 10),
			CreatedAt:  now,
		}
	}
	_, _ = store.SaveEvents(EventBatch{
		BootID:      "b1",
		Generation:  "g1",
		EarliestSeq: 1,
		LatestSeq:   9900,
		Events:      initialEvents,
	}, now)

	b.ResetTimer()
	seq := int64(9901)
	for i := 0; i < b.N; i++ {
		batch := make([]Event, 64)
		for j := 0; j < 64; j++ {
			batch[j] = Event{
				Version:    1,
				BootID:     "b1",
				Generation: "g1",
				Seq:        seq,
				Cycle:      seq / 10,
				Component:  "wan",
				EventType:  "observed",
				Reason:     "ok",
				Uptime:     seq * 10,
			}
			seq++
		}
		_, err := store.SaveEvents(EventBatch{
			BootID:      "b1",
			Generation:  "g1",
			EarliestSeq: batch[0].Seq,
			LatestSeq:   batch[63].Seq,
			Events:      batch,
		}, now)
		if err != nil {
			b.Fatalf("save error: %v", err)
		}
	}
}

func TestEventStoreGapAndCutoffAndErrors(t *testing.T) {
	dir := t.TempDir()
	store := NewEventStore(dir, "dev1", "router1")
	now := time.Now()

	// 1. 声明了最早序号（由于满缓冲丢弃旧序号）时的 Gap 记录
	batch := EventBatch{
		BootID:      "b1",
		Generation:  "g1",
		EarliestSeq: 10,
		LatestSeq:   10,
		Events: []Event{
			{Version: 1, BootID: "b1", Generation: "g1", Seq: 10, Cycle: 5, Component: "wan", EventType: "action_requested", ActionID: "a1", Reason: "down", Uptime: 100},
		},
	}
	ack, err := store.SaveEvents(batch, now)
	if err != nil {
		t.Fatalf("unexpected save error with gap: %v", err)
	}
	if ack != 10 {
		t.Fatalf("expected ack=10, got %d", ack)
	}

	// 2. 检查 QueryEvents 与 GetAckCursor
	boot, gen, cur, err := store.GetAckCursor()
	if err != nil || boot != "b1" || gen != "g1" || cur != 10 {
		t.Fatalf("unexpected cursor: %s %s %d %v", boot, gen, cur, err)
	}

	evs, err := store.QueryEvents(2)
	if err != nil || len(evs) != 2 {
		t.Fatalf("expected 2 events (gap + seq 10), got %d %v", len(evs), err)
	}
	if evs[0].EventType != "gap" {
		t.Fatalf("expected gap event, got %s", evs[0].EventType)
	}

	// 3. 各种分支覆盖测试
	allTimeline := BuildTimelineItems([]Event{
		{EventType: "observed"},
		{EventType: "action_restricted", Reason: "cooling"},
		{EventType: "action_blocked", Reason: "precheck"},
		{EventType: "action_requested", ActionID: "a1", Component: "c1"},
		{EventType: "action_success"},
		{EventType: "action_failed", Reason: "err", ActionID: "a2"},
		{EventType: "unhealthy_recheck", ActionID: "a3"},
		{EventType: "gap"},
		{EventType: "unknown_type"},
	})
	if len(allTimeline) != 9 {
		t.Fatalf("expected 9 timeline items, got %d", len(allTimeline))
	}
}
