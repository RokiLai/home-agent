import { apiFetch } from './api.js';
import { state } from './state.js';
import { copyToClipboard, escapeHTML, showToast } from './utils.js';

let settingsRevision = 0;
let activeFileId = '';
let latestCapacity = { quota: 0, used: 0, reserved: 0 };
let latestFiles = [];
let uploadInProgress = false;
let uploadRetryAvailable = false;

export function formatBytes(value) {
  const bytes = Number(value || 0);
  if (bytes < 1024) return `${bytes} B`;
  const units = ['KiB', 'MiB', 'GiB', 'TiB'];
  let size = bytes;
  let unit = -1;
  do { size /= 1024; unit += 1; } while (size >= 1024 && unit < units.length - 1);
  return `${Number.isInteger(size) ? size : size.toFixed(1)} ${units[unit]}`;
}

function formatDate(value) {
  if (!value) return '-';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString();
}

export function renderFileList(data) {
  const files = Array.isArray(data?.files) ? data.files : [];
  const body = document.getElementById('filesTableBody');
  const usageText = document.getElementById('fileUsageText');
  const usageBar = document.getElementById('fileUsageBar');
  const empty = document.getElementById('fileEmptyState');
  const error = document.getElementById('fileLoadError');
  const quota = Number(data?.quota_bytes || 0);
  const used = Number(data?.used_bytes || 0);
  const reserved = Number(data?.reserved_bytes || 0);
  latestFiles = files;
  latestCapacity = { quota, used, reserved };
  if (usageText) usageText.innerText = `${formatBytes(used)} / ${formatBytes(quota)}${reserved ? `，上传预留 ${formatBytes(reserved)}` : ''}`;
  if (usageBar) usageBar.style.width = `${quota > 0 ? Math.min(100, (used + reserved) / quota * 100) : 0}%`;
  if (error) error.classList.add('hidden');
  if (empty) empty.classList.toggle('hidden', files.length !== 0);
  if (!body) return;
  body.innerHTML = files.map(file => `
    <tr>
      <td><strong class="file-name">${escapeHTML(file.name)}</strong><span class="file-hash font-mono">${escapeHTML((file.sha256 || '').slice(0, 12))}</span></td>
      <td>${formatBytes(file.size)}</td>
      <td>${escapeHTML(formatDate(file.created_at))}</td>
      <td>${escapeHTML(formatDate(file.expires_at))}</td>
      <td><div class="file-actions">
        <button class="btn btn-icon btn-secondary" title="下载文件" aria-label="下载 ${escapeHTML(file.name)}" onclick="window.downloadFile('${escapeHTML(file.id)}')">&#8595;</button>
        <button class="btn btn-icon btn-secondary" title="管理限时链接" aria-label="管理 ${escapeHTML(file.name)} 的限时链接" onclick="window.openFileLinks('${escapeHTML(file.id)}', '${escapeHTML(file.name)}')">&#128279;</button>
        <button class="btn btn-icon btn-danger-ghost" title="删除文件" aria-label="删除 ${escapeHTML(file.name)}" onclick="window.deleteFile('${escapeHTML(file.id)}', '${escapeHTML(file.name)}')">&#128465;</button>
      </div></td>
    </tr>`).join('');
}

export function canUploadSize(size) {
  return latestCapacity.quota > 0 && Number(size) <= latestCapacity.quota - latestCapacity.used - latestCapacity.reserved;
}

export async function fetchFiles() {
  const error = document.getElementById('fileLoadError');
  try {
    const response = await apiFetch(`${state.serverHost}/api/v1/files`);
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    const data = await response.json();
    renderFileList(data);
    return data;
  } catch (err) {
    if (error) { error.innerText = `文件列表加载失败：${err.message}`; error.classList.remove('hidden'); }
    return null;
  }
}

function updateFileSelection() {
  const input = document.getElementById('fileUploadInput');
  const selection = document.getElementById('fileUploadSelection');
  if (!selection) return;
  const name = input?.files?.[0]?.name || '未选择文件';
  selection.innerText = name;
  selection.title = name;
}

function setUploadButton(label, disabled = false) {
  const button = document.getElementById('fileUploadBtn');
  if (!button) return;
  button.innerText = label;
  button.disabled = disabled;
}

function handleUploadButtonClick() {
  if (uploadInProgress) return;
  const input = document.getElementById('fileUploadInput');
  if (uploadRetryAvailable && input?.files?.[0]) {
    uploadSelectedFile();
    return;
  }
  input?.click();
}

export function uploadSelectedFile() {
  const input = document.getElementById('fileUploadInput');
  const progress = document.getElementById('fileUploadProgress');
  const bar = document.getElementById('fileUploadProgressBar');
  const text = document.getElementById('fileUploadProgressText');
  const file = input?.files?.[0];
  if (!file) { showToast('请选择文件'); return; }
  if (!canUploadSize(file.size)) {
    showToast(`文件超过当前剩余容量（${formatBytes(Math.max(0, latestCapacity.quota - latestCapacity.used - latestCapacity.reserved))}）`);
    uploadRetryAvailable = true;
    setUploadButton('重新上传');
    return;
  }
  const data = new FormData(); data.append('file', file);
  const xhr = new XMLHttpRequest();
  xhr.open('POST', `${state.serverHost}/api/v1/files`);
  xhr.withCredentials = true;
  uploadInProgress = true;
  uploadRetryAvailable = false;
  setUploadButton('上传中 0%', true);
  if (progress) progress.classList.remove('hidden');
  xhr.upload.addEventListener('progress', event => {
    if (!event.lengthComputable) return;
    const percent = Math.round(event.loaded / event.total * 100);
    if (bar) bar.style.width = `${percent}%`;
    if (text) text.innerText = `${percent}% · ${formatBytes(event.loaded)} / ${formatBytes(event.total)}`;
    setUploadButton(percent >= 100 ? '正在保存…' : `上传中 ${percent}%`, true);
  });
  xhr.upload.addEventListener('load', () => {
    if (text) text.innerText = '文件已传输，正在保存…';
    setUploadButton('正在保存…', true);
  });
  xhr.addEventListener('loadend', async () => {
    uploadInProgress = false;
    if (xhr.status === 201) {
      let uploadedFile = null;
      try { uploadedFile = JSON.parse(xhr.responseText); } catch (_) {}
      if (input) input.value = '';
      uploadRetryAvailable = false;
      updateFileSelection();
      setUploadButton('选择并上传文件');
      if (uploadedFile?.id) {
        renderFileList({
          files: [uploadedFile, ...latestFiles.filter(item => item.id !== uploadedFile.id)],
          quota_bytes: latestCapacity.quota,
          used_bytes: latestCapacity.used + Number(uploadedFile.size || 0),
          reserved_bytes: 0
        });
      }
      showToast('文件上传成功');
      await fetchFiles();
    } else {
      uploadRetryAvailable = true;
      setUploadButton('重新上传');
      showToast(xhr.status === 507 ? '存储空间不足' : `上传失败（HTTP ${xhr.status || 0}）`);
    }
  });
  xhr.send(data);
}

export function downloadFile(id) {
  window.location.assign(`${state.serverHost}/api/v1/files/${encodeURIComponent(id)}/download`);
}

export async function deleteFile(id, name) {
  if (!window.confirm(`确认删除“${name}”？相关限时链接会立即失效。`)) return;
  const response = await apiFetch(`${state.serverHost}/api/v1/files/${encodeURIComponent(id)}`, { method: 'DELETE' });
  if (!response.ok) { showToast(`删除失败（HTTP ${response.status}）`); return; }
  showToast('文件已删除');
  await fetchFiles();
}

function renderLinks(links) {
  const list = document.getElementById('fileLinksList');
  if (!list) return;
  list.innerHTML = links.length ? links.map(link => `
    <div class="file-link-row"><span>有效至 ${escapeHTML(formatDate(link.expires_at))}${link.revoked_at ? '（已撤销）' : ''}</span>
    ${link.revoked_at ? '' : `<button class="btn btn-secondary btn-sm" onclick="window.revokeFileLink('${escapeHTML(link.id)}')">撤销</button>`}</div>`).join('') : '<p class="text-muted">尚未创建限时链接</p>';
}

async function loadLinks() {
  const response = await apiFetch(`${state.serverHost}/api/v1/files/${encodeURIComponent(activeFileId)}/links`);
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  renderLinks((await response.json()).links || []);
}

export async function openFileLinks(id, name) {
  activeFileId = id;
  const dialog = document.getElementById('fileLinksDialog');
  const title = document.getElementById('fileLinksTitle');
  if (title) title.innerText = `限时链接 · ${name}`;
  try { await loadLinks(); dialog?.showModal(); } catch (err) { showToast(`链接加载失败：${err.message}`); }
}

export async function createFileLink() {
  const duration = Number(document.getElementById('fileLinkDuration')?.value || 86400);
  const response = await apiFetch(`${state.serverHost}/api/v1/files/${encodeURIComponent(activeFileId)}/links`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expires_in_seconds: duration })
  });
  if (!response.ok) { showToast(`链接创建失败（HTTP ${response.status}）`); return; }
  const link = await response.json();
  copyToClipboard(link.download_url, '限时下载链接已复制');
  await loadLinks();
}

export async function revokeFileLink(linkId) {
  if (!window.confirm('确认撤销这个下载链接？')) return;
  const response = await apiFetch(`${state.serverHost}/api/v1/files/${encodeURIComponent(activeFileId)}/links/${encodeURIComponent(linkId)}`, { method: 'DELETE' });
  if (!response.ok) { showToast(`撤销失败（HTTP ${response.status}）`); return; }
  await loadLinks();
}

export function renderFileSettings(data, user = state.currentUser) {
  const input = document.getElementById('fileRetentionDaysInput');
  const button = document.getElementById('saveFileSettingsBtn');
  const usage = document.getElementById('fileSettingsUsage');
  settingsRevision = Number(data?.revision || 0);
  if (input) { input.value = Number(data?.retention_days || 7); input.disabled = user?.role !== 'owner'; }
  if (button) button.disabled = user?.role !== 'owner';
  if (usage) usage.innerText = `${formatBytes(data?.used_bytes)} / ${formatBytes(data?.quota_bytes)}`;
}

export async function loadFileSettings() {
  const response = await apiFetch(`${state.serverHost}/api/v1/files/settings`);
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  renderFileSettings(await response.json());
}

export async function saveFileSettings() {
  const retentionDays = Number(document.getElementById('fileRetentionDaysInput')?.value);
  const response = await apiFetch(`${state.serverHost}/api/v1/files/settings`, {
    method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ retention_days: retentionDays, revision: settingsRevision })
  });
  if (!response.ok) { showToast(`设置保存失败（HTTP ${response.status}）`); return; }
  showToast('文件保留设置已保存');
  await loadFileSettings();
}

export function initFileShare() {
  document.getElementById('fileUploadInput')?.addEventListener('change', () => {
    uploadRetryAvailable = false;
    updateFileSelection();
    if (document.getElementById('fileUploadInput')?.files?.[0]) uploadSelectedFile();
  });
  document.getElementById('fileUploadBtn')?.addEventListener('click', handleUploadButtonClick);
  document.getElementById('createFileLinkBtn')?.addEventListener('click', createFileLink);
  document.getElementById('closeFileLinksBtn')?.addEventListener('click', () => document.getElementById('fileLinksDialog')?.close());
  document.getElementById('saveFileSettingsBtn')?.addEventListener('click', saveFileSettings);
  window.downloadFile = downloadFile;
  window.deleteFile = deleteFile;
  window.openFileLinks = openFileLinks;
  window.revokeFileLink = revokeFileLink;
}
