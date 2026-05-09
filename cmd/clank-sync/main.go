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
//	CLANK_SYNC_DB_PATH      — SQLite path for the HostStore + sync tables
//	                           (default ~/.local/state/clank/clank.db)
//	SPRITES_TOKEN           — sprites API token (required)
//	SPRITES_ORG             — optional Sprites org slug
//	SPRITES_REGION          — optional Sprites region
//	MIRROR_BASE_URL         — placeholder (provisioner validates non-empty)
//	MIRROR_AUTH_TOKEN       — placeholder
//	CLANK_SYNC_DEBOUNCE     — Go duration; default "30s"
//
// Object-storage backend (S3-compatible — AWS S3, R2, Tigris, MinIO).
// All four are required to enable the new /v1/worktrees + /v1/checkpoints
// endpoints; if any are unset, only the legacy /v1/bundles path is served.
//
//	CLANK_SYNC_S3_BUCKET     — bucket name
//	CLANK_SYNC_S3_REGION     — region (use "auto" for R2)
//	CLANK_SYNC_S3_ENDPOINT   — optional; set for R2/Tigris/MinIO
//	CLANK_SYNC_S3_ACCESS_KEY — access key
//	CLANK_SYNC_S3_SECRET_KEY — secret key
//	CLANK_SYNC_S3_PATH_STYLE — "1" / "true" for path-style addressing
//	                            (required for MinIO and most R2 setups)
//	CLANK_SYNC_PRESIGN_TTL   — Go duration; default "5m"
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
	"strconv"
	"syscall"
	"time"

	"github.com/acksell/clank/internal/store"
	clankflyio "github.com/acksell/clank/pkg/provisioner/flyio"
	clanksync "github.com/acksell/clank/pkg/sync"
	"github.com/acksell/clank/pkg/sync/storage"
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

	syncCfg := clanksync.Config{
		Provisioner:   flyProv,
		FlushDebounce: debounce,
	}

	// S3-compatible object storage backs the new checkpoint substrate.
	// All four credentials must be set together — partial config is a
	// fatal error rather than a silent fallback to legacy-only mode.
	s3Cfg, ok := loadS3Config()
	if ok {
		ctxInit, cancelInit := context.WithTimeout(context.Background(), 30*time.Second)
		bucket, err := storage.NewS3(ctxInit, s3Cfg)
		cancelInit()
		if err != nil {
			log.Fatalf("storage S3: %v", err)
		}
		syncCfg.Store = st
		syncCfg.Storage = bucket
		if ttl := os.Getenv("CLANK_SYNC_PRESIGN_TTL"); ttl != "" {
			parsed, err := time.ParseDuration(ttl)
			if err != nil {
				log.Fatalf("CLANK_SYNC_PRESIGN_TTL: %v", err)
			}
			syncCfg.PresignTTL = parsed
		}
		log.Printf("clank-sync: checkpoint endpoints enabled (bucket=%s region=%s)", s3Cfg.Bucket, s3Cfg.Region)
	} else {
		log.Printf("clank-sync: S3 not configured; only legacy /v1/bundles path is served")
	}

	srv, err := clanksync.NewServer(syncCfg, log.Default())
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

// loadS3Config reads CLANK_SYNC_S3_* env vars. Returns (cfg, true) when
// the four required credentials are all set, or (cfg, false) when none
// are set. A partial config is a fatal error so half-configured deploys
// can't silently fall back to legacy-only mode.
func loadS3Config() (storage.S3Config, bool) {
	bucket := os.Getenv("CLANK_SYNC_S3_BUCKET")
	region := os.Getenv("CLANK_SYNC_S3_REGION")
	access := os.Getenv("CLANK_SYNC_S3_ACCESS_KEY")
	secret := os.Getenv("CLANK_SYNC_S3_SECRET_KEY")
	allEmpty := bucket == "" && region == "" && access == "" && secret == ""
	if allEmpty {
		return storage.S3Config{}, false
	}
	if bucket == "" || region == "" || access == "" || secret == "" {
		log.Fatalf("CLANK_SYNC_S3_* env vars are partially set; need all of BUCKET, REGION, ACCESS_KEY, SECRET_KEY (or none)")
	}
	pathStyle := false
	if v := os.Getenv("CLANK_SYNC_S3_PATH_STYLE"); v != "" {
		parsed, err := strconv.ParseBool(v)
		if err != nil {
			log.Fatalf("CLANK_SYNC_S3_PATH_STYLE: %v", err)
		}
		pathStyle = parsed
	}
	return storage.S3Config{
		Bucket:       bucket,
		Region:       region,
		Endpoint:     os.Getenv("CLANK_SYNC_S3_ENDPOINT"),
		AccessKey:    access,
		SecretKey:    secret,
		UsePathStyle: pathStyle,
	}, true
}
