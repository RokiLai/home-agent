import test from 'node:test';
import assert from 'node:assert/strict';
globalThis.window = { location: { origin: 'http://homeagent.test' } };
globalThis.localStorage = { getItem() { return null; } };
const nodes = new Map();
const node = (extra = {}) => ({ innerHTML: '', innerText: '', value: '', disabled: false, classList: { add() {}, remove() {} }, setAttribute() {}, removeAttribute() {}, querySelectorAll() { return []; }, ...extra });
for (const id of ['deviceDetailContainer', 'deviceDetailTitle', 'deviceDetailBadge', 'deviceContainer', 'deviceSearchInput', 'dashboardDeviceSummary', 'sshPortInput', 'sshPortFeedback', 'sshPortSave', 'sshPortRestore']) nodes.set(id, node());
globalThis.document = { getElementById(id) { return nodes.get(id) || null; }, querySelectorAll() { return []; } };
const { state } = await import('../static/js/state.js');
const { renderDeviceDetailView } = await import('../static/js/devices/render.js');
const { saveSSHPort, fetchDevices } = await import('../static/js/devices/actions.js');
// Fixture follows GET/PATCH responses verified by TestSSHPortAPISettingsAndFacts.
// Node DOM objects do not model layout; the real API/browser test below provides layout evidence.
const device = { id: 'port-device', hostname: 'host', ssh_user: 'admin', ssh_port: 22, ssh_port_reported: 22, ssh_port_override: 0, can_edit_ssh_port: true, addresses: ['192.0.2.10'], os: 'linux' };
function reset() {
  state.devices = [{ ...device }]; state.currentPage = 'deviceDetail'; state.currentDetailDeviceId = device.id; state.currentDetailSection = 'settings';
  nodes.get('sshPortInput').value = '2222'; nodes.get('sshPortFeedback').innerText = '';
}
test('settings expose source and port controls only with device permission', () => {
  reset(); renderDeviceDetailView(device.id, 'settings');
  const html = nodes.get('deviceDetailContainer').innerHTML;
  assert.match(html, /sshPortInput/); assert.match(html, /onclick="saveSSHPort\(this\.dataset\.deviceId\)"/); assert.match(html, /客户端上报/); assert.match(html, /sshPortRestore[^>]*disabled/);
  state.devices[0].can_edit_ssh_port = false; renderDeviceDetailView(device.id, 'settings');
  assert.doesNotMatch(nodes.get('deviceDetailContainer').innerHTML, /id="sshPortInput"/);
});
test('PATCH response updates shared state and both SSH commands', async () => {
  reset(); let calls = 0;
  globalThis.fetch = async (url, options) => { calls++; assert.match(url, /port-device$/); assert.equal(options.method, 'PATCH'); assert.deepEqual(JSON.parse(options.body), { ssh_port_override: 2222 }); return { ok: true, status: 200, json: async () => ({ ...device, ssh_port: 2222, ssh_port_override: 2222 }) }; };
  await saveSSHPort(device.id);
  assert.equal(calls, 1); assert.equal(state.devices[0].ssh_port, 2222);
  assert.match(nodes.get('deviceContainer').innerHTML, /ssh -p 2222 admin@192\.0\.2\.10/);
  renderDeviceDetailView(device.id, 'ssh'); assert.match(nodes.get('deviceDetailContainer').innerHTML, /ssh -p 2222 admin@192\.0\.2\.10/);
});
test('invalid input, denied PATCH and network failures retain state and input', async () => {
  reset(); let calls = 0;
  globalThis.fetch = async () => { calls++; return { ok: false, status: 403 }; };
  nodes.get('sshPortInput').value = '1.5'; await saveSSHPort(device.id); assert.equal(calls, 0);
  nodes.get('sshPortInput').value = '2222'; await saveSSHPort(device.id); assert.equal(calls, 1);
  assert.equal(state.devices[0].ssh_port, 22); assert.equal(nodes.get('sshPortInput').value, '2222'); assert.match(nodes.get('sshPortFeedback').innerText, /403/);
  globalThis.fetch = async () => { throw new Error('offline'); }; await saveSSHPort(device.id);
  assert.match(nodes.get('sshPortFeedback').innerText, /offline/); assert.equal(nodes.get('sshPortSave').disabled, false);
});
test('mismatched response cannot produce a success notification', async () => {
  reset(); globalThis.fetch = async () => ({ ok: true, status: 200, json: async () => ({ ...device, id: 'other' }) });
  await saveSSHPort(device.id); assert.equal(state.devices[0].ssh_port, 22); assert.match(nodes.get('sshPortFeedback').innerText, /保存失败/);
});
test('restore uses latest report and stale list responses cannot undo a save', async () => {
  reset(); let releaseList;
  globalThis.fetch = async (url, options = {}) => {
    if (options.method === 'PATCH') return { ok: true, status: 200, json: async () => ({ ...device, ssh_port: 2222, ssh_port_override: 2222 }) };
    return await new Promise(resolve => { releaseList = resolve; });
  };
  const list = fetchDevices(); await saveSSHPort(device.id);
  releaseList({ ok: true, status: 200, json: async () => ({ devices: [{ ...device }] }) }); await list;
  assert.equal(state.devices[0].ssh_port, 2222);
  globalThis.fetch = async (url, options) => { assert.deepEqual(JSON.parse(options.body), { ssh_port_override: 0 }); return { ok: true, status: 200, json: async () => ({ ...device, ssh_port: 2200, ssh_port_reported: 2200 }) }; };
  await saveSSHPort(device.id, true); assert.equal(state.devices[0].ssh_port, 2200); assert.equal(state.devices[0].ssh_port_override, 0);
});
test('in-flight duplicate clicks and device navigation cannot change another page', async () => {
  reset(); let release; let calls = 0;
  globalThis.fetch = async () => { calls++; return await new Promise(resolve => { release = resolve; }); };
  const first = saveSSHPort(device.id); await saveSSHPort(device.id); assert.equal(calls, 1);
  state.currentDetailDeviceId = 'other'; nodes.get('sshPortFeedback').innerText = 'other-page';
  release({ ok: true, status: 200, json: async () => ({ ...device, ssh_port: 2222, ssh_port_override: 2222 }) }); await first;
  assert.equal(nodes.get('sshPortFeedback').innerText, 'other-page'); assert.equal(state.devices[0].ssh_port, 2222);
});

if (process.env.TEST_SSH_PORT_API_URL) {
  test('real API → browser state → layout → save → clipboard → restore', { timeout: 60000 }, async () => {
    const { spawn } = await import('node:child_process');
    const fs = await import('node:fs');
    const os = await import('node:os');
    const path = await import('node:path');
    const base = process.env.TEST_SSH_PORT_API_URL;
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'homeagent-ssh-browser-'));
    const binary = process.env.CHROME_BIN || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : '/usr/bin/google-chrome');
    const child = spawn(binary, ['--headless=new', '--no-sandbox', '--remote-debugging-port=0', `--user-data-dir=${dir}`, '--no-first-run', 'about:blank'], { detached: process.platform !== 'win32', stdio: ['ignore', 'ignore', 'pipe'] });
    let socket;
    try {
      const endpoint = await new Promise((resolve, reject) => {
        let log = ''; const fail = error => { clearTimeout(timer); reject(error); };
        const timer = setTimeout(() => fail(new Error(`Chrome launch timeout: ${log}`)), 15000);
        child.once('error', fail);
        child.once('exit', (code, signal) => fail(new Error(`Chrome exited (${code}, ${signal}): ${log}`)));
        child.stderr.on('data', chunk => { log += chunk.toString(); const m = log.match(/DevTools listening on (ws:\/\/\S+)/); if (m) { clearTimeout(timer); resolve(m[1]); } });
      });
      socket = new WebSocket(endpoint); await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
      let sequence = 0; const pending = new Map(); const errors = [];
      socket.onmessage = event => { const message = JSON.parse(event.data); if (message.id) { const p = pending.get(message.id); if (p) { pending.delete(message.id); message.error ? p.reject(new Error(message.error.message)) : p.resolve(message.result); } } else if (message.method === 'Runtime.exceptionThrown') errors.push(message.params.exceptionDetails.exception?.description || JSON.stringify(message.params.exceptionDetails)); else if (message.method === 'Runtime.consoleAPICalled' && message.params.type === 'error') errors.push(message.params.args.map(a => a.value || a.description).join(' ')); };
      const send = (method, params = {}, sessionId) => new Promise((resolve, reject) => { const id = ++sequence; pending.set(id, { resolve, reject }); socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) })); });
      const target = await send('Target.createTarget', { url: 'about:blank' });
      const { sessionId } = await send('Target.attachToTarget', { targetId: target.targetId, flatten: true });
      await send('Runtime.enable', {}, sessionId); await send('Page.enable', {}, sessionId); await send('Network.enable', {}, sessionId);
      await send('Network.setCookie', { name: 'homeagent_session', value: process.env.TEST_SSH_PORT_COOKIE, url: base }, sessionId);
      await send('Browser.grantPermissions', { origin: base, permissions: ['clipboardReadWrite', 'clipboardSanitizedWrite'] });
      const evaluate = async expression => { const result = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true }, sessionId); if (result.exceptionDetails) throw new Error(result.exceptionDetails.text); return result.result.value; };
      const wait = async expression => { const deadline = Date.now() + 10000; while (Date.now() < deadline) { if (await evaluate(expression)) return; await new Promise(resolve => setTimeout(resolve, 50)); } throw new Error(`Timed out: ${expression}`); };
      await send('Page.navigate', { url: `${base}/#/devices/port-device/settings` }, sessionId);
      await wait('!!document.getElementById("sshPortInput")');
      for (const width of [375, 768, 1440]) {
        await send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: false }, sessionId);
        const geometry = await evaluate(`(() => {
          const input=document.getElementById('sshPortInput'), save=document.getElementById('sshPortSave'), restore=document.getElementById('sshPortRestore');
          input.scrollIntoView({block:'center'}); input.focus();
          const rects=[input,save,restore].map(e=>e.getBoundingClientRect());
          const label=document.querySelector('label[for="sshPortInput"]').getBoundingClientRect();
          const hit=(e)=>{const r=e.getBoundingClientRect();const target=document.elementFromPoint(r.x+r.width/2,r.y+r.height/2);return target===e||e.contains(target)};
          const overlap=(a,b)=>Math.min(a.right,b.right)>Math.max(a.left,b.left)&&Math.min(a.bottom,b.bottom)>Math.max(a.top,b.top);
          return {offenders:[...document.querySelectorAll("body *")].filter(e=>e.getBoundingClientRect().right>innerWidth+1).map(e=>[e.tagName,e.id,e.className,e.getBoundingClientRect().right]).slice(0,15),overflow:document.documentElement.scrollWidth>innerWidth,inside:rects.every(r=>r.left>=0&&r.right<=innerWidth&&r.width>0),hits:[hit(input),hit(save)],overlap:rects.some((a,i)=>rects.slice(i+1).some(b=>overlap(a,b))),alignment:innerWidth<600?label.bottom<=rects[0].top:Math.abs((label.top+label.height/2)-(rects[0].top+rects[0].height/2))<2,focused:document.activeElement===input,outline:getComputedStyle(input).outlineStyle};
        })()`);
        assert.equal(geometry.overflow, false, JSON.stringify({width,...geometry})); assert.equal(geometry.inside, true); assert.deepEqual(geometry.hits, [true, true]); assert.equal(geometry.overlap, false); assert.equal(geometry.alignment, true); assert.equal(geometry.focused, true); assert.notEqual(geometry.outline, 'none');
        await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 }, sessionId);
        await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 }, sessionId);
        assert.equal(await evaluate(`document.activeElement.id`), 'sshPortSave');
        assert.notEqual(await evaluate(`getComputedStyle(document.activeElement).outlineStyle`), 'none');
        const shot = await send('Page.captureScreenshot', { format: 'png' }, sessionId);
        fs.writeFileSync(path.join(os.tmpdir(), `homeagent-ssh-port-${width}.png`), Buffer.from(shot.data, 'base64'));
      }
      await evaluate(`document.getElementById('sshPortInput').value='2222'; document.getElementById('sshPortSave').click()`);
      await wait(`document.getElementById('sshPortEffective').textContent==='2222' && document.getElementById('sshPortFeedback').textContent==='端口已保存'`);
      const persisted = await evaluate(`fetch('/api/v1/devices/port-device').then(r=>r.json())`); assert.equal(persisted.ssh_port, 2222); assert.equal(persisted.ssh_port_reported, 22); assert.equal(persisted.ssh_port_override, 2222);
      await evaluate(`window.location.hash='#/devices'`); await wait(`!!document.querySelector('.btn-ssh-box[data-ssh="ssh -p 2222 admin@192.0.2.10"]')`);
      await evaluate(`navigator.clipboard.writeText('')`); await evaluate(`document.querySelector('.btn-ssh-box').click()`); await wait(`document.getElementById('toastMsg').textContent.includes('SSH')`);
      assert.equal(await evaluate('navigator.clipboard.readText()'), 'ssh -p 2222 admin@192.0.2.10');
      await evaluate(`window.location.hash='#/devices/port-device/ssh'`); await wait(`document.querySelector('#deviceDetailContainer code')?.textContent==='ssh -p 2222 admin@192.0.2.10'`);
      await evaluate(`navigator.clipboard.writeText('')`); await evaluate(`document.querySelector('#deviceDetailContainer .btn-copy').click()`); await wait(`navigator.clipboard.readText().then(t=>t==='ssh -p 2222 admin@192.0.2.10')`); assert.equal(await evaluate('navigator.clipboard.readText()'), 'ssh -p 2222 admin@192.0.2.10');
      await evaluate(`window.location.hash='#/devices/port-device/settings'`); await wait(`!!document.getElementById('sshPortRestore')`);
      await evaluate(`document.getElementById('sshPortRestore').click()`); await wait(`document.getElementById('sshPortEffective').textContent==='22' && document.getElementById('sshPortRestore').disabled`);
      assert.deepEqual(errors, []);
      await send('Browser.close');
    } finally {
      socket?.close();
      if (child.pid && process.platform !== 'win32') {
        try { process.kill(-child.pid, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
      } else child.kill('SIGKILL');
      await new Promise(resolve => { if (child.exitCode !== null || child.signalCode !== null) resolve(); else child.once('close', resolve); });
      fs.rmSync(dir, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 });
    }
  });
}
