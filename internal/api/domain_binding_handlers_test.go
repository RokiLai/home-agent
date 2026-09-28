package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"homeagent/internal/domainbinding"
	"homeagent/internal/store"
	"homeagent/internal/store/filestore"
)

func newDomainBindingAPIServer(t *testing.T) *Server {
	t.Helper()
	repository, err := filestore.OpenControlPlane(filepath.Join(t.TempDir(), "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	control := store.NewControlPlaneService(repository)
	return &Server{DomainBindings: domainbinding.NewService(control, nil), DomainBindingConfig: domainbinding.ConfigResult{State: domainbinding.ConfigUnconfigured}}
}

func TestServerDomainBindingPreflightIsReadOnlyAndCreateObservesExistingRecord(t *testing.T) {
	server := newDomainBindingAPIServer(t)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/server/domain-bindings/preflight", bytes.NewBufferString(`{"fqdn":"Srv.Rokilai.Online."}`))
	response := httptest.NewRecorder()
	server.preflightServerDomainBinding(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("preflight status=%d body=%s", response.Code, response.Body.String())
	}
	var preflight map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &preflight); err != nil {
		t.Fatal(err)
	}
	if preflight["fqdn"] != "srv.rokilai.online" || preflight["writes_dns"] != false {
		t.Fatalf("preflight=%v", preflight)
	}
	bindings, err := server.DomainBindings.List(context.Background(), "", "")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("preflight persisted bindings=%v err=%v", bindings, err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/server/domain-bindings", bytes.NewBufferString(`{"fqdn":"srv.rokilai.online","expected_revision":0,"existing_record":true}`))
	response = httptest.NewRecorder()
	server.createServerDomainBinding(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var binding domainbinding.Binding
	if err := json.Unmarshal(response.Body.Bytes(), &binding); err != nil {
		t.Fatal(err)
	}
	if binding.ConfigState != domainbinding.ConfigObserving || binding.RuntimeState != domainbinding.RuntimeWaitingReport {
		t.Fatalf("binding=%+v", binding)
	}
}

func TestCreateServerDomainBindingReturnsConflictForStaleControlPlaneRevision(t *testing.T) {
	server := newDomainBindingAPIServer(t)
	if _, err := server.DomainBindings.Create(context.Background(), domainbinding.CreateCommand{
		SourceType:       domainbinding.SourceServer,
		SourceID:         domainbinding.LocalServerSourceID,
		FQDN:             "first.rokilai.online",
		ExpectedRevision: 0,
		ExistingRecord:   true,
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/server/domain-bindings", bytes.NewBufferString(`{"fqdn":"second.rokilai.online","expected_revision":0,"existing_record":true}`))
	response := httptest.NewRecorder()
	server.createServerDomainBinding(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale create status=%d body=%s", response.Code, response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "conflict" {
		t.Fatalf("stale create body=%v", body)
	}
	bindings, err := server.DomainBindings.List(context.Background(), domainbinding.SourceServer, domainbinding.LocalServerSourceID)
	if err != nil || len(bindings) != 1 || bindings[0].FQDN != "first.rokilai.online" {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
}

func TestScopedDomainBindingTransitionsRejectIDORAndPreserveDNSContract(t *testing.T) {
	server := newDomainBindingAPIServer(t)
	binding, err := server.DomainBindings.Create(context.Background(), domainbinding.CreateCommand{SourceType: domainbinding.SourceDevice, SourceID: "device-1", FQDN: "device.rokilai.online", ExpectedRevision: 0, ExistingRecord: true})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/devices/device-2/domain-bindings/x/enable", bytes.NewBufferString(`{"expected_revision":1,"publisher_disabled_confirmed":true}`))
	request.SetPathValue("id", "device-2")
	request.SetPathValue("binding_id", binding.BindingID)
	response := httptest.NewRecorder()
	server.enableDeviceDomainBinding(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-device enable status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/devices/device-1/domain-bindings/x/enable", bytes.NewBufferString(`{"expected_revision":1,"publisher_disabled_confirmed":false}`))
	request.SetPathValue("id", "device-1")
	request.SetPathValue("binding_id", binding.BindingID)
	response = httptest.NewRecorder()
	server.enableDeviceDomainBinding(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("unconfirmed enable status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/devices/device-1/domain-bindings/x/enable", bytes.NewBufferString(`{"expected_revision":1,"publisher_disabled_confirmed":true}`))
	request.SetPathValue("id", "device-1")
	request.SetPathValue("binding_id", binding.BindingID)
	response = httptest.NewRecorder()
	server.enableDeviceDomainBinding(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("enable status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/devices/device-1/domain-bindings/x/disable", bytes.NewBufferString(`{"expected_revision":2}`))
	request.SetPathValue("id", "device-1")
	request.SetPathValue("binding_id", binding.BindingID)
	response = httptest.NewRecorder()
	server.disableDeviceDomainBinding(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/v1/devices/device-1/domain-bindings/x", bytes.NewBufferString(`{"expected_revision":3}`))
	request.SetPathValue("id", "device-1")
	request.SetPathValue("binding_id", binding.BindingID)
	response = httptest.NewRecorder()
	server.deleteDeviceDomainBinding(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	bindings, err := server.DomainBindings.List(context.Background(), domainbinding.SourceDevice, "device-1")
	if err != nil || len(bindings) != 0 {
		t.Fatalf("deleted bindings=%+v err=%v", bindings, err)
	}
}

func TestDomainBindingHandlersRejectInvalidRequestsAndUnavailableService(t *testing.T) {
	server := &Server{}
	response := httptest.NewRecorder()
	server.listServerDomainBindings(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d", response.Code)
	}

	server = newDomainBindingAPIServer(t)
	response = httptest.NewRecorder()
	server.preflightServerDomainBinding(response, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"fqdn":"fake-rokilai.online"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid fqdn status=%d", response.Code)
	}
	response = httptest.NewRecorder()
	server.createServerDomainBinding(response, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"unknown":true}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d", response.Code)
	}
}
