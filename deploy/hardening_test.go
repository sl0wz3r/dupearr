// Package deploy holds no code: its tests pin the security properties of the files Dupearr ships
// for deployment (Dockerfiles, CI workflows, the compose file, the Unraid template, the systemd
// unit and the container entrypoint). They are configuration, so nothing else would notice when
// an edit drops one of them. docker/test-image.sh checks the container's behaviour itself.
package deploy

import (
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// readRepoFile returns a file's content by its path from the repository root.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// existingRepoFiles returns those of rels that exist, by path from the repository root. The private
// repository carries the Gitea workflows (.gitea/workflows) next to the GitHub ones; the public
// repository only has .github/workflows.
func existingRepoFiles(t *testing.T, rels ...string) []string {
	t.Helper()
	var found []string
	for _, rel := range rels {
		_, err := os.Stat(filepath.Join("..", filepath.FromSlash(rel)))
		switch {
		case err == nil:
			found = append(found, rel)
		case !errors.Is(err, fs.ErrNotExist):
			t.Fatalf("stat %s: %v", rel, err)
		}
	}
	return found
}

// workflowFiles lists the Gitea and GitHub workflow files.
func workflowFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, dir := range []string{".gitea/workflows", ".github/workflows"} {
		for _, ext := range []string{"*.yml", "*.yaml"} {
			m, err := filepath.Glob(filepath.Join("..", dir, ext))
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range m {
				rel, err := filepath.Rel("..", f)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, filepath.ToSlash(rel))
			}
		}
	}
	if len(files) == 0 {
		t.Fatal("no workflow files found")
	}
	return files
}

var imageDigest = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// SEC-029: a base image referenced by a mutable tag can be re-pointed upstream; the digest pins
// exactly what is built. The Dockerfile frontend (# syntax=) runs during the build too.
func TestBaseImagesPinnedByDigest(t *testing.T) {
	for _, file := range []string{"Dockerfile", "docker/fakemedia.Dockerfile"} {
		stages := map[string]bool{}
		froms := 0
		for i, line := range strings.Split(readRepoFile(t, file), "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, "# syntax="); ok {
				if !imageDigest.MatchString(strings.TrimSpace(v)) {
					t.Errorf("%s:%d: the Dockerfile frontend is not pinned by digest: %q", file, i+1, line)
				}
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.EqualFold(fields[0], "FROM") {
				continue
			}
			froms++
			args := fields[1:]
			for len(args) > 0 && strings.HasPrefix(args[0], "--") {
				args = args[1:]
			}
			if len(args) == 0 {
				t.Errorf("%s:%d: FROM without an image: %q", file, i+1, line)
				continue
			}
			image := args[0]
			if len(args) >= 3 && strings.EqualFold(args[1], "AS") {
				stages[strings.ToLower(args[2])] = true
			}
			if stages[strings.ToLower(image)] && !strings.Contains(image, ":") {
				continue // an earlier build stage
			}
			if !imageDigest.MatchString(image) {
				t.Errorf("%s:%d: base image %q is not pinned by digest (image:tag@sha256:...)", file, i+1, image)
			}
		}
		if froms == 0 {
			t.Errorf("%s: no FROM lines found", file)
		}
	}
}

// A digest pins the base image's own packages as they were on that day. The runtime image must
// still take the Alpine branch's security fixes at build time (busybox runs the entrypoint as root),
// since nothing else gates on them the way docker/check-go-version.sh does for Go.
func TestRuntimeImageUpgradesBasePackages(t *testing.T) {
	text := readRepoFile(t, "Dockerfile")
	lines := strings.Split(text, "\n")
	last := -1
	for i, line := range lines {
		if f := strings.Fields(line); len(f) > 0 && strings.EqualFold(f[0], "FROM") {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("Dockerfile: no FROM lines")
	}
	if !regexp.MustCompile(`(?m)^RUN apk upgrade --no-cache\b`).MatchString(strings.Join(lines[last:], "\n")) {
		t.Error("Dockerfile: the runtime stage does not run `apk upgrade --no-cache` (the digest-pinned base would miss Alpine's security fixes)")
	}
}

var (
	usesLine = regexp.MustCompile(`^\s*(?:-\s+)?uses:\s*(\S+)(.*)$`)
	// owner/repo[/path]@<40 hex> # vX.Y.Z
	pinnedAction = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}$`)
	versionNote  = regexp.MustCompile(`^\s*#\s*v\d+\.\d+\.\d+\s*$`)
)

// SEC-029: an action referenced by a tag runs whatever the tag points to when the job starts, with
// the job's secrets (the release job hands it the registry token). A full commit SHA cannot move.
func TestWorkflowActionsPinnedToCommits(t *testing.T) {
	total := 0
	for _, file := range workflowFiles(t) {
		for i, line := range strings.Split(readRepoFile(t, file), "\n") {
			m := usesLine.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			total++
			ref, rest := strings.Trim(m[1], `"'`), m[2]
			switch {
			case strings.HasPrefix(ref, "./"):
				// an action in this repository
			case strings.HasPrefix(ref, "docker://"):
				if !imageDigest.MatchString(ref) {
					t.Errorf("%s:%d: container action %q is not pinned by digest", file, i+1, ref)
				}
			case !pinnedAction.MatchString(ref):
				t.Errorf("%s:%d: action %q is not pinned to a full commit SHA", file, i+1, ref)
			case !versionNote.MatchString(rest):
				t.Errorf("%s:%d: pinned action %q lacks its release as a comment (# vX.Y.Z)", file, i+1, ref)
			}
		}
	}
	if total == 0 {
		t.Fatal("no uses: lines found")
	}
}

var govulncheckPinned = regexp.MustCompile(`go run golang\.org/x/vuln/cmd/govulncheck@v\d+\.\d+\.\d+ \./\.\.\.`)

// SEC-029: CI must fail on known, reachable vulnerabilities in the Go code and the UI's runtime
// dependencies, and a release must not ship an image built with an outdated Go patch release
// (the golang base image is pinned by digest, so standard library fixes need a digest bump).
func TestWorkflowsGateOnKnownVulnerabilities(t *testing.T) {
	ci := existingRepoFiles(t, ".gitea/workflows/ci.yml", ".github/workflows/ci.yml")
	releases := existingRepoFiles(t, ".gitea/workflows/release.yml", ".github/workflows/release.yml")
	if len(ci) == 0 || len(releases) == 0 {
		t.Fatalf("want a CI and a release workflow, found CI %q and release %q", ci, releases)
	}
	for _, file := range append(ci, releases...) {
		text := readRepoFile(t, file)
		if !govulncheckPinned.MatchString(text) {
			t.Errorf("%s: no govulncheck step with a pinned version (go run golang.org/x/vuln/cmd/govulncheck@vX.Y.Z ./...)", file)
		}
		if !strings.Contains(text, "npm audit --omit=dev") {
			t.Errorf("%s: no npm audit step for the web UI's runtime dependencies", file)
		}
		if !strings.Contains(text, "sh docker/check-go-version.sh") {
			t.Errorf("%s: the image's Go version is not checked (docker/check-go-version.sh)", file)
		}
	}
	for _, file := range releases {
		release := readRepoFile(t, file)
		if !regexp.MustCompile(`(?m)run: sh docker/check-go-version\.sh dupearr:release-test\s*$`).MatchString(release) {
			t.Errorf("%s: the Go version check of the release image must fail the job (no --warn)", file)
		}
	}
}

// workflowSteps splits a workflow into its steps (the text from one "      - " item to the next,
// without comment lines); job headers and top-level keys end a step too.
func workflowSteps(text string) []string {
	var steps []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			steps = append(steps, strings.Join(cur, "\n"))
		}
		cur = nil
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue // comments configure nothing
		}
		if strings.HasPrefix(line, "      - ") || (len(line) > 0 && !strings.HasPrefix(line, " ")) ||
			strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") {
			flush()
		}
		cur = append(cur, line)
	}
	flush()
	return steps
}

// SEC-011: plain HTTP to the registry exposes the registry token and lets anyone on the path swap
// the image. It must be an explicit opt-in (repository variable), never the default, and the
// release must publish the image digest so users can pin a verified pull.
func TestReleasePlainHTTPRegistryIsOptIn(t *testing.T) {
	const file = ".gitea/workflows/release.yml"
	if len(existingRepoFiles(t, file)) == 1 {
		checkGiteaReleasePlainHTTPOptIn(t, file)
	} else {
		t.Logf("%s is not present (public repository layout): checking the Makefile only", file)
	}

	makefile := readRepoFile(t, "Makefile")
	if !regexp.MustCompile(`(?m)^REGISTRY_HTTP\s*\?=\s*false\s*$`).MatchString(makefile) {
		t.Error("Makefile: REGISTRY_HTTP must default to false (plain HTTP only on explicit opt-in)")
	}
	// A buildx builder keeps the BuildKit config it was created with: one made with http = true
	// (including the single "dupearr-builder" of earlier versions) must not serve a push that did
	// not opt in, so the builder's name depends on the transport.
	if !regexp.MustCompile(`(?m)^DOCKER_BUILDER\s*\?=.*\$\(filter true,\$\(REGISTRY_HTTP\)\)`).MatchString(makefile) ||
		regexp.MustCompile(`(?m)^DOCKER_BUILDER\s*\?=\s*dupearr-builder\s*$`).MatchString(makefile) {
		t.Error("Makefile: DOCKER_BUILDER must be named per transport (REGISTRY_HTTP), never reuse a plain-HTTP builder")
	}
}

// checkGiteaReleasePlainHTTPOptIn checks the Gitea release workflow's plain-HTTP opt-in (SEC-011, SEC-047).
func checkGiteaReleasePlainHTTPOptIn(t *testing.T, file string) {
	t.Helper()
	text := readRepoFile(t, file)
	optIn := regexp.MustCompile(`(?m)^\s+if: vars\.REGISTRY_PLAIN_HTTP == 'true'\s*$`)
	plainSteps, buildxSteps := 0, 0
	for _, step := range workflowSteps(text) {
		if strings.Contains(step, "docker/setup-buildx-action@") {
			buildxSteps++
		}
		if !strings.Contains(step, "http = true") && !strings.Contains(step, "insecure = true") {
			continue
		}
		plainSteps++
		if !optIn.MatchString(step) {
			t.Errorf("%s: a step configures a plain-HTTP registry without the REGISTRY_PLAIN_HTTP opt-in:\n%s", file, step)
		}
	}
	if buildxSteps < 2 || plainSteps == 0 {
		t.Errorf("%s: want an HTTPS buildx step plus an opt-in plain-HTTP one, found %d buildx step(s), %d plain-HTTP", file, buildxSteps, plainSteps)
	}
	// The opt-in must protect the tokens too, not only the push: the Docker daemon behind
	// docker/login-action falls back to plain HTTP on its own for an insecure registry, and the
	// release step sends RELEASE_TOKEN to GITHUB_API_URL, whatever its scheme.
	httpsCheck := regexp.MustCompile(`(?m)^\s+if: vars\.REGISTRY_PLAIN_HTTP != 'true'\s*$`)
	checked, loggedIn, guarded := false, false, false
	for _, step := range workflowSteps(text) {
		switch {
		case strings.Contains(step, "docker/login-action@"):
			loggedIn = true
			if !checked {
				t.Errorf("%s: docker/login-action runs before an HTTPS check of the registry (without the REGISTRY_PLAIN_HTTP opt-in)", file)
			}
		case httpsCheck.MatchString(step) && strings.Contains(step, `"https://${REGISTRY}/v2/"`):
			checked = true
		case strings.Contains(step, "secrets.RELEASE_TOKEN"):
			guarded = strings.Contains(step, "PLAIN_HTTP: ${{ vars.REGISTRY_PLAIN_HTTP }}") &&
				strings.Contains(step, "https://*) ;;") && strings.Contains(step, `if [ "${PLAIN_HTTP:-}" != true ]; then`)
		}
	}
	if !loggedIn || !guarded {
		t.Errorf("%s: the release step must refuse a plain-HTTP GITHUB_API_URL without the REGISTRY_PLAIN_HTTP opt-in (login found: %v, guard found: %v)", file, loggedIn, guarded)
	}
	for _, want := range []string{
		"digest: ${{ steps.push.outputs.digest }}",
		"DIGEST: ${{ needs.image.outputs.digest }}",
		"TOKEN: ${{ secrets.RELEASE_TOKEN || secrets.REGISTRY_TOKEN }}",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: missing %q", file, want)
		}
	}
	if !regexp.MustCompile(`body="[^"\n]*\$\{DIGEST\}`).MatchString(text) {
		t.Errorf("%s: the release notes do not name the image digest", file)
	}
}

// The public release workflow (GitHub Actions, ghcr.io over HTTPS) has no plain-HTTP path at all,
// grants write access only to the jobs that publish, attests what it publishes and names the image
// digest in the release notes, so users can pin exactly the image that was tested.
func TestGitHubReleaseWorkflow(t *testing.T) {
	const file = ".github/workflows/release.yml"
	if len(existingRepoFiles(t, file)) == 0 {
		t.Skipf("%s is not present", file)
	}
	text := readRepoFile(t, file)
	for _, step := range workflowSteps(text) {
		for _, bad := range []string{"http = true", "insecure = true", "REGISTRY_PLAIN_HTTP", "insecure-registr"} {
			if strings.Contains(step, bad) {
				t.Errorf("%s: a step configures a plain-HTTP or insecure registry (%q):\n%s", file, bad, step)
			}
		}
	}
	if !regexp.MustCompile(`(?m)^permissions:\n  contents: read\n`).MatchString(text) {
		t.Errorf("%s: the workflow-level token must be read-only (permissions: contents: read)", file)
	}
	for _, want := range []string{
		"packages: write",
		"attestations: write",
		"id-token: write",
		"contents: write",
		"provenance: mode=max",
		"sbom: true",
		"push-to-registry: true",
		"digest: ${{ steps.push.outputs.digest }}",
		"DIGEST: ${{ needs.image.outputs.digest }}",
		"persist-credentials: false",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("%s: missing %q", file, want)
		}
	}
	// Write scopes belong to the publishing jobs, never to the workflow as a whole.
	top, _, _ := strings.Cut(text, "\njobs:")
	for _, line := range strings.Split(top, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "write") {
			t.Errorf("%s: write permission outside a job: %s", file, strings.TrimSpace(line))
		}
	}
	if !regexp.MustCompile(`\$\{IMAGE\}:\$\{VERSION\}@\$\{DIGEST\}`).MatchString(text) {
		t.Errorf("%s: the release notes do not name the pinned image (IMAGE:VERSION@DIGEST)", file)
	}
}

// The capability set the entrypoint needs: chown in /config (CHOWN, DAC_OVERRIDE), su-exec
// (SETUID, SETGID) and tini forwarding signals to the app running as PUID (KILL).
var wantCaps = []string{"CHOWN", "DAC_OVERRIDE", "KILL", "SETGID", "SETUID"}

// yamlList returns the items of the block list under "key:" (at any indentation).
func yamlList(text, key string) []string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != key+":" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		var items []string
		for _, l := range lines[i+1:] {
			trimmed := strings.TrimSpace(l)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if len(l)-len(strings.TrimLeft(l, " ")) <= indent || !strings.HasPrefix(trimmed, "- ") {
				break
			}
			items = append(items, strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), `"'`))
		}
		return items
	}
	return nil
}

// hardeningFlags parses docker run flags into no-new-privileges, cap-drop and cap-add values.
func hardeningFlags(flags []string) (noNewPrivs bool, drop, add []string) {
	for _, f := range flags {
		name, value, _ := strings.Cut(f, "=")
		switch name {
		case "--security-opt":
			if value == "no-new-privileges:true" || value == "no-new-privileges" {
				noNewPrivs = true
			}
		case "--cap-drop":
			drop = append(drop, value)
		case "--cap-add":
			add = append(add, value)
		}
	}
	slices.Sort(add)
	return noNewPrivs, drop, add
}

// SEC-027: the shipped container configurations drop every capability the entrypoint does not
// need and forbid privilege gain; docker/test-image.sh runs the real server with the same flags.
func TestContainerConfigsDropPrivileges(t *testing.T) {
	compose := readRepoFile(t, "deploy/docker-compose.yml")
	if got := yamlList(compose, "security_opt"); !slices.Contains(got, "no-new-privileges:true") {
		t.Errorf("deploy/docker-compose.yml: security_opt = %q, want no-new-privileges:true", got)
	}
	if got := yamlList(compose, "cap_drop"); !slices.Equal(got, []string{"ALL"}) {
		t.Errorf("deploy/docker-compose.yml: cap_drop = %q, want [ALL]", got)
	}
	got := yamlList(compose, "cap_add")
	slices.Sort(got)
	if !slices.Equal(got, wantCaps) {
		t.Errorf("deploy/docker-compose.yml: cap_add = %q, want %q", got, wantCaps)
	}
	if regexp.MustCompile(`(?m)^\s*privileged:\s*true`).MatchString(compose) {
		t.Error("deploy/docker-compose.yml: privileged: true")
	}

	var tmpl struct {
		Privileged  string `xml:"Privileged"`
		ExtraParams string `xml:"ExtraParams"`
	}
	if err := xml.Unmarshal([]byte(readRepoFile(t, "unraid/dupearr.xml")), &tmpl); err != nil {
		t.Fatalf("unraid/dupearr.xml: %v", err)
	}
	if tmpl.Privileged != "false" {
		t.Errorf("unraid/dupearr.xml: Privileged = %q, want false", tmpl.Privileged)
	}
	check := func(where string, flags []string) {
		t.Helper()
		nnp, drop, add := hardeningFlags(flags)
		if !nnp {
			t.Errorf("%s: no --security-opt=no-new-privileges:true in %q", where, flags)
		}
		if !slices.Equal(drop, []string{"ALL"}) {
			t.Errorf("%s: --cap-drop = %q, want ALL", where, drop)
		}
		if !slices.Equal(add, wantCaps) {
			t.Errorf("%s: --cap-add = %q, want %q", where, add, wantCaps)
		}
	}
	check("unraid/dupearr.xml ExtraParams", strings.Fields(tmpl.ExtraParams))

	m := regexp.MustCompile(`(?m)^HARDENED="([^"]*)"$`).FindStringSubmatch(readRepoFile(t, "docker/test-image.sh"))
	if m == nil {
		t.Fatal("docker/test-image.sh: no HARDENED=\"...\" flags (the image test must run with the shipped hardening)")
	}
	check("docker/test-image.sh HARDENED", strings.Fields(m[1]))
}

// systemdService returns the [Service] section's settings (commented lines skipped; a key given
// more than once keeps every value).
func systemdService(text string) map[string][]string {
	settings := map[string][]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";"):
		case strings.HasPrefix(line, "["):
			section = line
		case section == "[Service]":
			k, v, ok := strings.Cut(line, "=")
			if ok {
				settings[strings.TrimSpace(k)] = append(settings[strings.TrimSpace(k)], strings.TrimSpace(v))
			}
		}
	}
	return settings
}

// SEC-028: the native service runs sandboxed by default; only its data directory (and, through a
// documented drop-in, the media folders) stays writable.
func TestSystemdUnitIsSandboxed(t *testing.T) {
	const file = "deploy/systemd/dupearr.service"
	svc := systemdService(readRepoFile(t, file))
	want := map[string]string{
		"User":                    "dupearr",
		"NoNewPrivileges":         "true",
		"PrivateTmp":              "true",
		"PrivateDevices":          "true",
		"ProtectSystem":           "strict",
		"ProtectHome":             "true",
		"ProtectKernelTunables":   "true",
		"ProtectKernelModules":    "true",
		"ProtectKernelLogs":       "true",
		"ProtectControlGroups":    "true",
		"ProtectClock":            "true",
		"ProtectHostname":         "true",
		"RestrictSUIDSGID":        "true",
		"RestrictRealtime":        "true",
		"RestrictNamespaces":      "true",
		"LockPersonality":         "true",
		"MemoryDenyWriteExecute":  "true",
		"SystemCallArchitectures": "native",
		"CapabilityBoundingSet":   "",
		"AmbientCapabilities":     "",
		"StateDirectory":          "dupearr",
	}
	for k, v := range want {
		got, ok := svc[k]
		if !ok || got[len(got)-1] != v {
			t.Errorf("%s: %s = %q, want %q", file, k, got, v)
		}
	}
	if got := svc["SystemCallFilter"]; !slices.Contains(got, "@system-service") {
		t.Errorf("%s: SystemCallFilter = %q, want @system-service", file, got)
	}
	families := strings.Fields(strings.Join(svc["RestrictAddressFamilies"], " "))
	slices.Sort(families)
	if !slices.Equal(families, []string{"AF_INET", "AF_INET6", "AF_UNIX"}) {
		t.Errorf("%s: RestrictAddressFamilies = %q, want AF_INET AF_INET6 AF_UNIX", file, families)
	}
	// The data directory must be the one StateDirectory= makes writable under ProtectSystem=strict.
	if exec := strings.Join(svc["ExecStart"], " "); !strings.Contains(exec, "--data=/var/lib/dupearr") {
		t.Errorf("%s: ExecStart %q does not use the StateDirectory /var/lib/dupearr", file, exec)
	}
}

// SEC-026: /config belongs to PUID, so whatever can write it as PUID can swap the lock file for a
// symlink between any two commands. The entrypoint runs as root there and must never write,
// create or chown through that path (docker/test-image.sh races it for real).
func TestEntrypointNeverWritesOrChownsTheLockPathAsRoot(t *testing.T) {
	const file = "docker/entrypoint.sh"
	checks := []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`\bchown\b[^#\n]*"\$LOCK_FILE"`), "chowns the lock file by name"},
		{regexp.MustCompile(`>>?\s*"\$LOCK_FILE"`), "opens the lock file by name for writing (follows a symlink, creates its target)"},
		{regexp.MustCompile(`<>\s*"\$LOCK_FILE"`), "opens the lock file by name for reading and writing"},
	}
	for i, line := range strings.Split(readRepoFile(t, file), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, c := range checks {
			if c.re.MatchString(line) {
				t.Errorf("%s:%d: %s: %s", file, i+1, c.what, strings.TrimSpace(line))
			}
		}
		// The recursive chown of Dupearr's files in /config: never a file with a second name (a
		// hard link placed in /config would hand over a root-owned file from the same disk).
		if strings.Contains(line, "-exec chown") && !strings.Contains(line, "-links 1") {
			t.Errorf("%s:%d: recursive chown includes hard-linked files (want -links 1): %s", file, i+1, strings.TrimSpace(line))
		}
		// find checks an entry and chown resolves its path again later, so root must only select
		// entries that PUID cannot create (those of other users): an entry of PUID's with another
		// group, below a directory it swaps for a symlink in between, would redirect root's chown.
		// Group-only fixes run as PUID.
		if strings.Contains(line, "-exec chown") && (strings.Contains(line, "-group") || !strings.Contains(line, `! -user "$PUID"`)) {
			t.Errorf("%s:%d: root's recursive chown must select only entries of other users (! -user \"$PUID\", no -group): %s", file, i+1, strings.TrimSpace(line))
		}
		if strings.Contains(line, "-exec chgrp") && !strings.HasPrefix(strings.TrimSpace(line), `su-exec "$PUID:$PGID" `) {
			t.Errorf("%s:%d: the group fix must run as PUID (su-exec), not as root: %s", file, i+1, strings.TrimSpace(line))
		}
	}
}
