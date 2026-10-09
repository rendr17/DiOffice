// Package executionmanifest validates the repository execution contract
// (.dioffice/execution.json). The manifest is untrusted project input: this
// package enforces the canonical v1 schema, the worker-profile allowlist,
// path/command policy, and resource ceilings declared in
// docs/EXECUTION_MANIFEST.md and docs/schemas/execution-manifest.schema.json.
package executionmanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Path is the canonical manifest location inside a checked-out repository.
const Path = ".dioffice/execution.json"

// WorkerProfile is the only operator-allowed worker image profile in v0.1.
const WorkerProfile = "node-22-pnpm-10-playwright"

// ValidationError collects every detected contract violation so the Owner
// receives actionable diagnostics in one pass.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "invalid execution manifest: " + strings.Join(e.Problems, "; ")
}

type Command struct {
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

type Check struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Argv           []string `json:"argv"`
	Required       bool     `json:"required"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

type Viewport struct {
	Name   string `json:"name"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type Manifest struct {
	ManifestVersion  int    `json:"manifestVersion"`
	WorkerProfile    string `json:"workerProfile"`
	WorkingDirectory string `json:"workingDirectory"`
	NetworkProfile   string `json:"networkProfile"`
	Environment      struct {
		PassThrough *[]string `json:"passThrough"`
	} `json:"environment"`
	Commands struct {
		Install Command `json:"install"`
		Start   Command `json:"start"`
		Checks  []Check `json:"checks"`
	} `json:"commands"`
	Preview struct {
		Port                    int        `json:"port"`
		HealthPath              string     `json:"healthPath"`
		ReadinessTimeoutSeconds int        `json:"readinessTimeoutSeconds"`
		Routes                  []string   `json:"routes"`
		Viewports               []Viewport `json:"viewports"`
	} `json:"preview"`
	Resources struct {
		CPU                   float64 `json:"cpu"`
		MemoryMiB             int     `json:"memoryMiB"`
		DiskGiB               int     `json:"diskGiB"`
		AttemptTimeoutSeconds int     `json:"attemptTimeoutSeconds"`
	} `json:"resources"`
}

// Digest returns the canonical SHA-256 hex of the raw manifest bytes. This is
// the digest recorded on tasks.manifest_digest and bound to each attempt.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

var (
	checkIDPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	envNamePattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	sensitiveEnvMarkers = []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "CREDENTIAL", "PRIVATE"}
)

// Parse decodes and fully validates a manifest. Unknown fields, malformed
// values, and policy violations all produce a *ValidationError.
func Parse(data []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, &ValidationError{Problems: []string{"not valid schema JSON: " + err.Error()}}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Manifest{}, &ValidationError{Problems: []string{"trailing content after manifest JSON"}}
	}

	problems := []string{}
	check := func(ok bool, problem string) {
		if !ok {
			problems = append(problems, problem)
		}
	}

	check(manifest.ManifestVersion == 1, "manifestVersion must be 1")
	check(manifest.WorkerProfile == WorkerProfile,
		fmt.Sprintf("workerProfile %q is not in the operator allowlist", manifest.WorkerProfile))
	check(validRelativePath(manifest.WorkingDirectory),
		"workingDirectory must be a relative path inside the workspace without '..'")
	check(manifest.NetworkProfile == "none" || manifest.NetworkProfile == "package-registries",
		"networkProfile must be one of the fixed enums (none, package-registries)")

	if manifest.Environment.PassThrough == nil {
		problems = append(problems, "environment.passThrough is required (use [] for none)")
	} else {
		names := *manifest.Environment.PassThrough
		check(len(names) <= 32, "environment.passThrough accepts at most 32 names")
		seen := map[string]bool{}
		for _, name := range names {
			check(envNamePattern.MatchString(name), "environment.passThrough name "+name+" is not a valid identifier")
			if envNamePattern.MatchString(name) {
				for _, marker := range sensitiveEnvMarkers {
					if strings.Contains(name, marker) {
						problems = append(problems, "environment.passThrough name "+name+" matches a sensitive pattern")
						break
					}
				}
			}
			check(!seen[name], "environment.passThrough duplicates "+name)
			seen[name] = true
		}
	}

	checkCommand("commands.install", manifest.Commands.Install, &problems)
	checkCommand("commands.start", manifest.Commands.Start, &problems)
	check(len(manifest.Commands.Checks) >= 1 && len(manifest.Commands.Checks) <= 20,
		"commands.checks requires 1 to 20 entries")
	requiredChecks := 0
	checkIDs := map[string]bool{}
	for index, checkEntry := range manifest.Commands.Checks {
		field := fmt.Sprintf("commands.checks[%d]", index)
		check(checkIDPattern.MatchString(checkEntry.ID), field+".id must match ^[a-z0-9][a-z0-9._-]{0,63}$")
		check(!checkIDs[checkEntry.ID], field+".id "+checkEntry.ID+" is duplicated")
		checkIDs[checkEntry.ID] = true
		check(len(checkEntry.Name) >= 1 && len(checkEntry.Name) <= 128, field+".name must be 1-128 characters")
		checkArgv(field+".argv", checkEntry.Argv, &problems)
		check(checkEntry.TimeoutSeconds >= 1 && checkEntry.TimeoutSeconds <= 3600,
			field+".timeoutSeconds must be 1-3600")
		if checkEntry.Required {
			requiredChecks++
		}
	}
	check(requiredChecks >= 1, "at least one check must be required")

	check(manifest.Preview.Port >= 1 && manifest.Preview.Port <= 65535, "preview.port must be 1-65535")
	check(validLocalRoute(manifest.Preview.HealthPath), "preview.healthPath must be a local '/' route without '..'")
	check(manifest.Preview.ReadinessTimeoutSeconds >= 5 && manifest.Preview.ReadinessTimeoutSeconds <= 600,
		"preview.readinessTimeoutSeconds must be 5-600")
	check(len(manifest.Preview.Routes) >= 1 && len(manifest.Preview.Routes) <= 20,
		"preview.routes requires 1 to 20 entries")
	routes := map[string]bool{}
	for index, route := range manifest.Preview.Routes {
		check(validLocalRoute(route) && len(route) <= 512,
			fmt.Sprintf("preview.routes[%d] must be a local '/' route without '..'", index))
		check(!routes[route], "preview.routes duplicates "+route)
		routes[route] = true
	}
	check(len(manifest.Preview.Viewports) >= 1 && len(manifest.Preview.Viewports) <= 4,
		"preview.viewports requires 1 to 4 entries")
	for index, viewport := range manifest.Preview.Viewports {
		field := fmt.Sprintf("preview.viewports[%d]", index)
		check(len(viewport.Name) >= 1 && len(viewport.Name) <= 64, field+".name must be 1-64 characters")
		check(viewport.Width >= 1 && viewport.Width <= 3840, field+".width must be 1-3840")
		check(viewport.Height >= 1 && viewport.Height <= 2160, field+".height must be 1-2160")
	}

	check(manifest.Resources.CPU > 0 && manifest.Resources.CPU <= 4, "resources.cpu must be >0 and <=4")
	check(manifest.Resources.MemoryMiB >= 512 && manifest.Resources.MemoryMiB <= 8192,
		"resources.memoryMiB must be 512-8192")
	check(manifest.Resources.DiskGiB >= 1 && manifest.Resources.DiskGiB <= 20,
		"resources.diskGiB must be 1-20")
	check(manifest.Resources.AttemptTimeoutSeconds >= 60 && manifest.Resources.AttemptTimeoutSeconds <= 3600,
		"resources.attemptTimeoutSeconds must be 60-3600")

	if len(problems) > 0 {
		return Manifest{}, &ValidationError{Problems: problems}
	}
	return manifest, nil
}

func checkCommand(field string, command Command, problems *[]string) {
	checkArgv(field+".argv", command.Argv, problems)
	if command.TimeoutSeconds < 1 || command.TimeoutSeconds > 3600 {
		*problems = append(*problems, field+".timeoutSeconds must be 1-3600")
	}
}

func checkArgv(field string, argv []string, problems *[]string) {
	if len(argv) < 1 || len(argv) > 64 {
		*problems = append(*problems, field+" requires a nonempty argv array (max 64)")
		return
	}
	for index, arg := range argv {
		if len(arg) < 1 || len(arg) > 2048 {
			*problems = append(*problems, fmt.Sprintf("%s[%d] must be 1-2048 characters", field, index))
		}
	}
}

// validRelativePath enforces the schema pattern plus the spike's rule that
// workspace-relative paths never use Windows separators or drive letters.
func validRelativePath(path string) bool {
	if len(path) < 1 || len(path) > 256 || strings.HasPrefix(path, "/") ||
		strings.Contains(path, "\\") || strings.ContainsRune(path, 0) {
		return false
	}
	if len(path) >= 2 && path[1] == ':' {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return false
		}
	}
	return true
}

// validLocalRoute enforces a same-origin local route: leading '/', no
// traversal, no host prefix, and no Windows separators.
func validLocalRoute(route string) bool {
	return len(route) >= 1 && len(route) <= 512 && strings.HasPrefix(route, "/") &&
		!strings.HasPrefix(route, "//") && !strings.Contains(route, "\\") &&
		!strings.Contains(route, "..") && !strings.ContainsRune(route, 0)
}
