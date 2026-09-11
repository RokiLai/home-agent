// Package versionstatus manages independent Server and Agent release snapshots.
package versionstatus

import (
	"time"

	"homeagent/internal/githubrelease"
)

type Status string

const (
	StatusUnknown   Status = "unknown"
	StatusChecking  Status = "checking"
	StatusAvailable Status = "available"
	StatusStale     Status = "stale"
	StatusError     Status = "error"
)

type UpdateState string

const (
	UpdateCurrent   UpdateState = "current"
	UpdateAvailable UpdateState = "update_available"
	UpdateUnknown   UpdateState = "unknown"
)

// Snapshot is the persisted trusted result for one component channel.
type Snapshot struct {
	Component           githubrelease.Component `json:"component"`
	SnapshotID          string                  `json:"snapshot_id,omitempty"`
	Status              Status                  `json:"status"`
	CurrentVersion      string                  `json:"current_version,omitempty"`
	LatestVersion       string                  `json:"latest_version,omitempty"`
	UpdateState         UpdateState             `json:"update_state,omitempty"`
	CheckedAt           time.Time               `json:"checked_at,omitempty"`
	LastSuccessAt       time.Time               `json:"last_success_at,omitempty"`
	Stale               bool                    `json:"stale"`
	Refreshing          bool                    `json:"refreshing,omitempty"`
	ErrorCode           string                  `json:"error_code,omitempty"`
	ErrorMessage        string                  `json:"error_message,omitempty"`
	ReleaseURL          string                  `json:"release_url,omitempty"`
	ReleaseID           int64                   `json:"release_id,omitempty"`
	Tag                 string                  `json:"tag,omitempty"`
	Commitish           string                  `json:"commitish,omitempty"`
	Assets              []githubrelease.Asset   `json:"assets,omitempty"`
	RetryAt             time.Time               `json:"retry_at,omitempty"`
	ConsecutiveFailures int                     `json:"consecutive_failures,omitempty"`
}

type persistedState struct {
	SchemaVersion int                                  `json:"schema_version"`
	Channels      map[githubrelease.Component]Snapshot `json:"channels"`
}
