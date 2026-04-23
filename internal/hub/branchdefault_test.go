package hub

import (
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
			name:     "remote host with empty session id yields empty (fail-safe)",
			hostname: "daytona-abc",
			session:  "",
			explicit: "",
			want:     "",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := defaultWorktreeBranch(tc.hostname, tc.session, tc.explicit)
			if got != tc.want {
				t.Errorf("defaultWorktreeBranch(%q, %q, %q) = %q, want %q",
					tc.hostname, tc.session, tc.explicit, got, tc.want)
			}
		})
	}
}
