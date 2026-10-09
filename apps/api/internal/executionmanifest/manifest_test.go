package executionmanifest

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func exampleManifest(t *testing.T) []byte {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("could not locate test source")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", "..", "..",
		"examples", "execution-manifest.react-pnpm.json"))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read example manifest: %v", err)
	}
	return data
}

func TestParseAcceptsCanonicalExample(t *testing.T) {
	manifest, err := Parse(exampleManifest(t))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if manifest.WorkerProfile != WorkerProfile || manifest.WorkingDirectory != "." ||
		manifest.NetworkProfile != "package-registries" || len(manifest.Commands.Checks) != 3 {
		t.Fatalf("manifest = %+v, want the canonical React/pnpm contract", manifest)
	}
	if got := Digest(exampleManifest(t)); len(got) != 64 {
		t.Fatalf("Digest() = %q, want 64 hex characters", got)
	}
}

func TestParseRejectsContractViolations(t *testing.T) {
	base := `{
		"manifestVersion": 1,
		"workerProfile": "node-22-pnpm-10-playwright",
		"workingDirectory": ".",
		"networkProfile": "none",
		"environment": {"passThrough": ["CI"]},
		"commands": {
			"install": {"argv": ["pnpm", "install", "--frozen-lockfile"], "timeoutSeconds": 600},
			"start": {"argv": ["pnpm", "dev"], "timeoutSeconds": 60},
			"checks": [{"id": "lint", "name": "Lint", "argv": ["pnpm", "lint"], "required": true, "timeoutSeconds": 300}]
		},
		"preview": {"port": 3000, "healthPath": "/", "readinessTimeoutSeconds": 120, "routes": ["/"], "viewports": [{"name": "d", "width": 1440, "height": 900}]},
		"resources": {"cpu": 2, "memoryMiB": 4096, "diskGiB": 10, "attemptTimeoutSeconds": 1800}
	}`
	cases := []struct {
		name    string
		mutate  func(string) string
		wantSub string
	}{
		{"unknown field", func(s string) string {
			return strings.Replace(s, `"manifestVersion": 1`, `"manifestVersion": 1, "extra": true`, 1)
		}, "unknown field"},
		{"unsupported version", func(s string) string { return strings.Replace(s, `"manifestVersion": 1`, `"manifestVersion": 2`, 1) }, "manifestVersion"},
		{"unknown worker profile", func(s string) string { return strings.Replace(s, "node-22-pnpm-10-playwright", "node-20", 1) }, "workerProfile"},
		{"absolute working directory", func(s string) string {
			return strings.Replace(s, `"workingDirectory": "."`, `"workingDirectory": "/etc"`, 1)
		}, "workingDirectory"},
		{"traversal working directory", func(s string) string {
			return strings.Replace(s, `"workingDirectory": "."`, `"workingDirectory": "../escape"`, 1)
		}, "workingDirectory"},
		{"windows path working directory", func(s string) string {
			return strings.Replace(s, `"workingDirectory": "."`, `"workingDirectory": "apps\\web"`, 1)
		}, "workingDirectory"},
		{"free-form network profile", func(s string) string {
			return strings.Replace(s, `"networkProfile": "none"`, `"networkProfile": "https://evil.example"`, 1)
		}, "networkProfile"},
		{"missing passThrough", func(s string) string {
			return strings.Replace(s, `"environment": {"passThrough": ["CI"]},`, `"environment": {},`, 1)
		}, "passThrough"},
		{"sensitive env name", func(s string) string {
			return strings.Replace(s, `"passThrough": ["CI"]`, `"passThrough": ["GITHUB_TOKEN"]`, 1)
		}, "sensitive"},
		{"lowercase env name", func(s string) string { return strings.Replace(s, `"passThrough": ["CI"]`, `"passThrough": ["ci"]`, 1) }, "identifier"},
		{"empty install argv", func(s string) string {
			return strings.Replace(s, `"install": {"argv": ["pnpm", "install", "--frozen-lockfile"], "timeoutSeconds": 600}`, `"install": {"argv": [], "timeoutSeconds": 600}`, 1)
		}, "argv"},
		{"no required check", func(s string) string { return strings.Replace(s, `"required": true`, `"required": false`, 1) }, "required"},
		{"external route", func(s string) string {
			return strings.Replace(s, `"routes": ["/"]`, `"routes": ["https://external.example"]`, 1)
		}, "routes"},
		{"traversal health path", func(s string) string { return strings.Replace(s, `"healthPath": "/"`, `"healthPath": "/../admin"`, 1) }, "healthPath"},
		{"over-cap cpu", func(s string) string { return strings.Replace(s, `"cpu": 2`, `"cpu": 16`, 1) }, "cpu"},
		{"over-cap timeout", func(s string) string {
			return strings.Replace(s, `"attemptTimeoutSeconds": 1800`, `"attemptTimeoutSeconds": 99999`, 1)
		}, "attemptTimeoutSeconds"},
		{"duplicate check id", func(s string) string {
			return strings.Replace(s, `"checks": [{"id": "lint"`,
				`"checks": [{"id": "lint", "name": "A", "argv": ["true"], "required": false, "timeoutSeconds": 10}, {"id": "lint"`, 1)
		}, "duplicated"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.mutate(base)))
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Parse() error = %v, want *ValidationError", err)
			}
			if !strings.Contains(err.Error(), test.wantSub) {
				t.Fatalf("Parse() error = %q, want it to mention %q", err.Error(), test.wantSub)
			}
		})
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	for _, data := range []string{"", "not json", `{"manifestVersion":1} trailing`, `[1,2]`} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", data)
		}
	}
}
