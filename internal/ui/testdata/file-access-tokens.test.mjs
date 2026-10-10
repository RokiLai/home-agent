import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import cp from 'node:child_process';

test('file token management uses real HTTP responses, native dialog focus, geometry and click callbacks', async () => {
  const base = process.env.TEST_FILE_TOKEN_URL;
  assert.ok(base, 'real API test server required');
  const chrome = ['/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/usr/bin/google-chrome', '/usr/bin/chromium'].find(fs.existsSync);
  assert.ok(chrome, 'Chrome required');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'file-token-browser-'));
  const child = cp.spawn(chrome, ['--headless=new', '--no-sandbox', '--remote-debugging-port=0', `--user-data-dir=${dir}`, '--disable-gpu', '--no-first-run', 'about:blank'], { stdio: 'ignore' });
  let ws;
  try {
    let port;
    for (let i = 0; i < 100; i++) { try { port = fs.readFileSync(path.join(dir, 'DevToolsActivePort'), 'utf8').split('\n')[0]; break; } catch { await new Promise(r => setTimeout(r, 100)); } }
    assert.ok(port, 'Chrome startup');
    const tabs = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
    ws = new WebSocket(tabs.find(t => t.type === 'page').webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { ws.onopen = resolve; ws.onerror = reject; });
    let seq = 0; const pending = new Map(); const errors = [];
    ws.onmessage = event => { const msg = JSON.parse(event.data); if (msg.id) { const p = pending.get(msg.id); if (p) { pending.delete(msg.id); msg.error ? p.reject(msg.error) : p.resolve(msg.result); } } else if (msg.method === 'Runtime.exceptionThrown') errors.push(msg.params.exceptionDetails.text); };
    const send = (method, params = {}) => new Promise((resolve, reject) => { const id = ++seq; pending.set(id, { resolve, reject }); ws.send(JSON.stringify({ id, method, params })); });
    const evaluate = async expression => { const r = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }); assert.equal(r.exceptionDetails, undefined, JSON.stringify(r.exceptionDetails)); return r.result.value; };
    const wait = async expression => { for (let i = 0; i < 100; i++) { if (await evaluate(expression)) return; await new Promise(r => setTimeout(r, 50)); } assert.fail(`Timed out: ${expression}; state=${JSON.stringify(await evaluate(`({hash:location.hash,login:document.getElementById("loginOverlay")?.className,page:document.getElementById("pageFiles")?.className,message:document.getElementById("fileTokensMessage")?.textContent,list:document.getElementById("fileTokensList")?.textContent,errors:${JSON.stringify(errors)}})`))}`); };
    const click = async selector => { const point = await evaluate(`(() => { const e=document.querySelector(${JSON.stringify(selector)});const r=e.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`); await send('Input.dispatchMouseEvent', { type: 'mousePressed', ...point, button: 'left', clickCount: 1 }); await send('Input.dispatchMouseEvent', { type: 'mouseReleased', ...point, button: 'left', clickCount: 1 }); };
    await send('Runtime.enable'); await send('Page.enable');
    await send('Network.setCookie', { name: 'homeagent_session', value: process.env.TEST_FILE_TOKEN_SESSION, url: base, httpOnly: true });
    await send('Page.navigate', { url: `${base}/#/files` });
    await wait('document.getElementById("fileTokensOpen") && document.getElementById("pageFiles").classList.contains("active") && document.getElementById("loginOverlay").classList.contains("hidden")');
    for (const width of [375, 390, 768, 1440]) {
      await send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: false });
      await click('#fileTokensOpen'); await wait('document.getElementById("fileTokensDialog").open');
      await wait('document.getElementById("fileTokensList").textContent.includes("暂无")');
      const geometry = await evaluate(`(() => {const d=document.getElementById('fileTokensDialog'),r=d.getBoundingClientRect(),b=document.getElementById('fileTokensCreate'),br=b.getBoundingClientRect(),hit=document.elementFromPoint(br.x+br.width/2,br.y+br.height/2);return {left:r.left,right:r.right,width:innerWidth,overflow:d.scrollWidth>d.clientWidth+1,focused:document.activeElement.id,hit:hit===b||b.contains(hit),height:br.height};})()`);
      assert.ok(geometry.left >= 0 && geometry.right <= geometry.width); assert.equal(geometry.overflow, false); assert.equal(geometry.focused, 'fileTokenName'); assert.equal(geometry.hit, true); assert.ok(geometry.height >= 44);
      await send('Input.dispatchKeyEvent', { type: 'keyDown', key: 'Tab', code: 'Tab', modifiers: 8 }); await send('Input.dispatchKeyEvent', { type: 'keyUp', key: 'Tab', code: 'Tab', modifiers: 8 });
      assert.equal(await evaluate('document.getElementById("fileTokensDialog").contains(document.activeElement)'), true);
      await click('#fileTokensClose'); await wait('!document.getElementById("fileTokensDialog").open'); assert.equal(await evaluate('document.activeElement.id'), 'fileTokensOpen');
    }
    await click('#fileTokensOpen'); await wait('document.getElementById("fileTokensDialog").open');
    await evaluate('document.getElementById("fileTokenName").value="iPhone <test>"');
    assert.equal(await evaluate('document.getElementById("fileTokenDays").value'), "365");
    await evaluate('document.getElementById("fileTokenDays").value="-1"');
    await click('#fileTokensCreate'); await wait('document.getElementById("fileTokenSecret").textContent.startsWith("agt_file_")');
    await wait('document.getElementById("fileTokensList").textContent.includes("iPhone <test>")');
    assert.equal(await evaluate('document.getElementById("fileTokensList").textContent.includes("到期：永久")'), true);
    assert.equal(await evaluate('document.querySelector("#fileTokensList test")'), null);
    await evaluate('Object.defineProperty(navigator, "clipboard", {configurable:true,value:{writeText:async()=>{throw new Error("denied")}}})');
    await click('#fileTokenCopy'); await wait('document.getElementById("fileTokensMessage").textContent.includes("复制失败")');
    await evaluate('Object.defineProperty(navigator, "clipboard", {configurable:true,value:{writeText:async()=>{}}})');
    await click('#fileTokenCopy'); await wait('document.getElementById("fileTokensMessage").textContent.includes("已复制")');
    const raw = await evaluate('document.getElementById("fileTokenSecret").textContent');
    const check = await fetch(`${base}/api/v1/files`, { headers: { Authorization: `Bearer ${raw}` } }); assert.equal(check.status, 200);
    await click('#fileTokensClose'); await click('#fileTokensOpen'); await wait('document.getElementById("fileTokensList").querySelector("button")');
    assert.equal(await evaluate('document.getElementById("fileTokenSecret").textContent'), '');
    await evaluate('window.confirm=()=>true'); await click('#fileTokensList button'); await wait('document.getElementById("fileTokensList").textContent.includes("已撤销")');
    const rejected = await fetch(`${base}/api/v1/files`, { headers: { Authorization: `Bearer ${raw}` } }); assert.equal(rejected.status, 401);
    assert.deepEqual(errors, []);
  } finally {
    if (ws) ws.close(); child.kill('SIGKILL'); await new Promise(resolve => child.exitCode !== null ? resolve() : child.once('exit', resolve)); fs.rmSync(dir, { recursive: true, force: true });
  }
});
