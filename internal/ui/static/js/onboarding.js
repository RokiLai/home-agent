import { state } from './state.js';
import { apiFetch } from './api.js';

export async function fetchOrRefreshClaimToken() {
  try {
    const res = await apiFetch(`${state.serverHost}/api/v1/enrollment-tokens`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ttl_seconds: 900,
        max_uses: 1,
        description: 'Web Console Quick Onboarding'
      })
    });
    if (res.ok) {
      const data = await res.json();
      state.currentClaimToken = data.token || '';
      state.claimTokenExpiresAt = new Date(data.expires_at);
      startClaimCountdown();
      updateInstallCommand();
    }
  } catch (err) {
    console.error('Failed to generate claim token:', err);
  }
}

export function startClaimCountdown() {
  const claimTokenCountdown = document.getElementById('claimTokenCountdown');
  if (state.claimCountdownTimer) clearInterval(state.claimCountdownTimer);
  function tick() {
    if (!claimTokenCountdown) return;
    if (!state.claimTokenExpiresAt) {
      claimTokenCountdown.textContent = '暂无有效凭据';
      return;
    }
    const now = new Date();
    const diffSec = Math.max(0, Math.floor((state.claimTokenExpiresAt - now) / 1000));
    if (diffSec > 0) {
      const m = Math.floor(diffSec / 60);
      const s = diffSec % 60;
      claimTokenCountdown.textContent = `凭据有效倒计时: ${m}分${s < 10 ? '0' : ''}${s}秒 (单次有效)`;
    } else {
      claimTokenCountdown.textContent = '凭据已过期，请点击右侧「刷新凭据」重新生成';
    }
  }
  tick();
  state.claimCountdownTimer = setInterval(tick, 1000);
}

export function updateInstallCommand() {
  const installCode = document.getElementById('installCommandCode');
  const dashboardInstallPreviewCode = document.getElementById('dashboardInstallPreviewCode');
  let cmd = '';
  const host = state.publicUrl || state.serverHost || window.location.origin;
  const token = state.currentClaimToken || '<CLAIM_TOKEN>';
  const isDefaultServer = (host === 'https://homeagent.rokilai.online');
  const serverPrefix = isDefaultServer ? '' : `HOMEAGENT_SERVER="${host}" `;
  const psServerPrefix = isDefaultServer ? '' : `$env:HOMEAGENT_SERVER="${host}"; `;

  if (state.activeOSTab === 'darwin' || state.activeOSTab === 'linux' || state.activeOSTab === 'openwrt') {
    cmd = `curl -fsSL https://raw.githubusercontent.com/RokiLai/home-agent/main/scripts/install.sh | ${serverPrefix}HOMEAGENT_CLAIM_TOKEN="${token}" sh`;
  } else if (state.activeOSTab === 'windows') {
    cmd = `${psServerPrefix}$env:HOMEAGENT_CLAIM_TOKEN="${token}"; irm https://raw.githubusercontent.com/RokiLai/home-agent/main/scripts/install.ps1 | iex`;
  }

  if (installCode) installCode.innerText = cmd;
  if (dashboardInstallPreviewCode) {
    dashboardInstallPreviewCode.innerText = `curl -fsSL https://raw.githubusercontent.com/RokiLai/home-agent/main/scripts/install.sh | ${serverPrefix}HOMEAGENT_CLAIM_TOKEN="${token}" sh`;
  }
}
