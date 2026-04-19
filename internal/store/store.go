// Package store provides SQLite-backed persistence for session metadata.
//
// The store is the source of truth for user-owned fields (visibility,
// follow_up, draft, last_read_at) while backend-owned fields (title,
// timestamps, status) are refreshed from the agent backend on discovery.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/acksell/clank/internal/agent"

	// Pure-Go SQLite driver (no CGo).
	_ "modernc.org/sqlite"
)

// Store wraps a SQLite database for persisting session metadata.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) a SQLite database at dbPath and runs any
// pending schema migrations. The caller must call Close when done.
func Open(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	// SQLite only supports a single concurrent writer. Limiting the pool
	// to one connection ensures all PRAGMAs apply to every query (they are
	// per-connection state) and serialises writes through Go's sql.DB,
	// avoiding SQLITE_BUSY errors from pool-spawned connections that would
	// otherwise lack the busy_timeout PRAGMA.
	db.SetMaxOpenConns(1)

	// Configure SQLite for concurrent access and durability.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %q: %w", p, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// migrate applies schema migrations using PRAGMA user_version.
func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	if version < 1 {
		_, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS sessions (
				id            TEXT PRIMARY KEY,
				external_id   TEXT NOT NULL DEFAULT '',
				backend       TEXT NOT NULL,
				status        TEXT NOT NULL DEFAULT 'idle',
				visibility    TEXT NOT NULL DEFAULT '',
				follow_up     INTEGER NOT NULL DEFAULT 0,
				project_dir   TEXT NOT NULL,
				project_name  TEXT NOT NULL,
				prompt        TEXT NOT NULL DEFAULT '',
				title         TEXT NOT NULL DEFAULT '',
				ticket_id     TEXT NOT NULL DEFAULT '',
				agent         TEXT NOT NULL DEFAULT '',
				draft         TEXT NOT NULL DEFAULT '',
				created_at    DATETIME NOT NULL,
				updated_at    DATETIME NOT NULL,
				last_read_at  DATETIME
			);
			CREATE INDEX IF NOT EXISTS idx_sessions_external_id ON sessions(external_id);
			PRAGMA user_version = 1;
		`)
		if err != nil {
			return fmt.Errorf("migration v1: %w", err)
		}
		version = 1
	}

	if version < 2 {
		_, err := s.db.Exec(`
			CREATE TABLE IF NOT EXISTS agents (
				backend      TEXT NOT NULL,
				project_dir  TEXT NOT NULL,
				agents_json  TEXT NOT NULL DEFAULT '[]',
				updated_at   DATETIME NOT NULL,
				PRIMARY KEY (backend, project_dir)
			);
			PRAGMA user_version = 2;
		`)
		if err != nil {
			return fmt.Errorf("migration v2: %w", err)
		}
		version = 2
	}

	if version < 3 {
		_, err := s.db.Exec(`
			ALTER TABLE agents RENAME TO primary_agents;
			ALTER TABLE primary_agents RENAME COLUMN agents_json TO primary_agents_json;
			PRAGMA user_version = 3;
		`)
		if err != nil {
			return fmt.Errorf("migration v3: %w", err)
		}
		version = 3
	}

	if version < 9 {
		_, err := s.db.Exec(`
			ALTER TABLE sessions ADD COLUMN branch TEXT NOT NULL DEFAULT '';
			ALTER TABLE sessions ADD COLUMN worktree_dir TEXT NOT NULL DEFAULT '';
			PRAGMA user_version = 9;
		`)
		if err != nil {
			return fmt.Errorf("migration v9: %w", err)
		}
		version = 9
	}

	if version < 10 {
		_, err := s.db.Exec(`
			ALTER TABLE sessions RENAME COLUMN branch TO worktree_branch;
			PRAGMA user_version = 10;
		`)
		if err != nil {
			return fmt.Errorf("migration v10: %w", err)
		}
		version = 10
	}
	if version < 11 {
		// Phase 3A: introduce host-scoped, path-free identity alongside legacy
		// project_dir/worktree_branch columns. Default host_id to 'local' so
		// existing rows remain valid; repo_remote_url is backfilled lazily by
		// the hub on next session start (or DiscoverSessions).
		_, err := s.db.Exec(`
			ALTER TABLE sessions ADD COLUMN host_id TEXT NOT NULL DEFAULT 'local';
			ALTER TABLE sessions ADD COLUMN repo_remote_url TEXT NOT NULL DEFAULT '';
			PRAGMA user_version = 11;
		`)
		if err != nil {
			return fmt.Errorf("migration v11: %w", err)
		}
		version = 11
	}
	if version < 12 {
		// Step 8d: replace the single repo_remote_url string with the
		// three-field GitRef shape (kind + url|path) as discrete columns.
		// Existing non-empty values are remote URLs by construction, so
		// we backfill kind='remote' for those rows; empty rows stay empty
		// and will be re-resolved on next session start.
		_, err := s.db.Exec(`
			ALTER TABLE sessions RENAME COLUMN repo_remote_url TO git_ref_url;
			ALTER TABLE sessions ADD COLUMN git_ref_kind TEXT NOT NULL DEFAULT '';
			ALTER TABLE sessions ADD COLUMN git_ref_path TEXT NOT NULL DEFAULT '';
			UPDATE sessions SET git_ref_kind = 'remote' WHERE git_ref_url != '';
			PRAGMA user_version = 12;
		`)
		if err != nil {
			return fmt.Errorf("migration v12: %w", err)
		}
		version = 12
	}
	if version < 13 {
		// Step 8e-2a: rekey the primary_agents catalog cache from
		// (backend, project_dir) to (backend, host_id, git_ref) per §7.8
		// of hub_host_refactor_code_review.md. The wire identity is now
		// (Hostname, GitRef); the cache must follow. We drop the table
		// outright (no migration of stale rows) — the cache is a
		// pure-derived warmup and will be repopulated on next list call.
		_, err := s.db.Exec(`
			DROP TABLE IF EXISTS primary_agents;
			CREATE TABLE primary_agents (
				backend             TEXT NOT NULL,
				host_id             TEXT NOT NULL,
				git_ref_kind        TEXT NOT NULL,
				git_ref_url         TEXT NOT NULL DEFAULT '',
				git_ref_path        TEXT NOT NULL DEFAULT '',
				primary_agents_json TEXT NOT NULL DEFAULT '[]',
				updated_at          DATETIME NOT NULL,
				PRIMARY KEY (backend, host_id, git_ref_kind, git_ref_url, git_ref_path)
			);
			PRAGMA user_version = 13;
		`)
		if err != nil {
			return fmt.Errorf("migration v13: %w", err)
		}
		version = 13
	}
	if version < 14 {
		// Step 8e-2b: drop project_dir, project_name, worktree_dir columns
		// from sessions. Per §7.1 (line 307) of hub_host_refactor_code_review.md:
		// SessionInfo is now path-free. Identity = (Hostname, GitRef,
		// WorktreeBranch); the host resolves workdirs internally on
		// demand. Display name is derived from GitRef.DisplayName.
		//
		// SQLite < 3.35 lacks DROP COLUMN, but modernc.org/sqlite ships
		// a recent SQLite. Use ALTER TABLE DROP COLUMN.
		_, err := s.db.Exec(`
			ALTER TABLE sessions DROP COLUMN project_dir;
			ALTER TABLE sessions DROP COLUMN project_name;
			ALTER TABLE sessions DROP COLUMN worktree_dir;
			PRAGMA user_version = 14;
		`)
		if err != nil {
			return fmt.Errorf("migration v14: %w", err)
		}
		version = 14
	}
	_ = version // suppress unused warning after last migration

	return nil
}

// LoadSessions returns all persisted sessions. Returns an empty (non-nil)
// slice when no sessions exist.
func (s *Store) LoadSessions() ([]agent.SessionInfo, error) {
	rows, err := s.db.Query(`
		SELECT id, external_id, backend, status, visibility, follow_up,
		       worktree_branch,
		       host_id, git_ref_kind, git_ref_url, git_ref_path,
		       prompt, title, ticket_id, agent, draft,
		       created_at, updated_at, last_read_at
		FROM sessions
	`)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []agent.SessionInfo
	for rows.Next() {
		var info agent.SessionInfo
		var followUp int
		var lastReadAt sql.NullTime
		var refKind, refURL, refPath string
		err := rows.Scan(
			&info.ID,
			&info.ExternalID,
			&info.Backend,
			&info.Status,
			&info.Visibility,
			&followUp,
			&info.WorktreeBranch,
			&info.Hostname,
			&refKind,
			&refURL,
			&refPath,
			&info.Prompt,
			&info.Title,
			&info.TicketID,
			&info.Agent,
			&info.Draft,
			&info.CreatedAt,
			&info.UpdatedAt,
			&lastReadAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		if refKind != "" {
			info.GitRef = agent.GitRef{Kind: agent.GitRefKind(refKind), URL: refURL, Path: refPath}
		}
		info.FollowUp = followUp != 0
		if lastReadAt.Valid {
			info.LastReadAt = lastReadAt.Time
		}
		sessions = append(sessions, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}

	if sessions == nil {
		sessions = []agent.SessionInfo{}
	}
	return sessions, nil
}

// UpsertSession inserts or replaces a session in the database.
// All fields are overwritten.
func (s *Store) UpsertSession(info agent.SessionInfo) error {
	followUp := 0
	if info.FollowUp {
		followUp = 1
	}
	var lastReadAt *time.Time
	if !info.LastReadAt.IsZero() {
		lastReadAt = &info.LastReadAt
	}

	branch := info.WorktreeBranch
	hostname := info.Hostname
	if hostname == "" {
		hostname = "local"
	}

	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO sessions
			(id, external_id, backend, status, visibility, follow_up,
			 worktree_branch,
			 host_id, git_ref_kind, git_ref_url, git_ref_path,
			 prompt, title, ticket_id, agent, draft,
			 created_at, updated_at, last_read_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		info.ID,
		info.ExternalID,
		string(info.Backend),
		string(info.Status),
		string(info.Visibility),
		followUp,
		branch,
		hostname,
		string(info.GitRef.Kind),
		info.GitRef.URL,
		info.GitRef.Path,
		info.Prompt,
		info.Title,
		info.TicketID,
		info.Agent,
		info.Draft,
		info.CreatedAt,
		info.UpdatedAt,
		lastReadAt,
	)
	if err != nil {
		return fmt.Errorf("upsert session %s: %w", info.ID, err)
	}
	return nil
}

// DeleteSession removes a session by its daemon ID.
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	return nil
}

// AgentTarget identifies the (backend, host, repo) triple that the
// primary-agent catalog cache is keyed on. Returned by KnownAgentTargets
// for cache warmup. Per §7.8 of hub_host_refactor_code_review.md, the
// catalog is per-repo (branch deliberately dropped) because opencode /
// claude-code config is committed to git and shared across branches.
type AgentTarget struct {
	Backend  agent.BackendType
	Hostname string
	GitRef   agent.GitRef
}

// LoadPrimaryAgents returns the cached primary agent list for the
// (backend, hostname, gitRef) tuple. Returns nil (not an error) if no
// cached entry exists. Hostname defaults to "local" when empty (matches
// the sessions table convention).
func (s *Store) LoadPrimaryAgents(backend agent.BackendType, hostname string, ref agent.GitRef) ([]agent.AgentInfo, error) {
	if hostname == "" {
		hostname = "local"
	}
	var agentsJSON string
	err := s.db.QueryRow(`
		SELECT primary_agents_json FROM primary_agents
		WHERE backend = ? AND host_id = ?
		  AND git_ref_kind = ? AND git_ref_url = ? AND git_ref_path = ?
	`, string(backend), hostname, string(ref.Kind), ref.URL, ref.Path).Scan(&agentsJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load primary agents for %s/%s/%s: %w", backend, hostname, ref.Canonical(), err)
	}
	var agents []agent.AgentInfo
	if err := json.Unmarshal([]byte(agentsJSON), &agents); err != nil {
		return nil, fmt.Errorf("decode primary agents for %s/%s/%s: %w", backend, hostname, ref.Canonical(), err)
	}
	return agents, nil
}

// UpsertPrimaryAgents stores the primary agent list for the
// (backend, hostname, gitRef) tuple. Hostname defaults to "local" when
// empty.
func (s *Store) UpsertPrimaryAgents(backend agent.BackendType, hostname string, ref agent.GitRef, agents []agent.AgentInfo) error {
	if hostname == "" {
		hostname = "local"
	}
	if ref.Kind == "" {
		return fmt.Errorf("upsert primary agents: git ref is required")
	}
	data, err := json.Marshal(agents)
	if err != nil {
		return fmt.Errorf("encode primary agents: %w", err)
	}
	_, err = s.db.Exec(`
		INSERT OR REPLACE INTO primary_agents
			(backend, host_id, git_ref_kind, git_ref_url, git_ref_path,
			 primary_agents_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, string(backend), hostname, string(ref.Kind), ref.URL, ref.Path, string(data), time.Now())
	if err != nil {
		return fmt.Errorf("upsert primary agents for %s/%s/%s: %w", backend, hostname, ref.Canonical(), err)
	}
	return nil
}

// KnownAgentTargets returns the distinct (backend, hostname, gitRef)
// tuples derived from the sessions table. Used by the hub to warm the
// primary-agent catalog cache for every (host, repo) pair that has at
// least one known session. Sessions without a resolved GitRef are
// skipped — they are not addressable as catalog targets yet.
func (s *Store) KnownAgentTargets() ([]AgentTarget, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT backend, host_id, git_ref_kind, git_ref_url, git_ref_path
		FROM sessions
		WHERE git_ref_kind != ''
	`)
	if err != nil {
		return nil, fmt.Errorf("query agent targets: %w", err)
	}
	defer rows.Close()
	var out []AgentTarget
	for rows.Next() {
		var t AgentTarget
		var be, host, kind, url, path string
		if err := rows.Scan(&be, &host, &kind, &url, &path); err != nil {
			return nil, fmt.Errorf("scan agent target: %w", err)
		}
		t.Backend = agent.BackendType(be)
		t.Hostname = host
		t.GitRef = agent.GitRef{Kind: agent.GitRefKind(kind), URL: url, Path: path}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FindByExternalID returns the persisted session matching the given
// external (backend) ID, or nil if not found.
func (s *Store) FindByExternalID(externalID string) (*agent.SessionInfo, error) {
	var info agent.SessionInfo
	var followUp int
	var lastReadAt sql.NullTime
	var refKind, refURL, refPath string
	err := s.db.QueryRow(`
		SELECT id, external_id, backend, status, visibility, follow_up,
		       worktree_branch,
		       host_id, git_ref_kind, git_ref_url, git_ref_path,
		       prompt, title, ticket_id, agent, draft,
		       created_at, updated_at, last_read_at
		FROM sessions
		WHERE external_id = ?
	`, externalID).Scan(
		&info.ID,
		&info.ExternalID,
		&info.Backend,
		&info.Status,
		&info.Visibility,
		&followUp,
		&info.WorktreeBranch,
		&info.Hostname,
		&refKind,
		&refURL,
		&refPath,
		&info.Prompt,
		&info.Title,
		&info.TicketID,
		&info.Agent,
		&info.Draft,
		&info.CreatedAt,
		&info.UpdatedAt,
		&lastReadAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find session by external_id %s: %w", externalID, err)
	}
	info.FollowUp = followUp != 0
	if lastReadAt.Valid {
		info.LastReadAt = lastReadAt.Time
	}
	if refKind != "" {
		info.GitRef = agent.GitRef{Kind: agent.GitRefKind(refKind), URL: refURL, Path: refPath}
	}
	return &info, nil
}
