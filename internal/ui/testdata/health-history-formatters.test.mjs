import test from 'node:test';
import assert from 'node:assert/strict';

globalThis.window = { location: { origin: 'http://homeagent.test' } };
globalThis.localStorage = { getItem() { return null; } };
globalThis.document = {
  getElementById() { return null; },
  querySelectorAll() { return []; }
};

const {
  formatHealthEventType,
  formatHealthReasonCode,
  renderHealthEventsTimeline
} = await import('../static/js/devices/actions.js');

test('formatHealthEventType maps standard event types to Chinese labels and styles', () => {
  const opened = formatHealthEventType('opened');
  assert.equal(opened.label, '异常产生');
  assert.equal(opened.className, 'status-error');

  const resolved = formatHealthEventType('resolved');
  assert.equal(resolved.label, '已恢复');
  assert.equal(resolved.className, 'status-synced');

  const changed = formatHealthEventType('changed');
  assert.equal(changed.label, '状态变更');
  assert.equal(changed.className, 'status-pending');

  // Case insensitivity
  const openedUpper = formatHealthEventType('OPENED');
  assert.equal(openedUpper.label, '异常产生');
  assert.equal(openedUpper.className, 'status-error');

  const resolvedUpper = formatHealthEventType('Resolved');
  assert.equal(resolvedUpper.label, '已恢复');
  assert.equal(resolvedUpper.className, 'status-synced');
});

test('formatHealthEventType handles unknown or empty types with safe fallback', () => {
  const unknownType = formatHealthEventType('unknown_custom_type');
  assert.equal(unknownType.label, 'UNKNOWN_CUSTOM_TYPE');
  assert.equal(unknownType.className, 'status-secondary');

  const emptyType = formatHealthEventType('');
  assert.equal(emptyType.label, 'UNKNOWN');
  assert.equal(emptyType.className, 'status-secondary');

  const nullType = formatHealthEventType(null);
  assert.equal(nullType.label, 'UNKNOWN');
  assert.equal(nullType.className, 'status-secondary');
});

test('formatHealthReasonCode maps all known health evaluator codes to Chinese labels', () => {
  const expectedMappings = {
    device_offline: '设备离线',
    heartbeat_stale: '心跳陈旧',
    device_never_seen: '从未上报',
    agent_version_outdated: '客户端版本过旧',
    agent_version_invalid: '客户端版本非法',
    ssh_sync_failed: 'SSH 密钥同步失败',
    ssh_key_drift: 'SSH 密钥配置漂移',
    ddns_sync_failed: 'DDNS 解析同步失败',
    ddns_no_valid_address: '未检测到有效 IPv6',
    ddns_address_drift: 'DDNS 解析记录漂移',
    ddns_prefix_stale: 'IPv6 网络前缀陈旧',
    upgrade_failed: '自升级执行失败',
    upgrade_not_converged: '自升级未收敛',
    disk_space_low: '磁盘空间不足',
    memory_pressure: '内存压力偏高'
  };

  for (const [code, expectedLabel] of Object.entries(expectedMappings)) {
    const res = formatHealthReasonCode(code);
    assert.equal(res.label, expectedLabel, `code ${code} should map to ${expectedLabel}`);
    assert.equal(res.code, code);
  }
});

test('formatHealthReasonCode falls back safely on unknown or empty codes', () => {
  const custom = formatHealthReasonCode('custom_thirdparty_rule');
  assert.equal(custom.label, 'custom_thirdparty_rule');
  assert.equal(custom.code, 'custom_thirdparty_rule');

  const empty = formatHealthReasonCode('');
  assert.equal(empty.label, '未知原因');
  assert.equal(empty.code, '');

  const nullCode = formatHealthReasonCode(null);
  assert.equal(nullCode.label, '未知原因');
  assert.equal(nullCode.code, '');
});

test('renderHealthEventsTimeline renders empty placeholder when no events are provided', () => {
  const emptyHtml = renderHealthEventsTimeline([]);
  assert.match(emptyHtml, /暂无状态变动历史记录/);

  const nullHtml = renderHealthEventsTimeline(null);
  assert.match(nullHtml, /暂无状态变动历史记录/);
});

test('renderHealthEventsTimeline renders formatted Chinese items with badges, codes, and timestamps', () => {
  const events = [
    {
      id: 'hevt_1',
      type: 'opened',
      reason_code: 'heartbeat_stale',
      occurred_at: '2026-09-13T12:00:00Z'
    },
    {
      id: 'hevt_2',
      type: 'resolved',
      reason_code: 'memory_pressure',
      occurred_at: '2026-09-13T12:05:00Z'
    },
    {
      id: 'hevt_3',
      type: 'changed',
      reason_code: 'disk_space_low',
      occurred_at: '2026-09-13T12:10:00Z'
    }
  ];

  const html = renderHealthEventsTimeline(events);

  // Assertion: Chinese type badges and classes
  assert.match(html, /status-error/);
  assert.match(html, /异常产生/);

  assert.match(html, /status-synced/);
  assert.match(html, /已恢复/);

  assert.match(html, /status-pending/);
  assert.match(html, /状态变更/);

  // Assertion: Chinese reason titles
  assert.match(html, /心跳陈旧/);
  assert.match(html, /内存压力偏高/);
  assert.match(html, /磁盘空间不足/);

  // Assertion: Retained original code tags
  assert.match(html, /\(heartbeat_stale\)/);
  assert.match(html, /\(memory_pressure\)/);
  assert.match(html, /\(disk_space_low\)/);
});

test('renderHealthEventsTimeline safely handles unknown codes and escapes XSS payloads', () => {
  const maliciousEvents = [
    {
      id: 'hevt_xss',
      type: '<script>alert(1)</script>',
      reason_code: 'custom_<img src=x onerror=alert(2)>',
      occurred_at: '2026-09-13T12:00:00Z'
    }
  ];

  const html = renderHealthEventsTimeline(maliciousEvents);

  // Must not inject raw script or img tags
  assert.doesNotMatch(html, /<script>/i);
  assert.doesNotMatch(html, /<img /i);
  assert.match(html, /&lt;script&gt;/i);
  assert.match(html, /&lt;img /i);
});
