package hostmux_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/host"
	hostmux "github.com/acksell/clank/internal/host/mux"
)

// TestOpencodeVersionEndpoint_ReturnsBareVersion confirms the
// new GET /opencode-version mux route shells out to opencode and
// returns the bare version string. Used by the laptop CLI to drive
// the version-skew check in clankcli.assertOpencodeCompatible.
func TestOpencodeVersionEndpoint_ReturnsBareVersion(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode binary not on $PATH")
	}

	svc := host.New(host.Options{
		BackendManagers: map[agent.BackendType]agent.BackendManager{
			agent.BackendOpenCode: &noopBackendManager{},
		},
	})
	t.Cleanup(svc.Shutdown)

	mux := hostmux.New(svc, nil)
	srv := httptest.NewServer(mux.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/opencode-version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Version == "" {
		t.Errorf("empty version in response")
	}
	// Loose sanity check: opencode versions look like "1.x.y" or "2.x.y".
	if got.Version[0] < '0' || got.Version[0] > '9' {
		t.Errorf("version %q doesn't start with a digit", got.Version)
	}
}
