package domainbinding

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

const (
	ManagedSuffix       = "rokilai.online"
	LocalServerSourceID = "local-server"
)

type SourceType string
type BindingState string
type RuntimeState string

const (
	SourceServer SourceType = "server"
	SourceDevice SourceType = "device"

	ConfigObserving BindingState = "observing"
	ConfigEnabled   BindingState = "enabled"
	ConfigDisabled  BindingState = "disabled"
	ConfigDeleted   BindingState = "deleted"

	RuntimeWaitingReport RuntimeState = "waiting_report"
	RuntimePending       RuntimeState = "pending"
	RuntimeSyncing       RuntimeState = "syncing"
	RuntimeSynced        RuntimeState = "synced"
	RuntimeFailed        RuntimeState = "failed"
	RuntimeStale         RuntimeState = "stale"
)

var (
	ErrInvalidFQDN                   = errors.New("invalid managed fqdn")
	ErrFQDNConflict                  = errors.New("fqdn already bound")
	ErrBindingNotFound               = errors.New("domain binding not found")
	ErrBindingRevisionConflict       = errors.New("domain binding revision conflict")
	ErrPublisherConfirmationRequired = errors.New("existing publisher exclusion confirmation required")
)

type Binding struct {
	BindingID            string       `json:"binding_id"`
	SourceType           SourceType   `json:"source_type"`
	SourceID             string       `json:"source_id"`
	FQDN                 string       `json:"fqdn"`
	ConfigState          BindingState `json:"config_state"`
	RuntimeState         RuntimeState `json:"runtime_state"`
	Revision             uint64       `json:"revision"`
	ControlPlaneRevision uint64       `json:"control_plane_revision"`
	ProviderRecordID     string       `json:"provider_record_id,omitempty"`
	TTL                  int          `json:"ttl,omitempty"`
	Proxied              bool         `json:"proxied"`
	DesiredIPv6          string       `json:"desired_ipv6,omitempty"`
	ProviderIPv6         string       `json:"provider_ipv6,omitempty"`
	LastAppliedIPv6      string       `json:"last_applied_ipv6,omitempty"`
	LastError            string       `json:"last_error,omitempty"`
	LastSyncedAt         *time.Time   `json:"last_synced_at,omitempty"`
	UpdatedAt            time.Time    `json:"updated_at"`
}

func NormalizeFQDN(value, suffix string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	ascii, err := idna.Lookup.ToASCII(value)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", ErrInvalidFQDN
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 {
			return "", ErrInvalidFQDN
		}
	}
	suffix = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(suffix), "."))
	if ascii != suffix && !strings.HasSuffix(ascii, "."+suffix) {
		return "", ErrInvalidFQDN
	}
	return ascii, nil
}
