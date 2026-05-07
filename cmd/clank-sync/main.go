// clank-sync is the cloud sync middleware: it accepts streamed git
// bundles from laptops and flushes them (debounced) into per-user
// sandboxes via clank's Provisioner. The sandbox volume is the
// persistence layer; this process is just a buffer in front of it.
//
// Single Fly app. Stateless w.r.t. long-term storage — restart drops
// in-flight bundles, which laptops re-push via syncclient's outbox.
//
// Env (laptop dev / single-tenant deployment):
//
//	CLANK_SYNC_LISTEN_ADDR  — HTTP listen (default ":8081")
//	CLANK_SYNC_DB_PATH      — SQLite path for the HostStore the
//	                           provisioner uses (default
//	                           ~/.local/state/clank/clank.db)
//	SPRITES_TOKEN           — sprites API token (required)
//	SPRITES_ORG             — optional Sprites org slug
//	SPRITES_REGION          — optional Sprites region
//	MIRROR_BASE_URL         — placeholder (provisioner validates non-empty)
//	MIRROR_AUTH_TOKEN       — placeholder
//	CLANK_SYNC_DEBOUNCE     — Go duration; default "30s"
//
// For multi-tenant cloud deployments, run this binary with the
// same env your cloud gateway uses; auth defaults to permissive (PR follow-up:
// add a Supabase verifier flag here, or wrap pkg/sync.Server
// with its own auth layer in a separate binary).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/acksell/clank/internal/store"
	clankflyio "github.com/acksell/clank/pkg/provisioner/flyio"
	clanksync "github.com/acksell/clank/pkg/sync"
)

const defaultListenAddr = ":8081"

func main() {
	addr := envOrDefault("CLANK_SYNC_LISTEN_ADDR", defaultListenAddr)
	spritesToken := mustEnv("SPRITES_TOKEN")

	dbPath := os.Getenv("CLANK_SYNC_DB_PATH")
	if dbPath == "" {
		var err error
		dbPath, err = defaultDBPath()
		if err != nil {
			log.Fatalf("default db path: %v", err)
		}
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("open store %s: %v", dbPath, err)
	}
	defer st.Close()

	flyOpts := clankflyio.Options{
		APIToken:         spritesToken,
		OrganizationSlug: os.Getenv("SPRITES_ORG"),
		Region:           os.Getenv("SPRITES_REGION"),
		MirrorBaseURL:    envOrDefault("MIRROR_BASE_URL", "https://mirror.placeholder.invalid"),
		MirrorAuthToken:  envOrDefault("MIRROR_AUTH_TOKEN", "phase-c-placeholder"),
	}
	flyProv, err := clankflyio.New(flyOpts, st, log.Default())
	if err != nil {
		log.Fatalf("flyio provisioner: %v", err)
	}

	debounce := 30 * time.Second
	if d := os.Getenv("CLANK_SYNC_DEBOUNCE"); d != "" {
		parsed, err := time.ParseDuration(d)
		if err != nil {
			log.Fatalf("CLANK_SYNC_DEBOUNCE: %v", err)
		}
		debounce = parsed
	}

	srv, err := clanksync.NewServer(clanksync.Config{
		Provisioner:   flyProv,
		FlushDebounce: debounce,
	}, log.Default())
	if err != nil {
		log.Fatalf("sync server: %v", err)
	}
	defer srv.Stop()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("clank-sync listening on %s", addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func defaultDBPath() (string, error) {
	state, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(state, "clank")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "clank.db"), nil
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s must be set", key)
	}
	return v
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
