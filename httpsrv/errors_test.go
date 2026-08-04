package httpsrv

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/donaldgifford/booty/catalog"
)

// brokenCatalog selects every machine but names a profile that does not exist,
// so Match returns catalog.ErrUnknownProfile rather than ErrNoMatch.
func brokenCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Profiles: map[string]catalog.Profile{},
		Groups: []catalog.Group{
			{Name: "everything", Profile: "missing-profile"},
		},
	}
}

// TestBrokenCatalogIsNotReportedAsUnknownMachine covers the branches added when
// catalog.ErrUnknownProfile was introduced. A group that selects the machine but
// names a profile the catalog never defines is a server-side fault, and telling
// the operator to add a group — which is what every endpoint used to do — sends
// them to fix the one thing that is not broken.
func TestBrokenCatalogIsNotReportedAsUnknownMachine(t *testing.T) {
	h := newTestServer(t, Config{Catalog: brokenCatalog()})

	t.Run("ipxe names the real fault", func(t *testing.T) {
		body := get(t, h, "/ipxe?mac=d0:50:99:b3:4c:50&arch=x86_64").Body.String()
		if !strings.HasPrefix(body, "#!ipxe") {
			t.Fatalf("reply is not an iPXE script: %q", body)
		}
		if strings.Contains(body, "Add a group for this machine") {
			t.Errorf("broken catalog reported as an unknown machine: %q", body)
		}
		if !strings.Contains(body, "missing-profile") {
			t.Errorf("reply does not name the missing profile: %q", body)
		}
	})

	t.Run("machine-config is a server fault, not a 404", func(t *testing.T) {
		rec := get(t, h, "/machine-config?mac=d0:50:99:b3:4c:50&arch=x86_64")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500 (the machine is fine, the catalog is not)", rec.Code)
		}
	})

	t.Run("cloud-init is a server fault, not a 404", func(t *testing.T) {
		rec := get(t, h, "/cloud-init/meta-data")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

// TestProxmoxRejectsInjectedIdentity covers the POST-body half of the identity
// guard. The Proxmox installer supplies its DMI strings and NIC MACs in the
// request body, which is no more trustworthy than a query string, and the
// endpoint requires no token unless one is configured.
func TestProxmoxRejectsInjectedIdentity(t *testing.T) {
	h := newTestServer(t, Config{Catalog: bootCatalog()})

	post := func(t *testing.T, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/proxmox/answer", strings.NewReader(body))
		h.ServeHTTP(rec, req)
		return rec
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "newline in a NIC MAC",
			body: `{"dmi":{"system":{"uuid":"u","serial":"s","name":"n","manufacturer":"m"}},` +
				`"network_interfaces":[{"name":"eth0","mac":"d0:50:99:b3:4c:50\nroot_ssh_keys = [\"attacker\"]"}]}`,
		},
		{
			name: "newline in a DMI string",
			body: `{"dmi":{"system":{"uuid":"u","serial":"s\nroot_ssh_keys = [\"attacker\"]",` +
				`"name":"n","manufacturer":"m"}},"network_interfaces":[{"name":"eth0","mac":"d0:50:99:b3:4c:50"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := post(t, tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if strings.Contains(rec.Body.String(), "attacker") {
				t.Errorf("response echoed the injected payload: %q", rec.Body.String())
			}
		})
	}
}
