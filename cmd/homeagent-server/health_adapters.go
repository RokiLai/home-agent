package main

import (
	"context"
	"time"

	"homeagent/internal/broker"
	"homeagent/internal/command"
	"homeagent/internal/ddns"
	"homeagent/internal/devicestate"
	"homeagent/internal/health"
	"homeagent/internal/prefixstate"
	"homeagent/internal/registry"
	"homeagent/internal/sshsync"
	"homeagent/internal/version"
)

type serverHealthAdapters struct {
	reg         *registry.Registry
	broker      *broker.Broker
	syncer      *sshsync.Controller
	adminPub    string
	devState    *devicestate.Service
	prefixState *prefixstate.Service
	ddnsSvc     *ddns.Service
	cmdRepo     command.Repository
}

func (a *serverHealthAdapters) GetDeviceFacts(ctx context.Context, deviceID string) (*health.DeviceFactSummary, error) {
	d, err := a.reg.Get(deviceID)
	if err != nil {
		return nil, err
	}
	connected := false
	if a.broker != nil {
		connected = a.broker.IsConnected(d.ID)
	}

	var facts *health.RuntimeFacts
	if d.RuntimeFacts != nil {
		facts = &health.RuntimeFacts{
			ObservedAt:           d.RuntimeFacts.ObservedAt,
			UptimeSeconds:        d.RuntimeFacts.UptimeSeconds,
			Load1:                d.RuntimeFacts.Load1,
			LogicalCPUCount:      d.RuntimeFacts.LogicalCPUCount,
			MemoryTotalBytes:     d.RuntimeFacts.MemoryTotalBytes,
			MemoryAvailableBytes: d.RuntimeFacts.MemoryAvailableBytes,
			DiskTotalBytes:       d.RuntimeFacts.DiskTotalBytes,
			DiskAvailableBytes:   d.RuntimeFacts.DiskAvailableBytes,
			DiskMount:            d.RuntimeFacts.DiskMount,
		}
	}

	return &health.DeviceFactSummary{
		ID:                d.ID,
		Hostname:          d.Hostname,
		AgentVersion:      d.AgentVersion,
		OS:                d.OS,
		Arch:              d.Arch,
		LastSeenAt:        d.LastSeenAt,
		Connected:         connected,
		SyncStatus:        d.SyncStatus,
		AppliedVersion:    d.AppliedVersion,
		AppliedHash:       d.AppliedHash,
		SyncError:         d.SyncError,
		SyncUpdatedAt:     d.SyncUpdatedAt,
		GitHubSyncEnabled: d.GitHubSyncEnabled,
		GitHubStatus:      d.GitHubStatus,
		RuntimeFacts:      facts,
	}, nil
}

func (a *serverHealthAdapters) ListAllDeviceIDs(ctx context.Context) ([]string, error) {
	devices := a.reg.List()
	ids := make([]string, 0, len(devices))
	for _, d := range devices {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

func (a *serverHealthAdapters) GetDesiredKeySet(ctx context.Context) (int64, string, bool, error) {
	devices := a.reg.List()
	keys := make([]sshsync.Key, 0, len(devices)+1)
	if a.adminPub != "" {
		keys = append(keys, sshsync.Key{DeviceID: "homeagent-admin", PublicKey: a.adminPub})
	}
	for _, d := range devices {
		keys = append(keys, sshsync.Key{DeviceID: d.ID, PublicKey: d.PublicKey})
	}
	hash := sshsync.ComputeKeySetHash(keys)
	return 1, hash, true, nil
}

func (a *serverHealthAdapters) GetDeviceDDNSState(ctx context.Context, deviceID string) (*health.DDNSDeviceState, error) {
	if a.devState == nil {
		return nil, nil
	}
	st, err := a.devState.Get(deviceID)
	if err != nil || st == nil {
		return nil, nil
	}
	prefixStale := false
	if a.prefixState != nil && st.NetworkID != "" {
		ps, pErr := a.prefixState.GetByNetwork(st.NetworkID)
		if pErr == nil && ps != nil && !ps.LastSeenAt.IsZero() && time.Now().UTC().Sub(ps.LastSeenAt) > 15*time.Minute {
			prefixStale = true
		}
	}

	inGrace := false
	var graceUntil time.Time
	if st.GracePeriodEnd != nil {
		graceUntil = *st.GracePeriodEnd
		inGrace = time.Now().UTC().Before(graceUntil)
	}

	return &health.DDNSDeviceState{
		Enabled:       true,
		SyncStatus:    st.SyncStatus,
		SyncError:     st.SyncError,
		DesiredIPv6:   st.DesiredAddress,
		AppliedIPv6:   st.AppliedAddress,
		InGracePeriod: inGrace,
		GraceUntil:    graceUntil,
		PrefixStale:   prefixStale,
	}, nil
}

func (a *serverHealthAdapters) GetLatestCommand(ctx context.Context, deviceID string, kind string) (*health.CommandSummary, error) {
	if a.cmdRepo == nil {
		return nil, nil
	}
	k := command.Kind(kind)
	page, err := a.cmdRepo.List(command.Filter{
		DeviceID: deviceID,
		Kind:     k,
		Limit:    1,
	})
	if err != nil || len(page.Commands) == 0 {
		return nil, err
	}
	cmd := page.Commands[0]
	return &health.CommandSummary{
		CommandID:  string(cmd.ID),
		Kind:       string(cmd.Kind),
		Status:     string(cmd.Status),
		CreatedAt:  cmd.CreatedAt,
		FinishedAt: cmd.FinishedAt,
		ErrorCode:  cmd.ErrorCode,
	}, nil
}

func (a *serverHealthAdapters) GetVersionPolicy(ctx context.Context) (string, string, error) {
	return version.Get(), "v0.4.0", nil
}

type serverNameResolver struct {
	reg *registry.Registry
}

func (r *serverNameResolver) GetDeviceName(ctx context.Context, deviceID string) string {
	if r.reg == nil {
		return deviceID
	}
	d, err := r.reg.Get(deviceID)
	if err != nil {
		return deviceID
	}
	if d.Alias != "" {
		return d.Alias
	}
	if d.Hostname != "" {
		return d.Hostname
	}
	return deviceID
}

