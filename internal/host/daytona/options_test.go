package daytona

import (
	"testing"
	"time"
)

func TestLaunchOptionsValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		opts    LaunchOptions
		wantErr bool
	}{
		{
			name:    "missing api key",
			opts:    LaunchOptions{ListenPort: 8080},
			wantErr: true,
		},
		{
			name:    "port too low",
			opts:    LaunchOptions{APIKey: "k", ListenPort: 80},
			wantErr: true,
		},
		{
			name:    "port too high",
			opts:    LaunchOptions{APIKey: "k", ListenPort: 60000},
			wantErr: true,
		},
		{
			name:    "minimal valid",
			opts:    LaunchOptions{APIKey: "k", ListenPort: 8080},
			wantErr: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.opts.validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("validate err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestLaunchOptionsWithDefaults(t *testing.T) {
	t.Parallel()
	got := LaunchOptions{APIKey: "k"}.withDefaults()
	if got.ListenPort != defaultListenPort {
		t.Errorf("ListenPort=%d want %d", got.ListenPort, defaultListenPort)
	}
	if got.ReadyTimeout != defaultReadyTimeout {
		t.Errorf("ReadyTimeout=%v want %v", got.ReadyTimeout, defaultReadyTimeout)
	}
	if got.Labels[labelClankHost] != "true" {
		t.Errorf("missing required label %q", labelClankHost)
	}
}

func TestLaunchOptionsWithDefaults_PreservesUserLabels(t *testing.T) {
	t.Parallel()
	got := LaunchOptions{
		APIKey: "k",
		Labels: map[string]string{"team": "platform"},
	}.withDefaults()
	if got.Labels["team"] != "platform" {
		t.Errorf("user label dropped: %v", got.Labels)
	}
	if got.Labels[labelClankHost] != "true" {
		t.Errorf("required label dropped: %v", got.Labels)
	}
}

// Compile-time sanity that the default timeout is something sane —
// catches a future refactor that accidentally sets it to 0.
func TestDefaultsAreNonZero(t *testing.T) {
	t.Parallel()
	if defaultReadyTimeout <= 0 || defaultReadyTimeout > 5*time.Minute {
		t.Errorf("defaultReadyTimeout %v looks wrong", defaultReadyTimeout)
	}
}

// withDefaults must not mutate the caller's Labels map. Regression
// for: previously the labelClankHost write went through the aliased
// map, so a caller that reused a Labels literal across multiple
// Launch calls (or kept a reference for inspection) would observe
// surprising "clank/host" labels appearing on it.
func TestLaunchOptionsWithDefaults_DoesNotMutateCallerLabels(t *testing.T) {
	t.Parallel()
	caller := map[string]string{"team": "platform"}
	_ = LaunchOptions{APIKey: "k", Labels: caller}.withDefaults()
	if _, mutated := caller[labelClankHost]; mutated {
		t.Fatalf("withDefaults mutated caller's Labels: %v", caller)
	}
	if len(caller) != 1 || caller["team"] != "platform" {
		t.Fatalf("caller's Labels modified: %v", caller)
	}
}
