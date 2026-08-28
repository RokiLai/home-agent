import { apiFetch } from './api.js';

const labels = {
  ssh_keys: 'SSH 密钥同步', upgrade: '客户端升级', shutdown: '远程关机',
  github_credentials_sync: 'GitHub 凭据同步', github_credentials_revoke: 'GitHub 凭据撤销'
};

function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>'"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;',"'":'&#39;','"':'&quot;'}[c]));
}

export async function fetchCommands() {
  const body = document.getElementById('commandsTableBody');
  if (!body) return;
  try {
    const res = await apiFetch('/api/v1/commands?limit=100');
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    const commands = data.commands || [];
    document.getElementById('commandCount').textContent = `${commands.length} 条`;
    body.innerHTML = commands.length ? commands.map(c => `<tr>
      <td data-label="时间">${escapeHTML(new Date(c.created_at).toLocaleString())}</td>
      <td data-label="设备" class="font-mono">${escapeHTML(c.device_id)}</td>
      <td data-label="类型">${escapeHTML(labels[c.kind] || c.kind)}</td>
      <td data-label="状态"><span class="status-badge">${escapeHTML(c.status)}</span></td>
      <td data-label="结果">${escapeHTML(c.error_message || (c.status === 'legacy_untracked' ? '旧客户端：结果不可关联' : ''))}</td>
    </tr>`).join('') : '<tr><td colspan="5">暂无操作记录</td></tr>';
  } catch (error) {
    body.innerHTML = `<tr><td colspan="5">加载失败：${escapeHTML(error.message)}</td></tr>`;
  }
}
