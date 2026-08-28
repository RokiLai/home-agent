import test from 'node:test';
import assert from 'node:assert/strict';

test('upgrade-all reports dispatch outcomes and waits for command plus facts convergence', async () => {
  const container = { innerHTML: '' };
  globalThis.window = { location: { origin: 'http://homeagent.test' } };
  globalThis.localStorage = { getItem() { return null; } };
  globalThis.document = {
    getElementById(id) {
      if (id === 'deviceContainer') return container;
      if (id === 'deviceSearchInput') return { value: '' };
      return null;
    },
    querySelectorAll() { return []; }
  };

  const { summarizeUpgradeAllResponse, monitorUpgradeResults } = await import('../static/js/devices/actions.js');
  const { state } = await import('../static/js/state.js');
  const response = {
    target_version: 'v0.6.1',
    dispatched_count: 2,
    skipped_count: 1,
    failed_count: 1,
    device_results: [
      { device_id: 'converged', status: 'dispatched', command_id: 'cmd-ok' },
      { device_id: 'failed', status: 'dispatched', command_id: 'cmd-fail' },
      { device_id: 'offline', status: 'skipped', reason: 'device_offline' },
      { device_id: 'missing', status: 'failed', reason: 'artifact_unavailable' }
    ]
  };

  assert.deepEqual(summarizeUpgradeAllResponse(response), {
    dispatched: 2, skipped: 1, failed: 1, results: response.device_results
  });

  state.upgradingDevices = new Set(['converged', 'failed']);
  const outcomes = await monitorUpgradeResults(response.device_results, response.target_version, {
    refreshDevices: async () => {
      state.devices = [
        { id: 'converged', hostname: 'ok', agent_version: 'v0.6.1', os: 'darwin', arch: 'arm64', addresses: [], connected: true },
        { id: 'failed', hostname: 'bad', agent_version: 'v0.5.4', os: 'windows', arch: 'amd64', addresses: [], connected: true }
      ];
    },
    fetchJSON: async url => url.endsWith('cmd-ok')
      ? { status: 'succeeded' }
      : { status: 'timed_out', error_message: 'command deadline exceeded' },
    sleep: async () => {},
    timeoutMs: 1000
  });

  assert.deepEqual(outcomes, [
    { device_id: 'converged', status: 'converged' },
    { device_id: 'failed', status: 'timed_out', error: 'command deadline exceeded' }
  ]);
  assert.equal(state.upgradingDevices.size, 0);
});
