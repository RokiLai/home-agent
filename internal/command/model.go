// Package command owns the durable lifecycle of control-plane commands.
package command

import (
	"encoding/json"
	"errors"
	"time"
)

type ID string
type Kind string
type Status string

const (
	KindSSHKeys      Kind = "ssh_keys"
	KindUpgrade      Kind = "upgrade"
	KindShutdown     Kind = "shutdown"
	KindGitHubSync   Kind = "github_credentials_sync"
	KindGitHubRevoke Kind = "github_credentials_revoke"

	StatusQueued          Status = "queued"
	StatusDispatching     Status = "dispatching"
	StatusDispatched      Status = "dispatched"
	StatusAccepted        Status = "accepted"
	StatusSucceeded       Status = "succeeded"
	StatusFailed          Status = "failed"
	StatusTimedOut        Status = "timed_out"
	StatusCanceled        Status = "canceled"
	StatusInterrupted     Status = "interrupted"
	StatusLegacyUntracked Status = "legacy_untracked"
)

var (
	ErrNotFound            = errors.New("command not found")
	ErrConflict            = errors.New("command revision conflict")
	ErrInvalidTransition   = errors.New("invalid command transition")
	ErrIdempotencyConflict = errors.New("idempotency conflict")
	ErrCommandInProgress   = errors.New("command already in progress")
	ErrLateAckAccepted     = errors.New("late acknowledgment accepted for audit")
)

type Actor struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}
type TimeoutPolicy struct {
	Accept time.Duration `json:"accept"`
	Finish time.Duration `json:"finish"`
}
type LateAck struct {
	Digest     string    `json:"digest"`
	ReceivedAt time.Time `json:"received_at"`
	Conflict   bool      `json:"conflict,omitempty"`
}
type ProjectionState struct {
	Status   string `json:"status,omitempty"`
	Revision uint64 `json:"revision,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Command struct {
	ID                    ID              `json:"id"`
	Kind                  Kind            `json:"kind"`
	DeviceID              string          `json:"device_id"`
	Status                Status          `json:"status"`
	RequestedBy           Actor           `json:"requested_by"`
	IdempotencyKey        string          `json:"idempotency_key,omitempty"`
	Request               json.RawMessage `json:"request,omitempty"`
	RequestDigest         string          `json:"request_digest,omitempty"`
	Result                json.RawMessage `json:"result,omitempty"`
	ErrorCode             string          `json:"error_code,omitempty"`
	ErrorMessage          string          `json:"error_message,omitempty"`
	CreatedAt             time.Time       `json:"created_at"`
	UpdatedAt             time.Time       `json:"updated_at"`
	DispatchedAt          *time.Time      `json:"dispatched_at,omitempty"`
	AcceptedAt            *time.Time      `json:"accepted_at,omitempty"`
	FinishedAt            *time.Time      `json:"finished_at,omitempty"`
	TimeoutPolicy         TimeoutPolicy   `json:"timeout_policy"`
	AcceptDeadline        *time.Time      `json:"accept_deadline,omitempty"`
	FinishDeadline        *time.Time      `json:"finish_deadline,omitempty"`
	FinalAckDigest        string          `json:"final_ack_digest,omitempty"`
	LateAcks              []LateAck       `json:"late_acks,omitempty"`
	OutcomeRevision       uint64          `json:"outcome_revision,omitempty"`
	ProjectionInputDigest string          `json:"projection_input_digest,omitempty"`
	Projection            ProjectionState `json:"projection"`
	Revision              uint64          `json:"revision"`
	Protocol              int             `json:"protocol"`
	AttemptID             string          `json:"attempt_id,omitempty"`
}

func (c Command) Terminal() bool {
	switch c.Status {
	case StatusSucceeded, StatusFailed, StatusTimedOut, StatusCanceled, StatusInterrupted, StatusLegacyUntracked:
		return true
	}
	return false
}

type CreateRequest struct {
	Kind           Kind
	DeviceID       string
	RequestedBy    Actor
	IdempotencyKey string
	Request        json.RawMessage
	TimeoutPolicy  TimeoutPolicy
	Protocol       int
}
type Filter struct {
	DeviceID string
	Kind     Kind
	Status   Status
	Limit    int
	Cursor   string
}
type Page struct {
	Commands   []Command `json:"commands"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
type Transition struct {
	Status                                                                    Status
	Result                                                                    json.RawMessage
	ErrorCode, ErrorMessage, FinalAckDigest, AttemptID, ProjectionInputDigest string
	At                                                                        time.Time
}

type Repository interface {
	CreateOrGet(CreateRequest) (Command, bool, error)
	Get(ID) (Command, error)
	Transition(ID, uint64, Transition) (Command, error)
	List(Filter) (Page, error)
	ListExpired(time.Time, int) ([]Command, error)
	InterruptNonTerminal(time.Time) (int, error)
	AppendLateAck(ID, uint64, LateAck) (Command, error)
	UpdateProjection(ID, uint64, ProjectionState) (Command, error)
	ListProjectionPending(int) ([]Command, error)
}
