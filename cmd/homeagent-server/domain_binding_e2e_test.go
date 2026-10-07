package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"homeagent/internal/api"
	"homeagent/internal/device"
	"homeagent/internal/devicestate"
	"homeagent/internal/domainbinding"
	"homeagent/internal/networkaddr"
	"homeagent/internal/prefixstate"
	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
)

// This is a domain-port recorder, not a Cloudflare protocol emulator. Cloudflare
// HTTP fixtures and their real-observation provenance remain in its package.
type selectionPublisher struct {
	requests []domainbinding.ApplyRequest
	fail     bool
}

func (p *selectionPublisher) Observe(context.Context, domainbinding.ObserveRequest) (domainbinding.RecordObservation, error) {
	return domainbinding.RecordObservation{}, nil
}
func (p *selectionPublisher) Apply(_ context.Context, r domainbinding.ApplyRequest) (domainbinding.RecordObservation, error) {
	p.requests = append(p.requests, r)
	if p.fail {
		return domainbinding.RecordObservation{}, errors.New("provider unavailable")
	}
	return domainbinding.RecordObservation{Exists: true, RecordID: "record", IPv6: r.DesiredIPv6, TTL: r.TTL, Proxied: r.Proxied}, nil
}

func TestReportedAddressConvergesThroughDomainBinding(t *testing.T) {
	t.Setenv("HOMEAGENT_DDNS_ROUTER_ID", "")
	ctx := context.Background()
	root := t.TempDir()
	repository, err := filestore.OpenControlPlane(filepath.Join(root, "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	control := store.NewControlPlaneService(repository)
	if err := control.ReplaceRegistryState(ctx, []device.Device{{ID: "d"}}, nil); err != nil {
		t.Fatal(err)
	}
	service := domainbinding.NewService(control, nil)
	if _, err := service.Create(ctx, domainbinding.CreateCommand{SourceType: domainbinding.SourceDevice, SourceID: "d", OwnerUserID: "legacy-admin", FQDN: "selection.rokilai.online", ExpectedRevision: 1, TTL: 120}); err != nil {
		t.Fatal(err)
	}
	states, err := devicestate.OpenFileStore(filepath.Join(root, "network.json"))
	if err != nil {
		t.Fatal(err)
	}
	stateService := devicestate.NewService(states)
	server := httptest.NewServer((&api.Server{Token: "test-token", DeviceStateService: stateService, PrefixStateService: prefixstate.NewService(nil)}).Handler())
	defer server.Close()
	provider := &selectionPublisher{}
	coordinator := domainbinding.NewCoordinator(service, provider, bindingSourceResolver{devices: stateService})
	report := func(revision int, address string) {
		t.Helper()
		candidates := []networkaddr.ReportedIPv6Address{}
		if address != "" {
			candidates = append(candidates, networkaddr.ReportedIPv6Address{Address: address})
		}
		body, _ := json.Marshal(map[string]any{"network_id": "home", "revision": revision, "observed_at": time.Now().UTC(), "ipv6_addresses": candidates})
		req, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/devices/d/network-state", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("report status %d", resp.StatusCode)
		}
	}
	check := func(want domainbinding.RuntimeState, applied string) {
		t.Helper()
		bindings, err := service.List(ctx, domainbinding.SourceDevice, "d")
		if err != nil || len(bindings) != 1 {
			t.Fatalf("bindings=%+v err=%v", bindings, err)
		}
		if bindings[0].RuntimeState != want || bindings[0].LastAppliedIPv6 != applied {
			t.Fatalf("binding=%+v", bindings[0])
		}
	}
	report(1, "240e:1::1")
	if err := coordinator.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	check(domainbinding.RuntimeSynced, "240e:1::1")
	report(2, "240e:2::2")
	if err := coordinator.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	check(domainbinding.RuntimeSynced, "240e:2::2")
	if len(provider.requests) != 2 || provider.requests[1].DesiredIPv6 != "240e:2::2" || provider.requests[1].TTL != 120 {
		t.Fatalf("requests=%+v", provider.requests)
	}
	report(3, "")
	if err := coordinator.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	check(domainbinding.RuntimeStale, "240e:2::2")
	if len(provider.requests) != 2 {
		t.Fatal("empty candidates wrote DNS")
	}
	provider.fail = true
	report(4, "240e:3::3")
	_ = coordinator.RunOnce(ctx)
	check(domainbinding.RuntimeFailed, "240e:2::2")
	recovered, err := devicestate.OpenFileStore(filepath.Join(root, "network.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := recovered.Get("d")
	if err != nil || state.DesiredAddress != "240e:3::3" || state.Revision != 4 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
