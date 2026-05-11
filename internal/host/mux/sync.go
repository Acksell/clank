package hostmux

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/acksell/clank/pkg/sync/checkpoint"
	"github.com/acksell/clank/pkg/syncclient"
)

// syncErrorCodes are the typed sentinels the gateway uses to decide
// whether a /sync/apply-from-urls failure is retryable.
const (
	syncErrURLExpired     = "url_expired"     // S3 returned 403/expired
	syncErrS3Unreachable  = "s3_unreachable"  // network error reaching object storage
	syncErrApplyFailed    = "apply_failed"    // checkpoint.Apply returned an error
	syncErrBadManifest    = "bad_manifest"    // manifest JSON couldn't be parsed
	syncErrBadRequest     = "bad_request"     // missing/invalid URL fields
)

// syncURLBundleFetchTimeout caps each bundle GET so a slow S3 doesn't
// hang the apply. The presigned URL TTL (5m) is the upper bound on the
// whole flow; one bundle should fit well inside that.
const syncURLBundleFetchTimeout = 4 * time.Minute

// handleSyncApply applies a checkpoint into a per-user working tree
// under ~/work/<repo>/. The body is multipart/form-data with three
// fields: a JSON manifest plus two bundles.
//
// Query params:
//
//	repo  — relative path under ~/work/ (e.g. "myproject"). Must be a
//	        single path segment; ".." or absolute paths are rejected to
//	        keep blast radius inside ~/work/.
//
// Multipart fields (FormFile resolves each by name; order doesn't
// matter):
//
//	"manifest"     — application/json: the checkpoint.Manifest JSON.
//	"head_commit"  — application/octet-stream: the headCommit bundle.
//	"incremental"  — application/octet-stream: the incremental bundle.
//
// After Apply, the working tree at ~/work/<repo> matches the manifest
// exactly — HEAD, branch, index, and untracked files all restored.
//
// Returns 204 on success. Errors are JSON {code, error}.
func (m *Mux) handleSyncApply(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if !validRepoSlug(repo) {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_repo", Error: "repo must be a single non-empty path segment without '..' or '/'"})
		return
	}

	workDir, err := workRoot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "work_root", Error: err.Error()})
		return
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "mkdir_work", Error: err.Error()})
		return
	}
	target := filepath.Join(workDir, repo)

	// 256 MiB cap — large for typical edits but bounded so a malicious
	// client can't fill the disk via in-memory parts.
	if err := r.ParseMultipartForm(256 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_multipart", Error: err.Error()})
		return
	}

	manifestPart, _, err := r.FormFile("manifest")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "missing_manifest", Error: err.Error()})
		return
	}
	defer manifestPart.Close()
	manifestBytes, err := io.ReadAll(io.LimitReader(manifestPart, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "read_manifest", Error: err.Error()})
		return
	}
	var manifest checkpoint.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "parse_manifest", Error: err.Error()})
		return
	}

	headPart, _, err := r.FormFile("head_commit")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "missing_head_commit", Error: err.Error()})
		return
	}
	defer headPart.Close()

	incrPart, _, err := r.FormFile("incremental")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "missing_incremental", Error: err.Error()})
		return
	}
	defer incrPart.Close()

	if err := checkpoint.Apply(r.Context(), target, &manifest, headPart, incrPart); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "apply_checkpoint", Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validRepoSlug(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	if strings.ContainsAny(s, "/\\") {
		return false
	}
	if strings.Contains(s, "..") {
		return false
	}
	return true
}

// workRoot resolves $HOME/work for the sandbox user. clank-host runs as
// the same user that owns the volume, so $HOME is the right anchor.
func workRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(home, "work"), nil
}

// applyFromURLsRequest is the JSON body of POST /sync/apply-from-urls.
type applyFromURLsRequest struct {
	Repo            string `json:"repo"`
	ManifestURL     string `json:"manifest_url"`
	HeadCommitURL   string `json:"head_commit_url"`
	IncrementalURL  string `json:"incremental_url"`
}

// handleSyncApplyFromURLs is the pull-based counterpart to
// handleSyncApply: instead of accepting bundle bytes in a multipart
// body, the gateway hands the sandbox presigned GET URLs for the
// checkpoint blobs and the sandbox fetches them itself from object
// storage. Bundle bytes never traverse the gateway's memory.
//
// Body (JSON):
//
//	{
//	  "repo":              "<worktree-id>",
//	  "manifest_url":      "https://s3...",
//	  "head_commit_url":   "https://s3...",
//	  "incremental_url":   "https://s3..."
//	}
//
// Returns 204 on success. Errors are JSON {code, error}; code is one
// of url_expired / s3_unreachable / apply_failed / bad_manifest /
// bad_request so the gateway can decide whether to retry.
func (m *Mux) handleSyncApplyFromURLs(w http.ResponseWriter, r *http.Request) {
	var req applyFromURLsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: syncErrBadRequest, Error: "decode body: " + err.Error()})
		return
	}
	if !validRepoSlug(req.Repo) {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_repo", Error: "repo must be a single non-empty path segment without '..' or '/'"})
		return
	}
	if req.ManifestURL == "" || req.HeadCommitURL == "" || req.IncrementalURL == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Code: syncErrBadRequest, Error: "manifest_url, head_commit_url, and incremental_url are all required"})
		return
	}

	workDir, err := workRoot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "work_root", Error: err.Error()})
		return
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "mkdir_work", Error: err.Error()})
		return
	}
	target := filepath.Join(workDir, req.Repo)

	cli := &http.Client{Timeout: syncURLBundleFetchTimeout}

	manifestBytes, status, code, err := fetchURL(r.Context(), cli, req.ManifestURL)
	if err != nil {
		writeJSON(w, status, errResp{Code: code, Error: "fetch manifest: " + err.Error()})
		return
	}
	if len(manifestBytes) > 1<<20 {
		writeJSON(w, http.StatusBadRequest, errResp{Code: syncErrBadManifest, Error: "manifest exceeds 1MiB"})
		return
	}
	var manifest checkpoint.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: syncErrBadManifest, Error: "parse manifest: " + err.Error()})
		return
	}

	headBytes, status, code, err := fetchURL(r.Context(), cli, req.HeadCommitURL)
	if err != nil {
		writeJSON(w, status, errResp{Code: code, Error: "fetch head bundle: " + err.Error()})
		return
	}
	incrBytes, status, code, err := fetchURL(r.Context(), cli, req.IncrementalURL)
	if err != nil {
		writeJSON(w, status, errResp{Code: code, Error: "fetch incremental bundle: " + err.Error()})
		return
	}

	if err := checkpoint.Apply(r.Context(), target, &manifest, bytes.NewReader(headBytes), bytes.NewReader(incrBytes)); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: syncErrApplyFailed, Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// createCheckpointRequest is the JSON body of POST /sync/checkpoint.
// The sprite reads its working tree at ~/work/<repo>, builds a
// two-bundle checkpoint, and uploads to the sync server using the
// credentials supplied in the request.
//
// The gateway is the only legitimate caller — it knows the sync
// coordinates because it's wired to the sync server in-process.
// Passing credentials per-request keeps the sprite stateless: no
// long-lived sync auth on the sprite, no provisioner-level env
// injection.
type createCheckpointRequest struct {
	Repo          string `json:"repo"`
	WorktreeID    string `json:"worktree_id"`
	HostID        string `json:"host_id"`
	SyncBaseURL   string `json:"sync_base_url"`
	SyncAuthToken string `json:"sync_auth_token"`
}

type createCheckpointResponse struct {
	CheckpointID string `json:"checkpoint_id"`
	HeadCommit   string `json:"head_commit"`
}

// handleSyncCheckpoint is the sprite-side counterpart to the laptop's
// `clank push`: build a two-bundle checkpoint of ~/work/<repo>, upload
// to S3 via presigned URLs minted by the cloud sync server, then commit.
// Returns the canonical checkpoint ID assigned by the sync server.
//
// Called by the gateway during a `to_local` migration to capture the
// sprite's latest state before handing it to the laptop.
//
// Body fields are all required: any missing field is a programming
// error in the caller (the gateway), so we 400 rather than try to
// recover.
func (m *Mux) handleSyncCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req createCheckpointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, errResp{Code: syncErrBadRequest, Error: "decode body: " + err.Error()})
		return
	}
	if !validRepoSlug(req.Repo) {
		writeJSON(w, http.StatusBadRequest, errResp{Code: "bad_repo", Error: "repo must be a single non-empty path segment without '..' or '/'"})
		return
	}
	if req.WorktreeID == "" || req.HostID == "" || req.SyncBaseURL == "" {
		writeJSON(w, http.StatusBadRequest, errResp{Code: syncErrBadRequest, Error: "worktree_id, host_id, and sync_base_url are required"})
		return
	}

	workDir, err := workRoot()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "work_root", Error: err.Error()})
		return
	}
	repoPath := filepath.Join(workDir, req.Repo)
	if _, statErr := os.Stat(repoPath); statErr != nil {
		writeJSON(w, http.StatusNotFound, errResp{Code: "missing_repo", Error: "repo not found at " + repoPath})
		return
	}

	cli, err := syncclient.New(syncclient.Config{
		BaseURL:   req.SyncBaseURL,
		AuthToken: req.SyncAuthToken,
		// The sprite identifies as its host_id in ownership records,
		// distinct from the laptop's device_id. The sync server reads
		// X-Clank-Host-Id and treats this as a sprite-kind caller.
		HostID: req.HostID,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "syncclient_init", Error: err.Error()})
		return
	}

	res, err := cli.PushCheckpoint(r.Context(), req.WorktreeID, repoPath)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, errResp{Code: "push_failed", Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, createCheckpointResponse{
		CheckpointID: res.CheckpointID,
		HeadCommit:   res.Manifest.HeadCommit,
	})
}

// fetchURL GETs blobURL and returns its body, an HTTP status code to
// return on error, and a sync-error sentinel for the gateway's
// retry-decision logic. A 403/expired from object storage maps to
// url_expired (retryable by re-minting). Dial errors map to
// s3_unreachable. Anything else falls back to apply_failed.
func fetchURL(ctx context.Context, cli *http.Client, blobURL string) (body []byte, errStatus int, errCode string, err error) {
	parsed, perr := url.Parse(blobURL)
	if perr != nil || parsed.Scheme == "" {
		return nil, http.StatusBadRequest, syncErrBadRequest, fmt.Errorf("invalid url: %v", perr)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, blobURL, nil)
	if err != nil {
		return nil, http.StatusBadRequest, syncErrBadRequest, err
	}
	resp, err := cli.Do(req)
	if err != nil {
		// netop / DNS / connection refused — gateway treats as transient.
		if _, ok := err.(net.Error); ok || strings.Contains(err.Error(), "dial ") {
			return nil, http.StatusBadGateway, syncErrS3Unreachable, err
		}
		return nil, http.StatusBadGateway, syncErrS3Unreachable, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		// S3 returns 403 on SignatureDoesNotMatch and on expired URLs;
		// either way the gateway can retry by minting fresh URLs.
		return nil, http.StatusBadGateway, syncErrURLExpired, fmt.Errorf("403 from object storage")
	}
	if resp.StatusCode/100 != 2 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, http.StatusBadGateway, syncErrApplyFailed, fmt.Errorf("blob GET %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, http.StatusBadGateway, syncErrS3Unreachable, err
	}
	return body, 0, "", nil
}
