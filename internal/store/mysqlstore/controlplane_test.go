package mysqlstore

import (
	"errors"
	"strings"
	"testing"

	"homeagent/internal/store"
)

func TestSchemaContainsTransactionalControlPlaneSnapshot(t *testing.T) {
	joined := strings.Join(tableSchemas, "\n")
	for _, required := range []string{"control_plane_snapshots", "revision BIGINT UNSIGNED", "snapshot_json JSON"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("schema missing %q", required)
		}
	}
}

func TestControlPlaneJSONRoundTripAndValidation(t *testing.T) {
	snapshot := store.NewControlPlaneSnapshot()
	snapshot.Bindings["binding-1"] = &store.DomainBinding{BindingID: "binding-1", FQDN: "host.rokilai.online", Revision: 1}
	clone, err := cloneControlPlane(snapshot)
	if err != nil || clone.Bindings["binding-1"] == nil {
		t.Fatalf("clone=%+v err=%v", clone, err)
	}
	clone.Bindings["binding-2"] = &store.DomainBinding{BindingID: "binding-2", FQDN: "host.rokilai.online", Revision: 1}
	if _, err := cloneControlPlane(clone); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate fqdn error = %v", err)
	}
	if _, err := decodeControlPlane([]byte("{")); err == nil {
		t.Fatal("malformed JSON accepted")
	}
}
