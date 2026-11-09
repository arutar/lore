package httpapi

import (
	"net/http"
	"testing"
)

func TestGlobalThemeSetting(t *testing.T) {
	client, _ := newTestClient(t)
	client.login()
	status, _ := client.request(http.MethodPatch, "/web/v1/admin/settings", map[string]any{"registrationEnabled": false, "theme": "ocean"}, true)
	if status != http.StatusOK {
		t.Fatalf("theme update status = %d", status)
	}
	status, config := client.request(http.MethodGet, "/web/v1/config", nil, false)
	if status != http.StatusOK || config["theme"] != "ocean" {
		t.Fatalf("public theme = %v, status = %d", config["theme"], status)
	}
	status, _ = client.request(http.MethodPatch, "/web/v1/admin/settings", map[string]any{"registrationEnabled": false, "theme": "unknown"}, true)
	if status != http.StatusBadRequest {
		t.Fatalf("invalid theme status = %d", status)
	}
}
