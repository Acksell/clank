package store

import (
	"fmt"
	"strings"

	"github.com/acksell/clank/internal/agent"
	"github.com/acksell/clank/internal/gitendpoint"
)

// migrateV16 explodes the opaque git_remote_url column into the five
// flat fields of agent.GitEndpoint (protocol, user, host, port, path).
//
// Why: the hub's credential resolver needs the parsed endpoint to
// decide auth (e.g. SSH→HTTPS rewrite for public providers). Storing
// only the raw string forced every consumer to re-parse on every load,
// and silently re-broke the Daytona-no-SSH bug whenever a code path
// forgot to call gitendpoint.Parse. Storing the parsed form makes the
// in-memory invariant ("if endpoint != nil it has been parsed once
// and validated") match the on-disk shape.
//
// Backfill policy: hard-fail loudly if any existing row has a
// git_remote_url that gitendpoint.Parse rejects. The error message
// lists the offending session/primary-agent rows so the operator can
// decide whether to fix or delete them. We never silently drop rows.
//
// primary_agents is a pure derivation cache (re-populated on demand by
// the catalog warmup loop), so we DROP and CREATE it with the new
// schema rather than backfilling — simpler and safer.
func (s *Store) migrateV16() error {
	if err := s.migrateV16Sessions(); err != nil {
		return fmt.Errorf("sessions: %w", err)
	}
	if err := s.migrateV16PrimaryAgents(); err != nil {
		return fmt.Errorf("primary_agents: %w", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 16`); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

func (s *Store) migrateV16Sessions() error {
	cols, err := s.sessionColumns()
	if err != nil {
		return err
	}

	// Idempotency: if the new columns already exist, assume a previous
	// migration attempt got most of the way through.
	endpointCols := []struct {
		name string
		ddl  string
	}{
		{"git_endpoint_protocol", `ALTER TABLE sessions ADD COLUMN git_endpoint_protocol TEXT NOT NULL DEFAULT ''`},
		{"git_endpoint_user", `ALTER TABLE sessions ADD COLUMN git_endpoint_user TEXT NOT NULL DEFAULT ''`},
		{"git_endpoint_host", `ALTER TABLE sessions ADD COLUMN git_endpoint_host TEXT NOT NULL DEFAULT ''`},
		{"git_endpoint_port", `ALTER TABLE sessions ADD COLUMN git_endpoint_port INTEGER NOT NULL DEFAULT 0`},
		{"git_endpoint_path", `ALTER TABLE sessions ADD COLUMN git_endpoint_path TEXT NOT NULL DEFAULT ''`},
	}
	for _, c := range endpointCols {
		if cols[c.name] {
			continue
		}
		if _, err := s.db.Exec(c.ddl); err != nil {
			return fmt.Errorf("add %s: %w", c.name, err)
		}
	}

	// Backfill from git_remote_url. If it doesn't exist, nothing to do.
	if cols["git_remote_url"] {
		if err := s.backfillSessionEndpoints(); err != nil {
			return err
		}
		if _, err := s.db.Exec(`ALTER TABLE sessions DROP COLUMN git_remote_url`); err != nil {
			return fmt.Errorf("drop git_remote_url: %w", err)
		}
	}
	return nil
}

func (s *Store) backfillSessionEndpoints() error {
	rows, err := s.db.Query(`SELECT id, git_remote_url FROM sessions WHERE git_remote_url != ''`)
	if err != nil {
		return fmt.Errorf("scan sessions for backfill: %w", err)
	}
	defer rows.Close()

	type row struct {
		id, url string
		ep      *agent.GitEndpoint
	}
	var toUpdate []row
	var failures []string
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.url); err != nil {
			return fmt.Errorf("scan session row: %w", err)
		}
		ep, perr := gitendpoint.Parse(r.url)
		if perr != nil {
			failures = append(failures, fmt.Sprintf("  session %s: %q (%v)", r.id, r.url, perr))
			continue
		}
		r.ep = ep
		toUpdate = append(toUpdate, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sessions: %w", err)
	}
	if len(failures) > 0 {
		return fmt.Errorf("refusing to migrate: %d session row(s) have unparseable git_remote_url; "+
			"fix or delete them and retry. Offenders:\n%s",
			len(failures), strings.Join(failures, "\n"))
	}

	// Update outside the read iteration (single-conn pool — re-entrant
	// queries on the same connection are unsupported).
	for _, r := range toUpdate {
		ep := r.ep
		if _, err := s.db.Exec(`
			UPDATE sessions SET
				git_endpoint_protocol = ?,
				git_endpoint_user     = ?,
				git_endpoint_host     = ?,
				git_endpoint_port     = ?,
				git_endpoint_path     = ?
			WHERE id = ?
		`, string(ep.Protocol), ep.User, ep.Host, ep.Port, ep.Path, r.id); err != nil {
			return fmt.Errorf("update session %s: %w", r.id, err)
		}
	}
	return nil
}

func (s *Store) migrateV16PrimaryAgents() error {
	// primary_agents is a derivation cache; safe to drop and recreate.
	if _, err := s.db.Exec(`
		DROP TABLE IF EXISTS primary_agents;
		CREATE TABLE primary_agents (
			backend               TEXT    NOT NULL,
			host_id               TEXT    NOT NULL,
			project_dir           TEXT    NOT NULL DEFAULT '',
			git_endpoint_protocol TEXT    NOT NULL DEFAULT '',
			git_endpoint_user     TEXT    NOT NULL DEFAULT '',
			git_endpoint_host     TEXT    NOT NULL DEFAULT '',
			git_endpoint_port     INTEGER NOT NULL DEFAULT 0,
			git_endpoint_path     TEXT    NOT NULL DEFAULT '',
			primary_agents_json   TEXT    NOT NULL DEFAULT '[]',
			updated_at            DATETIME NOT NULL,
			PRIMARY KEY (backend, host_id, project_dir,
			             git_endpoint_protocol, git_endpoint_user,
			             git_endpoint_host, git_endpoint_port, git_endpoint_path)
		);
	`); err != nil {
		return fmt.Errorf("recreate primary_agents: %w", err)
	}
	return nil
}
