import test from 'node:test';
import assert from 'node:assert/strict';

function element() {
  return { textContent: '', hidden: false, disabled: false, href: '', className: '', title: '' };
}

test('version status renders independent server and agent channels without false-current fallback', async () => {
  const elements = new Map([
    'serverCurrentVersion', 'serverLatestVersion', 'serverVersionState', 'serverVersionCheckedAt',
    'serverUpgradeBtn', 'serverReleaseLink', 'agentLatestVersion', 'agentVersionState',
    'agentVersionCheckedAt', 'agentUpdateSummary', 'versionRefreshBtn'
  ].map(id => [id, element()]));
  globalThis.window = { location: { origin: 'http://homeagent.test', hash: '' }, addEventListener() {} };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = { getElementById(id) { return elements.get(id) || null; } };

  const { renderVersionStatus } = await import('../static/js/settings.js');
  renderVersionStatus({
    server: { status: 'available', current_version: 'v0.6.14', latest_version: 'v0.6.15', update_state: 'update_available', upgrade_supported: true, checked_at: '2026-09-11T04:00:00Z', release_url: 'https://example/server' },
    agent: { status: 'error', error_code: 'github_unavailable', error_message: 'timeout', device_summary: { update_available: 2, offline: 1 } }
  });

  assert.equal(elements.get('serverCurrentVersion').textContent, 'v0.6.14');
  assert.equal(elements.get('serverLatestVersion').textContent, 'v0.6.15');
  assert.equal(elements.get('serverUpgradeBtn').disabled, false);
  assert.match(elements.get('agentVersionState').textContent, /检查失败/);
  assert.equal(elements.get('agentLatestVersion').textContent, '未知');
  assert.match(elements.get('agentUpdateSummary').textContent, /可升级 2/);
});

test('server upgrade monitor survives restart gap and converges by operation id', async () => {
	const { monitorServerUpgradeOperation } = await import('../static/js/settings.js');
	const states = [new Error('restart gap'), { status: 'restarting' }, { status: 'succeeded' }];
	const result = await monitorServerUpgradeOperation('srv-up-1', {
		fetchOperation: async () => {
			const next = states.shift();
			if (next instanceof Error) throw next;
			return next;
		},
		sleep: async () => {},
		timeoutMs: 1000
	});
	assert.equal(result.status, 'succeeded');
});
