package daytona

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acksell/clank/internal/agent"
)

// fakeDaytona is a tiny in-memory stand-in for Daytona's control-plane
// API. It tracks created/deleted sandbox IDs so tests can assert the
// launcher's lifecycle behavior without hitting the real service.
type fakeDaytona struct {
	t        *testing.T
	srv      *httptest.Server
	apiKey   string
	pollsBe4 atomic.Int32 // pretend the sandbox spends N polls in "Pending"

	mu       sync.Mutex
	sandboxes map[string]string // id -> state
	deleted   []string
}

func newFakeDaytona(t *testing.T, apiKey string, pendingPolls int) *fakeDaytona {
	f := &fakeDaytona{t: t, apiKey: apiKey, sandboxes: map[string]string{}}
	f.pollsBe4.Store(int32(pendingPolls))
	mux := http.NewServeMux()

	// POST /sandbox
	mux.HandleFunc("POST /sandbox", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", 401)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		// Minimal validation: image must be present, env must be a map.
		if _, ok := body["image"].(string); !ok {
			http.Error(w, "image required", 400)
			return
		}
		if _, ok := body["env"].(map[string]any); !ok {
			http.Error(w, "env required", 400)
			return
		}
		id := "sb-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
		f.mu.Lock()
		f.sandboxes[id] = "creating"
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "state": "creating"})
	})

	// GET /sandbox/{id} — Daytona returns lowercase state values
	// per https://www.daytona.io/docs/en/typescript-sdk/sandbox/.
	mux.HandleFunc("GET /sandbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", 401)
			return
		}
		id := r.PathValue("id")
		f.mu.Lock()
		state, ok := f.sandboxes[id]
		// Flip to "started" once we've served the configured number of
		// transitional polls.
		if ok && state == "creating" && f.pollsBe4.Add(-1) <= 0 {
			f.sandboxes[id] = "started"
			state = "started"
		}
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "state": state})
	})

	// GET /sandbox/{id}/ports/{port}/preview-url — official Daytona
	// path per https://www.daytona.io/docs/en/preview/.
	mux.HandleFunc("GET /sandbox/{id}/ports/{port}/preview-url", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", 401)
			return
		}
		id := r.PathValue("id")
		port := r.PathValue("port")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"url":   "https://" + port + "-" + id + ".preview.daytona.app",
			"token": "preview-tkn-" + id,
		})
	})

	// DELETE /sandbox/{id}
	mux.HandleFunc("DELETE /sandbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiKey {
			http.Error(w, "unauthorized", 401)
			return
		}
		id := r.PathValue("id")
		f.mu.Lock()
		delete(f.sandboxes, id)
		f.deleted = append(f.deleted, id)
		f.mu.Unlock()
		w.WriteHeader(204)
	})

	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeDaytona) Close() { f.srv.Close() }

func TestDaytona_LaunchAndStop(t *testing.T) {
	fake := newFakeDaytona(t, "test-key", 1) // 1 poll spent in Pending
	t.Cleanup(fake.Close)

	launcher, err := New(Options{
		APIKey:           "test-key",
		HubBaseURL:       "http://hub.example",
		HubAuthToken:     "hub-tkn",
		BaseURL:          fake.srv.URL,
		Image:            "ghcr.io/test/image:latest",
		ProvisionTimeout: 5 * time.Second,
		ExtraEnv:         map[string]string{"ANTHROPIC_API_KEY": "key", "EMPTY": ""},
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	name, client, err := launcher.Launch(ctx, agent.LaunchHostSpec{Provider: "daytona"})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if !strings.HasPrefix(string(name), "daytona-") {
		t.Errorf("hostname should start with daytona-: %q", name)
	}
	if client == nil {
		t.Fatal("client is nil")
	}

	// Stop deletes every created sandbox.
	launcher.Stop()
	fake.mu.Lock()
	deleted := append([]string(nil), fake.deleted...)
	remaining := len(fake.sandboxes)
	fake.mu.Unlock()
	if len(deleted) != 1 {
		t.Errorf("want 1 deletion on Stop, got %d (%v)", len(deleted), deleted)
	}
	if remaining != 0 {
		t.Errorf("want 0 sandboxes remaining, got %d", remaining)
	}
}

func TestDaytona_FailsFastOnMissingOptions(t *testing.T) {
	cases := []Options{
		{HubBaseURL: "http://h", HubAuthToken: "t"},                // missing APIKey
		{APIKey: "k", HubAuthToken: "t"},                           // missing HubBaseURL
		{APIKey: "k", HubBaseURL: "http://h"},                      // missing HubAuthToken
	}
	for i, c := range cases {
		if _, err := New(c, nil); err == nil {
			t.Errorf("case %d: want error, got nil", i)
		}
	}
}

func TestDaytona_PreviewTokenInjectedInTransport(t *testing.T) {
	// Verify that the RoundTripper added by Launch attaches the
	// x-daytona-preview-token header to the host client's outgoing
	// requests. We don't actually invoke the host API; we capture the
	// header via a stand-in handler.
	var got string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-daytona-preview-token")
		w.WriteHeader(204)
	}))
	t.Cleanup(origin.Close)

	transport := &previewTokenInjector{token: "the-token"}
	cli := &http.Client{Transport: transport}
	resp, err := cli.Get(origin.URL + "/anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if got != "the-token" {
		t.Errorf("token header missing or wrong: %q", got)
	}
}
