export function sanitizeHost(url) {
  if (!url) return '';
  const clean = url.trim().replace(/\/+$/, '');
  return clean === window.location.origin ? '' : clean;
}

export const state = {
  devices: [],
  serverHash: '',
  serverHost: sanitizeHost(localStorage.getItem('homeagent_server_url')),
  publicUrl: '',
  activeOSTab: 'darwin',
  isFetching: false,
  currentFilter: 'all', // all, healthy, degraded, synced, pending
  currentPage: 'dashboard',
  currentClaimToken: '',
  claimTokenExpiresAt: null,
  claimCountdownTimer: null,
  isAuthenticated: false,
  currentUser: { id: '', username: '', role: 'owner', permissions: [] },
  usersList: [],
  wakingDevices: new Set(),
  upgradingDevices: new Set(),
  shuttingDownDevices: new Set(),
  openDropdownDevID: null,
  latestGitHubData: null,
  currentRenameDeviceId: '',
  currentShareDeviceId: '',
  currentTransferDeviceId: ''
};
