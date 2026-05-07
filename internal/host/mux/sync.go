package hostmux

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// handleSyncApply applies a git bundle uploaded by clank-sync into a
// per-user working tree on the host. The body is the raw bundle bytes
// (no multipart wrapper — middleware-friendly and avoids the 32MB
// default form parse).
//
// Query params:
//
//	repo     — relative path under ~/work/ (e.g. "myproject"). Must be
//	           a single path segment; ".." or absolute paths are
//	           rejected to keep blast radius inside ~/work/.
//
// First call: creates the target dir + clones from the bundle. Subsequent
// calls force-fetch all refs (laptop is the writer of record).
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
