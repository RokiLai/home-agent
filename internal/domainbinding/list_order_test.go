package domainbinding

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
	"homeagent/internal/store/mysqlstore"
)

// This repository models an internal snapshot read failure, not a DNS protocol.
type listOrderRepository struct {
	snapshot *store.ControlPlaneSnapshot
	err      error
	commits  int
}

func (r *listOrderRepository) Load(context.Context) (*store.ControlPlaneSnapshot, error) {
	return r.snapshot, r.err
}
func (r *listOrderRepository) Commit(context.Context, uint64, *store.ControlPlaneSnapshot) (*store.ControlPlaneSnapshot, error) {
	r.commits++
	return nil, errors.New("unexpected write during list")
}

func domainOrderSnapshot() *store.ControlPlaneSnapshot {
	snapshot := store.NewControlPlaneSnapshot()
	snapshot.Revision = 7
	for _, binding := range []store.DomainBinding{
		{BindingID: "z", FQDN: "z.rokilai.online", SourceType: "server", SourceID: LocalServerSourceID},
		{BindingID: "a", FQDN: "a.rokilai.online", SourceType: "server", SourceID: LocalServerSourceID},
		{BindingID: "b-old", FQDN: "b.rokilai.online", SourceType: "server", SourceID: LocalServerSourceID, ConfigState: "deleted"},
		{BindingID: "b", FQDN: "b.rokilai.online", SourceType: "server", SourceID: LocalServerSourceID},
		{BindingID: "device", FQDN: "c.rokilai.online", SourceType: "device", SourceID: "device-1"},
	} {
		binding.Revision = 1
		if binding.ConfigState == "" {
			binding.ConfigState = "observing"
		}
		binding.RuntimeState = "waiting_report"
		snapshot.Bindings[binding.BindingID] = &binding
	}
	return snapshot
}

func assertDomainOrder(t *testing.T, service *Service) {
	t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, binding := range bindings {
			ids = append(ids, binding.BindingID)
		}
		if !reflect.DeepEqual(ids, []string{"a", "b", "z"}) {
			t.Fatalf("attempt %d: ids=%v", attempt, ids)
		}
		for _, binding := range bindings {
			if binding.ConfigState == ConfigDeleted || binding.SourceType != SourceServer || binding.SourceID != LocalServerSourceID {
				t.Fatalf("unexpected binding: %+v", binding)
			}
		}
		devices, err := service.List(context.Background(), SourceDevice, "device-1")
		if err != nil || len(devices) != 1 || devices[0].BindingID != "device" {
			t.Fatalf("devices=%+v err=%v", devices, err)
		}
		all, err := service.List(context.Background(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		ids = []string{}
		for _, binding := range all {
			ids = append(ids, binding.BindingID)
		}
		if !reflect.DeepEqual(ids, []string{"a", "b", "device", "z"}) {
			t.Fatalf("all=%v", ids)
		}
	}
}

func TestDomainBindingListOrderIsStableAndReadOnly(t *testing.T) {
	r := &listOrderRepository{snapshot: domainOrderSnapshot()}
	service := NewService(store.NewControlPlaneService(r), nil)
	assertDomainOrder(t, service)
	r.snapshot.Bindings["z"].UpdatedAt = time.Now().Add(time.Hour)
	r.snapshot.Bindings["z"].RuntimeState = "synced"
	before := *r.snapshot.Bindings["z"]
	assertDomainOrder(t, service)
	if r.commits != 0 || r.snapshot.Revision != 7 || !reflect.DeepEqual(before, *r.snapshot.Bindings["z"]) || len(r.snapshot.Bindings) != 5 {
		t.Fatal("list modified snapshot")
	}
	bindings, err := service.List(context.Background(), SourceDevice, "missing")
	if err != nil || bindings == nil || len(bindings) != 0 {
		t.Fatalf("empty=%v err=%v", bindings, err)
	}
	bindings, _ = service.List(context.Background(), SourceServer, LocalServerSourceID)
	bindings[0].FQDN = "changed"
	if r.snapshot.Bindings["a"].FQDN != "a.rokilai.online" {
		t.Fatal("returned value aliases snapshot")
	}
	r.err = errors.New("snapshot read failure")
	bindings, err = service.List(context.Background(), "", "")
	if !errors.Is(err, r.err) || bindings != nil || r.commits != 0 || r.snapshot.Revision != 7 {
		t.Fatalf("failure returned data or wrote state: bindings=%v err=%v", bindings, err)
	}
}

func TestDomainBindingListOrderAfterRepositoryReload(t *testing.T) {
	for _, backend := range []string{"file", "mysql"} {
		t.Run(backend, func(t *testing.T) {
			var repository store.ControlPlaneRepository
			var reopen func() store.ControlPlaneRepository
			if backend == "file" {
				path := filepath.Join(t.TempDir(), "control-plane.json")
				reopen = func() store.ControlPlaneRepository {
					r, err := filestore.OpenControlPlane(path)
					if err != nil {
						t.Fatal(err)
					}
					return r
				}
				repository = reopen()
			} else {
				dsn := os.Getenv("HOMEAGENT_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("HOMEAGENT_TEST_MYSQL_DSN must point to a dedicated local test database")
				}
				ms, err := mysqlstore.NewMySQLStore(mysqlstore.Config{DSN: dsn})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { ms.Close() })
				// Real MySQL snapshot JSON; restore the exact pre-test row, including revisions.
				var revision uint64
				var data []byte
				var updated time.Time
				err = ms.DB().QueryRow("SELECT revision, snapshot_json, updated_at FROM control_plane_snapshots WHERE id = 1").Scan(&revision, &data, &updated)
				absent := errors.Is(err, sql.ErrNoRows)
				if err != nil && !absent {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					var err error
					if absent {
						_, err = ms.DB().Exec("DELETE FROM control_plane_snapshots WHERE id = 1")
					} else {
						_, err = ms.DB().Exec("UPDATE control_plane_snapshots SET revision = ?, snapshot_json = ?, updated_at = ? WHERE id = 1", revision, data, updated)
					}
					if err != nil {
						t.Error(err)
					}
				})
				repository = ms.ControlPlaneRepository()
				reopen = func() store.ControlPlaneRepository {
					fresh, err := mysqlstore.NewMySQLStore(mysqlstore.Config{DSN: dsn})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { fresh.Close() })
					return fresh.ControlPlaneRepository()
				}
			}
			current, err := repository.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repository.Commit(context.Background(), current.Revision, domainOrderSnapshot()); err != nil {
				t.Fatal(err)
			}
			assertDomainOrder(t, NewService(store.NewControlPlaneService(repository), nil))
			reloaded := reopen()
			before, err := reloaded.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			assertDomainOrder(t, NewService(store.NewControlPlaneService(reloaded), nil))
			after, err := reloaded.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("list changed persisted snapshot")
			}
		})
	}
}

// Duplicate active FQDNs are rejected on commit. This read-only fixture checks
// the documented secondary key if an older snapshot supplies tied names.
func TestDomainBindingListOrderUsesBindingIDForEqualFQDN(t *testing.T) {
	snapshot := store.NewControlPlaneSnapshot()
	for _, id := range []string{"binding-z", "binding-a", "binding-b"} {
		snapshot.Bindings[id] = &store.DomainBinding{BindingID: id, FQDN: "same.rokilai.online", SourceType: "server", SourceID: LocalServerSourceID, ConfigState: "observing", Revision: 1}
	}
	repository := &listOrderRepository{snapshot: snapshot}
	service := NewService(store.NewControlPlaneService(repository), nil)
	for attempt := 0; attempt < 20; attempt++ {
		bindings, err := service.List(context.Background(), SourceServer, LocalServerSourceID)
		if err != nil || len(bindings) != 3 {
			t.Fatalf("bindings=%v err=%v", bindings, err)
		}
		for i, want := range []string{"binding-a", "binding-b", "binding-z"} {
			if bindings[i].BindingID != want {
				t.Fatalf("bindings[%d]=%s", i, bindings[i].BindingID)
			}
		}
	}
	if repository.commits != 0 {
		t.Fatal("list wrote duplicate fixture")
	}
}
