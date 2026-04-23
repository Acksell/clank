package git

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// AskpassScript writes a temporary GIT_ASKPASS helper that echoes
// secret to stdout when git invokes it (typically prompting for a
// password or token). Returns the env vars to merge into a git
// command invocation, plus a cleanup closure that unlinks the script.
//
// Why askpass instead of GIT_USERNAME / token-in-URL:
//   - Tokens never appear in argv (visible via /proc/<pid>/cmdline to
//     any local user).
//   - Tokens never appear in os.Environ() of the git process beyond
//     the path to a script the OS already protects with mode 0700.
//   - The askpass script body is the only place the secret lives, on
//     a file we created and unlink in the same goroutine. No risk of
//     leaking via core dumps of the parent process.
//
// Callers MUST defer the returned cleanup. Failing to do so leaks a
// 0700-mode file containing the secret in TempDir.
//
// On Windows GIT_ASKPASS expects a .bat / .cmd; not supported here
// because the Clank host targets Unix. Errors loudly rather than
// producing a script git won't execute.
func AskpassScript(secret string) (env []string, cleanup func() error, err error) {
	if runtime.GOOS == "windows" {
		return nil, nil, fmt.Errorf("askpass: windows hosts are not supported")
	}
	if secret == "" {
		return nil, nil, fmt.Errorf("askpass: empty secret")
	}
	dir, err := os.MkdirTemp("", "clank-askpass-")
	if err != nil {
		return nil, nil, fmt.Errorf("askpass: mkdtemp: %w", err)
	}
	// 0700 on the dir + 0700 on the file: nothing else on the box can
	// read either. We escape the secret so a value containing single
	// quotes doesn't break out of the shell literal.
	path := filepath.Join(dir, "askpass.sh")
	body := "#!/bin/sh\nexec printf %s '" + shellEscapeSingle(secret) + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("askpass: write script: %w", err)
	}
	env = []string{
		"GIT_ASKPASS=" + path,
		// Suspenders + belt: even if askpass fails, do not fall back
		// to a TTY prompt.
		"GIT_TERMINAL_PROMPT=0",
	}
	cleanup = func() error {
		return os.RemoveAll(dir)
	}
	return env, cleanup, nil
}

// shellEscapeSingle escapes a string for safe interpolation inside a
// POSIX single-quoted literal. Single quotes inside single quotes are
// not legal — close, escape with \', reopen.
func shellEscapeSingle(s string) string {
	out := make([]byte, 0, len(s)+8)
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\\', '\'', '\'')
			continue
		}
		out = append(out, s[i])
	}
	return string(out)
}
