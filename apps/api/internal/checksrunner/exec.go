package checksrunner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// tailBuffer keeps only the last `limit` bytes written so a check's combined
// output artifact stays bounded without holding the whole stream in memory.
type tailBuffer struct {
	limit int
	data  []byte
	total int64
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.total += int64(len(p))
	b.data = append(b.data, p...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(p), nil
}

// checkEnv builds the child environment: a minimal allowlist of host
// variables the toolchain needs to resolve executables and write temp files,
// plus the manifest-declared passThrough names. Nothing else leaks into the
// check process or the recorded environment.
func checkEnv(passThrough []string, lookup func(string) string) []string {
	base := []string{
		"PATH", "PATHEXT", "COMSPEC", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR",
		"TEMP", "TMP", "TMPDIR", "USERPROFILE", "HOME", "HOMEDRIVE", "HOMEPATH",
		"APPDATA", "LOCALAPPDATA", "PROGRAMFILES", "PROGRAMFILES(X86)",
		"PROGRAMDATA", "OS", "USERNAME", "USERDOMAIN",
		"PROCESSOR_ARCHITECTURE", "NUMBER_OF_PROCESSORS",
	}
	env := make([]string, 0, len(base)+len(passThrough))
	seen := map[string]bool{}
	for _, name := range append(append([]string{}, base...), passThrough...) {
		if seen[name] {
			continue
		}
		seen[name] = true
		if value, ok := lookupEnv(lookup, name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

func lookupEnv(lookup func(string) string, name string) (string, bool) {
	if lookup != nil {
		value := lookup(name)
		return value, value != ""
	}
	return os.LookupEnv(name)
}

// resolveArgv rewrites an argv whose executable resolves to a batch file so
// it runs through %COMSPEC% on Windows; executable binaries run directly.
// The manifest argv array shape is preserved end-to-end — nothing is joined
// into a shell string by us.
func resolveArgv(argv []string, lookPath func(string) (string, error)) []string {
	resolved, err := lookPath(argv[0])
	if err != nil {
		return argv
	}
	ext := strings.ToLower(filepath.Ext(resolved))
	if runtime.GOOS == "windows" && (ext == ".cmd" || ext == ".bat") {
		comspec := os.Getenv("COMSPEC")
		if comspec == "" {
			comspec = `C:\Windows\System32\cmd.exe`
		}
		return append([]string{comspec, "/d", "/s", "/c", resolved}, argv[1:]...)
	}
	if resolved != argv[0] {
		return append([]string{resolved}, argv[1:]...)
	}
	return argv
}

// checkResult is one check's measured outcome.
type checkResult struct {
	exitCode    int
	duration    time.Duration
	output      []byte
	truncated   bool
	failureCode string // empty on exit 0
}

// runCheck executes one manifest check argv inside dir with a bounded
// timeout, allowlisted environment, and tail-bounded combined output.
func runCheck(ctx context.Context, dir string, argv []string, env []string,
	timeout time.Duration, outputLimit int) checkResult {
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv = resolveArgv(argv, exec.LookPath)
	cmd := exec.CommandContext(checkCtx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	out := &tailBuffer{limit: outputLimit}
	cmd.Stdout = out
	cmd.Stderr = out

	started := time.Now()
	err := cmd.Run()
	result := checkResult{duration: time.Since(started), output: out.data, truncated: out.total > int64(outputLimit)}
	if err == nil {
		return result
	}
	result.exitCode = -1
	var exitErr *exec.ExitError
	switch {
	case checkCtx.Err() == context.DeadlineExceeded:
		result.failureCode = "timeout"
	case errors.As(err, &exitErr):
		result.exitCode = exitErr.ExitCode()
		result.failureCode = "nonzero_exit"
	default:
		result.failureCode = "spawn_error"
	}
	return result
}
