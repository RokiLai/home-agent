package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"homeagent/internal/devicestate"
	"homeagent/internal/networkaddr"
	"homeagent/internal/prefixstate"
)

func TestDeviceNetworkSelection(t *testing.T) {
	for _, tc := range []struct {
		name, old, want      string
		addresses            []networkaddr.ReportedIPv6Address
		router, stale, empty bool
	}{
		{name: "replace obsolete address", old: "240e:1::1", want: "240e:2::2", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}}},
		{name: "retain valid address", old: "240e:2::2", want: "240e:2::2", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::1"}, {Address: "240e:2::2"}}},
		{name: "empty candidates", old: "240e:1::1"},
		{name: "reject temporary", old: "240e:1::1", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2", Temporary: true}}},
		{name: "no prefix intersection", router: true, old: "240e:1::1", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:3::3"}}},
		{name: "reject deprecated", old: "240e:1::1", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2", Deprecated: true}}},
		{name: "active prefix", router: true, old: "240e:1::1", want: "240e:2::2", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}, {Address: "240e:1::2"}}},
		{name: "stale prefix", router: true, stale: true, old: "240e:1::1", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}}},
		{name: "empty prefix", router: true, empty: true, old: "240e:1::1", addresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOMEAGENT_DDNS_ROUTER_ID", "")
			now := time.Now().UTC()
			states := devicestate.NewService(nil)
			if err := states.Save(devicestate.DeviceIPv6State{DeviceID: "d", NetworkID: "home", Revision: 1, DesiredAddress: tc.old}); err != nil {
				t.Fatal(err)
			}
			prefixes := prefixstate.NewMemoryStore()
			if tc.router {
				seen := now
				if tc.stale {
					seen = now.Add(-time.Hour)
				}
				p := []prefixstate.ReportedIPv6Prefix{{Prefix: "240e:2::/64"}}
				if tc.empty {
					p = nil
				}
				if err := prefixes.Save(prefixstate.RouterPrefixState{NetworkID: "home", RouterDeviceID: "r", LastSeenAt: seen, Prefixes: p}); err != nil {
					t.Fatal(err)
				}
			}
			s := &Server{DeviceStateService: states, PrefixStateService: prefixstate.NewService(prefixes)}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("id", "d"); s.putDeviceNetworkState(w, r) }))
			defer server.Close()
			body, _ := json.Marshal(map[string]any{"network_id": "home", "revision": 2, "observed_at": now, "ipv6_addresses": tc.addresses})
			req, _ := http.NewRequest(http.MethodPut, server.URL, bytes.NewReader(body))
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d", resp.StatusCode)
			}
			state, err := states.Get("d")
			if err != nil {
				t.Fatal(err)
			}
			if state.DesiredAddress != tc.want {
				t.Fatalf("desired=%q want=%q", state.DesiredAddress, tc.want)
			}
			// Rejected old snapshots must not replace either candidates or the selected address.
			body, _ = json.Marshal(map[string]any{"network_id": "home", "revision": 1, "observed_at": now, "ipv6_addresses": []networkaddr.ReportedIPv6Address{{Address: "240e:9::9"}}})
			req, _ = http.NewRequest(http.MethodPut, server.URL, bytes.NewReader(body))
			resp2, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp2.Body.Close()
			if resp2.StatusCode != 409 {
				t.Fatalf("old revision status %d", resp2.StatusCode)
			}
			after, _ := states.Get("d")
			if after.Revision != 2 || after.DesiredAddress != tc.want {
				t.Fatalf("rejected report changed state: %+v", after)
			}
		})
	}
}

func TestDeviceSelectionConfiguredRouterMissingAndNetworkIsolation(t *testing.T) {
	t.Setenv("HOMEAGENT_DDNS_ROUTER_ID", "r")
	t.Setenv("HOMEAGENT_DDNS_NETWORK_ID", "home")
	s := &Server{PrefixStateService: prefixstate.NewService(nil)}
	for _, tc := range []struct{ network, want string }{{"home", ""}, {"other", "240e:2::2"}} {
		st := devicestate.DeviceIPv6State{NetworkID: tc.network, DesiredAddress: "240e:1::1", ReportedAddresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}}}
		if err := s.selectDeviceDesiredAddress(&st); err != nil {
			t.Fatal(err)
		}
		if st.DesiredAddress != tc.want {
			t.Fatalf("network=%s desired=%s", tc.network, st.DesiredAddress)
		}
	}
}

func TestDeviceSelectionConcurrentReportsAndRestart(t *testing.T) {
	t.Setenv("HOMEAGENT_DDNS_ROUTER_ID", "")
	path := filepath.Join(t.TempDir(), "network.json")
	repository, err := devicestate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := devicestate.NewService(repository)
	s := &Server{DeviceStateService: svc, PrefixStateService: prefixstate.NewService(nil)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("id", "d"); s.putDeviceNetworkState(w, r) }))
	defer server.Close()
	var wg sync.WaitGroup
	for revision := 1; revision <= 20; revision++ {
		wg.Add(1)
		go func(revision int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]any{"network_id": "home", "revision": revision, "observed_at": time.Now().UTC(), "ipv6_addresses": []networkaddr.ReportedIPv6Address{{Address: fmt.Sprintf("240e:2::%x", revision)}}})
			req, _ := http.NewRequest(http.MethodPut, server.URL, bytes.NewReader(body))
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			if resp.StatusCode != 200 && resp.StatusCode != 409 {
				t.Errorf("status %d", resp.StatusCode)
			}
		}(revision)
	}
	wg.Wait()
	reopened, err := devicestate.OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := reopened.Get("d")
	if err != nil {
		t.Fatal(err)
	}
	if st.Revision != 20 || st.DesiredAddress != "240e:2::14" {
		t.Fatalf("recovered %+v", st)
	}
	// A heartbeat with unchanged candidates repairs a legacy desired address.
	st.DesiredAddress = "240e:1::1"
	if err := reopened.Save(*st); err != nil {
		t.Fatal(err)
	}
	s.DeviceStateService = devicestate.NewService(reopened)
	body, _ := json.Marshal(map[string]any{"network_id": "home", "revision": 21, "observed_at": time.Now().UTC(), "ipv6_addresses": st.ReportedAddresses})
	req, _ := http.NewRequest(http.MethodPut, server.URL, bytes.NewReader(body))
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	after, _ := reopened.Get("d")
	if after.DesiredAddress != "240e:2::14" {
		t.Fatalf("heartbeat did not recover: %+v", after)
	}
}

func TestRouterUpdateClearsDesiredAddressWithoutTouchingOtherNetwork(t *testing.T) {
	states := devicestate.NewService(nil)
	for _, id := range []string{"home", "other"} {
		if err := states.Save(devicestate.DeviceIPv6State{DeviceID: id, NetworkID: id, Revision: 4, DesiredAddress: "240e:2::2", ReportedAddresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}}}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{DeviceStateService: states, PrefixStateService: prefixstate.NewService(nil)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("id", "r"); s.putRouterPrefixes(w, r) }))
	defer server.Close()
	body, _ := json.Marshal(map[string]any{"network_id": "home", "revision": 1, "observed_at": time.Now().UTC(), "prefixes": []prefixstate.ReportedIPv6Prefix{}})
	req, _ := http.NewRequest(http.MethodPut, server.URL, bytes.NewReader(body))
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	home, _ := states.Get("home")
	other, _ := states.Get("other")
	if home.DesiredAddress != "" || home.Revision != 4 || other.DesiredAddress != "240e:2::2" {
		t.Fatalf("home=%+v other=%+v", home, other)
	}
}

type failingSelectionPrefixes struct{}

func (failingSelectionPrefixes) GetByNetwork(string) (*prefixstate.RouterPrefixState, error) {
	return nil, errors.New("storage unavailable")
}
func (failingSelectionPrefixes) Save(prefixstate.RouterPrefixState) error {
	return errors.New("storage unavailable")
}
func (failingSelectionPrefixes) List() ([]prefixstate.RouterPrefixState, error) {
	return nil, errors.New("storage unavailable")
}
func TestDeviceSelectionPrefixReadFailureDoesNotFallback(t *testing.T) {
	s := &Server{PrefixStateService: prefixstate.NewService(failingSelectionPrefixes{})}
	st := devicestate.DeviceIPv6State{NetworkID: "home", ReportedAddresses: []networkaddr.ReportedIPv6Address{{Address: "240e:2::2"}}}
	if err := s.selectDeviceDesiredAddress(&st); err == nil {
		t.Fatal("storage failure accepted")
	}
	if st.DesiredAddress != "" {
		t.Fatal("storage failure selected unverified address")
	}
}

func TestConcurrentRouterAndDeviceReportsKeepLatestRevision(t *testing.T) {
	t.Setenv("HOMEAGENT_DDNS_ROUTER_ID", "r")
	t.Setenv("HOMEAGENT_DDNS_NETWORK_ID", "home")
	states := devicestate.NewService(nil)
	s := &Server{DeviceStateService: states, PrefixStateService: prefixstate.NewService(nil)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/prefixes" {
			r.SetPathValue("id", "r")
			s.putRouterPrefixes(w, r)
		} else {
			r.SetPathValue("id", "d")
			s.putDeviceNetworkState(w, r)
		}
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for revision := 1; revision <= 10; revision++ {
		for _, prefix := range []bool{false, true} {
			wg.Add(1)
			go func(revision int, prefix bool) {
				defer wg.Done()
				path := "/device"
				payload := map[string]any{"network_id": "home", "revision": revision, "observed_at": time.Now().UTC()}
				if prefix {
					path = "/prefixes"
					payload["prefixes"] = []prefixstate.ReportedIPv6Prefix{{Prefix: "240e:2::/64"}}
				} else {
					payload["ipv6_addresses"] = []networkaddr.ReportedIPv6Address{{Address: fmt.Sprintf("240e:2::%x", revision)}}
				}
				body, _ := json.Marshal(payload)
				req, _ := http.NewRequest(http.MethodPut, server.URL+path, bytes.NewReader(body))
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
				if resp.StatusCode != 200 && resp.StatusCode != 409 {
					t.Errorf("status %d", resp.StatusCode)
				}
			}(revision, prefix)
		}
	}
	wg.Wait()
	state, err := states.Get("d")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 10 || state.DesiredAddress != "240e:2::a" {
		t.Fatalf("final %+v", state)
	}
}
