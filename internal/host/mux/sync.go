package hostmux

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/acksell/clank/pkg/sync/checkpoint"
)

// handleSyncApply applies code into a per-user working tree under
// ~/work/<repo>/. Two body shapes are supported, dispatched on the
// Content-Type header:
//
//   - application/x-git-bundle (legacy): the body is raw git bundle
//     bytes. First call clones, subsequent calls force-fetch all refs.
//
//   - multipart/form-data (new in P3): the body carries a checkpoint —
//     a JSON manifest plus two bundles (headCommit + incremental).
//     Apply rebuilds HEAD, branch, index, and the working tree
//     (including untracked files) to match the manifest exactly. The
//     gateway's MigrateWorktree(to_sprite) flow uses this shape; the
//     sync server is no longer permitted to call it autonomously.
//
// Query params:
//
//	repo  — relative path under ~/work/ (e.g. "myproject"). Must be a
//	        single path segment; ".." or absolute paths are rejected to
//	        keep blast radius inside ~/work/.
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

	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType == "multipart/form-data" {
		m.applyCheckpoint(w, r, target)
		return
	}
	m.applyLegacyBundle(w, r, workDir, target, repo)
}

// applyLegacyBundle is the pre-P3 path: a single raw git bundle in
// the request body. Stays in place to support the autonomous-flush
// path until it's deleted in the next phase.
func (m *Mux) applyLegacyBundle(w http.ResponseWriter, r *http.Request, workDir, target, repo string) {
	bundle, err := stageBundle(r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "stage_bundle", Error: err.Error()})
		return
	}
	defer os.Remove(bundle)

	exists, err := isGitRepo(target)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "stat_target", Error: err.Error()})
		return
	}
	if !exists {
		if err := runGit(r.Context(), workDir, "clone", bundle, repo); err != nil {
			writeJSON(w, http.StatusInternalServerError, errResp{Code: "git_clone", Error: err.Error()})
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Existing repo: force-fetch all refs from the bundle. +refs/*:refs/*
	// lets later bundles supersede earlier ones (laptop is the writer).
	if err := runGit(r.Context(), target, "fetch", bundle, "+refs/*:refs/*"); err != nil {
		writeJSON(w, http.StatusInternalServerError, errResp{Code: "git_fetch", Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// applyCheckpoint is the new P3 path: a multipart body containing a
// manifest JSON plus two bundles. After Apply, the working tree at
// ~/work/<repo> matches the manifest exactly — HEAD, branch, index,
// and untracked files all restored.
//
// Multipart form fields (order does not matter; FormFile resolves
// each by name):
//
//	"manifest"     — application/json: the checkpoint.Manifest JSON.
//	"head_commit"  — application/octet-stream: the headCommit bundle.
//	"incremental"  — application/octet-stream: the incremental bundle.
func (m *Mux) applyCheckpoint(w http.ResponseWriter, r *http.Request, target string) {
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

func stageBundle(body io.Reader) (string, error) {
	f, err := os.CreateTemp("", "clank-sync-*.bundle")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

func isGitRepo(target string) (bool, error) {
	if _, err := os.Stat(filepath.Join(target, ".git")); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if _, err := os.Stat(filepath.Join(target, "HEAD")); err == nil {
		return true, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	return false, nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}
