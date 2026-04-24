package hub

import (
	"strings"
	"testing"

	"github.com/acksell/clank/internal/host"
)

func TestDefaultWorktreeBranch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		hostname host.Hostname
		session  string
		explicit string
		want     string
		wantErr  string // substring; "" = no error expected
	}{
		{
			name:     "remote host, empty branch, fills clank/<id>",
			hostname: "daytona-abc",
			session:  "01SESSION",
			explicit: "",
			want:     "clank/01SESSION",
		},
		{
			name:     "remote host, explicit branch is unchanged",
			hostname: "daytona-abc",
			session:  "01SESSION",
			explicit: "feature/x",
			want:     "feature/x",
		},
		{
			name:     "local host, empty branch stays empty",
			hostname: host.HostLocal,
			session:  "01SESSION",
			explicit: "",
			want:     "",
		},
		{
			name:     "local host, explicit branch is unchanged",
			hostname: host.HostLocal,
			session:  "01SESSION",
			explicit: "feature/x",
			want:     "feature/x",
		},
		{
			name:     "empty hostname treated as local",
			hostname: "",
			session:  "01SESSION",
			explicit: "",
			want:     "",
		},
		{
			// Remote + empty sessionID is a programming error upstream
			// (CreateSession assigns the ID before this seam). Loud
			// failure beats silent fallthrough that would let work
			// land on the default branch of an ephemeral sandbox.
			name:     "remote host with empty session id is a hard error",
			hostname: "daytona-abc",
			session:  "",
			explicit: "",
			wantErr:  "non-empty sessionID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := defaultWorktreeBranch(tc.hostname, tc.session, tc.explicit)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("defaultWorktreeBranch err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("defaultWorktreeBranch unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("defaultWorktreeBranch(%q, %q, %q) = %q, want %q",
					tc.hostname, tc.session, tc.explicit, got, tc.want)
			}
		})
	}
}
