import { apiFetch } from './api.js';
import { escapeHTML } from './utils.js';

const endpoint = '/api/v1/file-access-tokens';
const labels = { active: '有效', expired: '已过期', revoked: '已撤销', account_invalid: '账户失效' };
let generation = 0;
let busy = false;
let initialized = false;
const element = id => document.getElementById(id);
const message = text => { element('fileTokensMessage').textContent = text; };

async function request(url, options) {
  const response = await apiFetch(url, options);
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    const errors = { token_limit_reached: '有效令牌已达 20 个，请先撤销旧令牌。', invalid_request: '请填写 1–64 字名称并选择有效期。', file_token_service_unavailable: '令牌服务暂不可用，请稍后重试。' };
    throw new Error(errors[body.error] || '操作失败，请重试。');
  }
  return response.status === 204 ? null : response.json();
}

async function refresh(epoch) {
  const result = await request(endpoint);
  if (epoch !== generation || !element('fileTokensDialog').open) return;
  if (!Array.isArray(result.tokens)) throw new Error('令牌列表响应无效。');
  element('fileTokensList').innerHTML = result.tokens.length ? result.tokens.map(token => {
    if (!labels[token.status]) throw new Error('令牌状态无法识别。');
    return `<div class="file-token-row"><div><strong>${escapeHTML(token.name)}</strong><p>创建：${escapeHTML(token.created_at)}<br>到期：${token.permanent === true && token.expires_at === null ? "永久" : escapeHTML(token.expires_at)}<br>${labels[token.status]}</p></div><button type="button" class="btn btn-secondary" data-token-id="${escapeHTML(token.id)}" data-token-name="${escapeHTML(token.name)}" ${token.status === 'revoked' ? 'disabled' : ''}>撤销</button></div>`;
  }).join('') : '<p>暂无专用令牌</p>';
}

function resetSecret() {
  element('fileTokenSecret').textContent = '';
  element('fileTokenSecretPanel').hidden = true;
}

export function initFileAccessTokens() {
  if (initialized || !element('fileTokensDialog')) return;
  initialized = true;
  const dialog = element('fileTokensDialog');
  const create = element('fileTokensCreate');
  const setBusy = value => { busy = value; create.disabled = value; element('fileTokenName').disabled = value; element('fileTokenDays').disabled = value; };
  element('fileTokensOpen').addEventListener('click', async () => {
    resetSecret(); message(''); element('fileTokensList').textContent = '正在加载…';
    dialog.showModal(); element('fileTokenName').focus();
    const epoch = ++generation;
    try { await refresh(epoch); } catch (error) { if (epoch === generation && dialog.open) message(error.message); }
  });
  element('fileTokensClose').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', () => { if (dialog.open) return; ++generation; resetSecret(); message(''); element('fileTokensList').textContent = ''; element('fileTokenName').value = ''; element('fileTokensOpen').focus(); });
  dialog.addEventListener('keydown', event => {
    if (event.key !== 'Tab') return;
    const controls = Array.from(dialog.querySelectorAll('button:not(:disabled), input:not(:disabled), select:not(:disabled)')).filter(node => node.getClientRects().length > 0);
    const first = controls[0]; const last = controls.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });
  element('fileTokensForm').addEventListener('submit', async event => {
    event.preventDefault(); if (busy) return;
    const epoch = generation; setBusy(true); resetSecret(); message('');
    try {
      const result = await request(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name: element('fileTokenName').value.trim(), expires_in_days: Number(element('fileTokenDays').value) }) });
      if (epoch !== generation || !dialog.open) return;
      if (typeof result.token !== 'string' || !result.token.startsWith('agt_file_')) throw new Error('创建响应无效，请检查令牌列表。');
      element('fileTokenSecret').textContent = result.token; element('fileTokenSecretPanel').hidden = false;
      message('令牌仅显示一次，请复制后妥善保存。'); await refresh(epoch);
    } catch (error) { if (epoch === generation && dialog.open) message(error.message); }
    finally { setBusy(false); }
  });
  element('fileTokenCopy').addEventListener('click', async () => {
    const epoch = generation;
    try { await navigator.clipboard.writeText(element('fileTokenSecret').textContent); if (epoch === generation && dialog.open) message('已复制令牌。'); }
    catch { if (epoch === generation && dialog.open) message('复制失败，请手动选择复制。'); }
  });
  element('fileTokensList').addEventListener('click', async event => {
    const button = event.target.closest('button[data-token-id]');
    if (!button || busy || !dialog.contains(button) || !window.confirm(`撤销“${button.dataset.tokenName}”？使用此令牌的快捷指令将无法继续访问；已生成的下载链接仍有效。`)) return;
    const epoch = generation; setBusy(true); button.disabled = true; message('');
    try { await request(`${endpoint}/${encodeURIComponent(button.dataset.tokenId)}`, { method: 'DELETE' }); if (epoch === generation && dialog.open) { message('已撤销。'); await refresh(epoch); } }
    catch (error) { if (epoch === generation && dialog.open) { button.disabled = false; message(error.message); } }
    finally { setBusy(false); }
  });
}
