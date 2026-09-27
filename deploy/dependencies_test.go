package deploy

// Dependency tracking and SBOMs: the Renovate configuration, the release workflow's SBOM assets,
// the workflows that run Renovate and the weekly dependency report, and the permissions and time
// limits every workflow must declare. Like hardening_test.go, these pin properties of configuration
// that nothing else would notice losing.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// optionalRepoFile returns a file's content by its path from the repository root, and false when
// the file does not exist (the public repository has no .gitea/workflows).
func optionalRepoFile(t *testing.T, rel string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data), true
}

var jobHeader = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)

// workflowJobs splits a workflow's jobs: section into its jobs, by job id.
func workflowJobs(text string) map[string]string {
	jobs := map[string]string{}
	_, body, ok := strings.Cut(text, "\njobs:\n")
	if !ok {
		return jobs
	}
	name := ""
	var cur []string
	flush := func() {
		if name != "" {
			jobs[name] = strings.Join(cur, "\n")
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if len(line) > 0 && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			break // the next top-level key
		}
		if m := jobHeader.FindStringSubmatch(line); m != nil {
			flush()
			name, cur = m[1], nil
			continue
		}
		cur = append(cur, line)
	}
	flush()
	return jobs
}

var (
	readOnlyDefault = regexp.MustCompile(`(?m)^permissions:\n  contents: read\n(?:\n|[^ ])`)
	jobTimeout      = regexp.MustCompile(`(?m)^    timeout-minutes: \d+\s*$`)
	jobWritePerm    = regexp.MustCompile(`(?m)^      [a-z-]+: write\s*$`)
)

// Every workflow's token is read-only unless a job asks for more (SEC-029), and every job has a time
// limit: a hung job would otherwise hold the self-hosted runner until its 2-hour limit (GitHub: 6).
func TestWorkflowsDefaultToReadOnlyWithTimeouts(t *testing.T) {
	for _, file := range workflowFiles(t) {
		text := readRepoFile(t, file)
		if !readOnlyDefault.MatchString(text) {
			t.Errorf("%s: the workflow-level token must be read-only (permissions: contents: read, nothing else)", file)
		}
		jobs := workflowJobs(text)
		if len(jobs) == 0 {
			t.Errorf("%s: no jobs found", file)
		}
		for name, job := range jobs {
			if !jobTimeout.MatchString(job) {
				t.Errorf("%s: job %s has no timeout-minutes", file, name)
			}
		}
	}
}

// walkJSON calls fn for every object member of a decoded JSON document, with its path.
func walkJSON(v any, path string, fn func(path, key string, value any)) {
	switch v := v.(type) {
	case map[string]any:
		for k, val := range v {
			fn(path, k, val)
			walkJSON(val, path+"."+k, fn)
		}
	case []any:
		for _, val := range v {
			walkJSON(val, path+"[]", fn)
		}
	}
}

// Renovate reads .github/renovate.json. A syntax error there silently stops every update; a rule
// that merges on its own or stops pinning digests would undo SEC-029; and the Go and Node.js release
// lines move only on purpose (go.mod, the Dockerfiles, the workflows together). A moved action tag
// waits for a tick on the Dependency Dashboard, and a minimumReleaseAge never reaches an update
// without a release date (the Renovate image on ghcr.io, the package-lock.json refresh), where
// Renovate would hold it pending for good.
func TestRenovateConfig(t *testing.T) {
	const file = ".github/renovate.json"
	text := readRepoFile(t, file)
	var tree any
	dec := json.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(&tree); err != nil {
		t.Fatalf("%s: not valid JSON: %v", file, err)
	}
	if dec.More() {
		t.Fatalf("%s: data after the JSON object", file)
	}
	var cfg struct {
		Schema              string           `json:"$schema"`
		Extends             []string         `json:"extends"`
		EnabledManagers     []string         `json:"enabledManagers"`
		DependencyDashboard bool             `json:"dependencyDashboard"`
		PackageRules        []map[string]any `json:"packageRules"`
	}
	if err := json.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	if cfg.Schema != "https://docs.renovatebot.com/renovate-schema.json" {
		t.Errorf("%s: $schema = %q", file, cfg.Schema)
	}
	for _, want := range []string{"config:recommended", "docker:pinDigests", "helpers:pinGitHubActionDigestsToSemver"} {
		if !slices.Contains(cfg.Extends, want) {
			t.Errorf("%s: extends %q lacks %q", file, cfg.Extends, want)
		}
	}
	for _, want := range []string{"dockerfile", "github-actions", "gomod", "npm"} {
		if !slices.Contains(cfg.EnabledManagers, want) {
			t.Errorf("%s: enabledManagers %q lacks %q", file, cfg.EnabledManagers, want)
		}
	}
	if !cfg.DependencyDashboard {
		t.Errorf("%s: dependencyDashboard must be true", file)
	}
	walkJSON(tree, "", func(path, key string, value any) {
		switch {
		case (key == "automerge" || key == "platformAutomerge") && value == true:
			t.Errorf("%s: %s.%s is true: updates must never be merged automatically", file, path, key)
		case key == "pinDigests" && value == false:
			t.Errorf("%s: %s.pinDigests is false: images and actions stay pinned by digest", file, path)
		}
	})

	has := func(rule map[string]any, key, value string) bool {
		list, _ := rule[key].([]any)
		return slices.Contains(list, any(value))
	}
	var goDirective, lines, actionsAge, movedTag, renovateImage bool
	for i, rule := range cfg.PackageRules {
		disabled := rule["enabled"] == false
		if disabled && has(rule, "matchManagers", "gomod") && has(rule, "matchDepTypes", "golang") {
			goDirective = true
		}
		if disabled && has(rule, "matchDatasources", "docker") && has(rule, "matchPackageNames", "golang") &&
			has(rule, "matchPackageNames", "node") && has(rule, "matchUpdateTypes", "minor") {
			lines = true
		}
		if has(rule, "matchManagers", "github-actions") && rule["minimumReleaseAge"] != nil {
			actionsAge = true
		}
		if has(rule, "matchManagers", "github-actions") && has(rule, "matchUpdateTypes", "digest") &&
			rule["dependencyDashboardApproval"] == true &&
			(rule["matchDatasources"] == nil || has(rule, "matchDatasources", "github-tags")) {
			movedTag = true
		}
		// ghcr.io has no release dates: a minimumReleaseAge (this rule's or the github-actions one)
		// would leave every Renovate release pending unless missing dates are accepted.
		if has(rule, "matchPackageNames", "ghcr.io/renovatebot/renovate") {
			renovateImage = true
			if rule["minimumReleaseAgeBehaviour"] != "timestamp-optional" {
				t.Errorf("%s: packageRules[%d] (the Renovate image) needs minimumReleaseAgeBehaviour timestamp-optional: ghcr.io has no release dates", file, i)
			}
		}
		// The npm lock file refresh has no release date either; a rule with a minimumReleaseAge that
		// matches it would leave a pending renovate/stability-days status on its pull request.
		if rule["minimumReleaseAge"] != nil && (rule["matchManagers"] == nil || has(rule, "matchManagers", "npm")) &&
			rule["matchDatasources"] == nil &&
			(rule["matchUpdateTypes"] == nil || has(rule, "matchUpdateTypes", "lockFileMaintenance")) {
			t.Errorf("%s: packageRules[%d] sets minimumReleaseAge for the npm lock file refresh too: limit it with matchDatasources or matchUpdateTypes", file, i)
		}
	}
	if !goDirective {
		t.Errorf("%s: no rule keeps Renovate off go.mod's go directive (matchManagers gomod, matchDepTypes golang, enabled false)", file)
	}
	if !lines {
		t.Errorf("%s: no rule keeps the golang and node base images on their minor line", file)
	}
	if !actionsAge {
		t.Errorf("%s: no minimumReleaseAge for github-actions updates", file)
	}
	if !movedTag {
		t.Errorf("%s: no rule holds a moved action tag (github-actions, update type digest) for approval on the Dependency Dashboard", file)
	}
	if !renovateImage {
		t.Errorf("%s: no rule for the Renovate image (matchPackageNames ghcr.io/renovatebot/renovate)", file)
	}
}

// The public release publishes SBOMs of the source and of each image platform, in both formats, next
// to the archives: listed in checksums.txt before the attestation step reads it, so they are
// attested like the archives, and made by a job that can only read.
func TestReleaseWorkflowPublishesSBOMs(t *testing.T) {
	const file = ".github/workflows/release.yml"
	text, ok := optionalRepoFile(t, file)
	if !ok {
		t.Skipf("%s is not present", file)
	}
	jobs := workflowJobs(text)
	sbom, binaries, image := jobs["sbom"], jobs["binaries"], jobs["image"]
	if sbom == "" || binaries == "" || image == "" {
		t.Fatalf("%s: want the jobs image, sbom and binaries", file)
	}
	for _, want := range []string{
		"uses: anchore/sbom-action/download-syft@",
		"scan dir:. ",
		`"registry:${IMAGE}@${DIGEST}"`,
		"for platform in linux/amd64 linux/arm64",
		"spdx-json@2.3=",
		"cyclonedx-json@1.6=",
		"_source.spdx.json",
		"_source.cdx.json",
		"dupearr_${version}_image_",
		"if-no-files-found: error",
	} {
		if !strings.Contains(sbom, want) {
			t.Errorf("%s: the sbom job lacks %q", file, want)
		}
	}
	if m := jobWritePerm.FindString(sbom); m != "" {
		t.Errorf("%s: the sbom job needs no write permission: %s", file, strings.TrimSpace(m))
	}
	if !regexp.MustCompile(`(?m)^    needs: \[[^\]\n]*\bsbom\b[^\]\n]*\]`).MatchString(binaries) {
		t.Errorf("%s: the binaries job must wait for the sbom job", file)
	}
	order := []string{
		"make release VERSION=",
		"uses: actions/download-artifact@",
		"sha256sum dupearr_* > checksums.txt",
		"subject-checksums: dist/checksums.txt",
		"dist/*.spdx.json dist/*.cdx.json",
		"gh release create",
	}
	last := -1
	for _, want := range order {
		i := strings.Index(binaries, want)
		if i < 0 {
			t.Errorf("%s: the binaries job lacks %q", file, want)
			continue
		}
		if i < last {
			t.Errorf("%s: in the binaries job, %q comes too early (want the order %q)", file, want, order)
		}
		last = i
	}
	// BuildKit's in-image SBOM and provenance stay as well.
	for _, want := range []string{"sbom: true", "provenance: mode=max"} {
		if !strings.Contains(image, want) {
			t.Errorf("%s: the image job lacks %q", file, want)
		}
	}
}

// withoutComments drops a YAML file's comment lines (not trailing comments, such as a pin's version).
func withoutComments(text string) string {
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

var (
	scheduled     = regexp.MustCompile(`(?m)^  schedule:\n(?:\s*#.*\n)*    - cron: "[^"]+"\s*$`)
	dispatchable  = regexp.MustCompile(`(?m)^  workflow_dispatch:\s*$`)
	secretEnvLine = regexp.MustCompile(`^\s+[A-Z_]+: \$\{\{ secrets\.[A-Z_]+(?: != '')? \}\}\s*$`)
)

// Renovate and the dependency report run on the private Gitea only. Renovate works on this
// repository alone and gets its token only after a settings check that refuses plain HTTP without
// the REGISTRY_PLAIN_HTTP opt-in (SEC-011); the report needs no secret at all and uploads nothing.
func TestGiteaDependencyWorkflows(t *testing.T) {
	renovate, okRenovate := optionalRepoFile(t, ".gitea/workflows/renovate.yml")
	report, okReport := optionalRepoFile(t, ".gitea/workflows/dependency-report.yml")
	if !okRenovate && !okReport {
		t.Skip(".gitea/workflows is not present (public repository layout)")
	}
	if !okRenovate || !okReport {
		t.Fatalf("want both .gitea/workflows/renovate.yml and dependency-report.yml (found renovate: %v, report: %v)", okRenovate, okReport)
	}
	for file, text := range map[string]string{"renovate.yml": renovate, "dependency-report.yml": report} {
		if !scheduled.MatchString(text) || !dispatchable.MatchString(text) {
			t.Errorf("%s: want a schedule and workflow_dispatch", file)
		}
	}

	for _, want := range []string{
		"uses: docker://ghcr.io/renovatebot/renovate:",
		"RENOVATE_PLATFORM: gitea",
		"RENOVATE_REPOSITORIES: ${{ github.repository }}",
		`RENOVATE_AUTODISCOVER: "false"`,
		`RENOVATE_ONBOARDING: "false"`,
		"PLAIN_HTTP: ${{ vars.REGISTRY_PLAIN_HTTP }}",
		"https://*) ;;",
		`if [ "${PLAIN_HTTP:-}" != true ]; then`,
		`if [ "$HAS_TOKEN" != true ]; then`,
		"::notice::",
	} {
		if !strings.Contains(renovate, want) {
			t.Errorf("renovate.yml: missing %q", want)
		}
	}
	// Secrets only ever reach a step through an env: mapping (never interpolated into a script), and
	// the token only reaches the Renovate step, which runs only when the settings check said so.
	for i, line := range strings.Split(renovate, "\n") {
		if strings.Contains(line, "secrets.") && !strings.HasPrefix(strings.TrimSpace(line), "#") && !secretEnvLine.MatchString(line) {
			t.Errorf("renovate.yml:%d: a secret outside an env: mapping: %s", i+1, strings.TrimSpace(line))
		}
	}
	tokenSteps := 0
	for _, step := range workflowSteps(renovate) {
		if !strings.Contains(step, "${{ secrets.RENOVATE_TOKEN }}") {
			continue
		}
		tokenSteps++
		if !strings.Contains(step, "if: steps.check.outputs.run == 'true'") || !strings.Contains(step, "uses: docker://") {
			t.Errorf("renovate.yml: the step that gets RENOVATE_TOKEN must be the Renovate container step, gated by the settings check:\n%s", step)
		}
	}
	if tokenSteps != 1 {
		t.Errorf("renovate.yml: RENOVATE_TOKEN reaches %d steps, want exactly the Renovate step", tokenSteps)
	}
	// Nothing may let repository content run code next to that token. A checkout would put the
	// repository in Renovate's working directory, and Renovate loads a config.js found there as admin
	// configuration, running it as JavaScript. The admin-only settings below let the repository
	// config run commands, scripts or plugins, or see the environment; NODE_OPTIONS can load code
	// into Renovate's own process.
	code := withoutComments(renovate)
	if strings.Contains(code, "actions/checkout") {
		t.Error("renovate.yml: the Renovate job must not check out the repository (Renovate would load a config.js from it as admin configuration)")
	}
	for _, name := range []string{
		"RENOVATE_CONFIG", "RENOVATE_CONFIG_FILE", "RENOVATE_ADDITIONAL_CONFIG_FILE",
		"RENOVATE_ALLOWED_COMMANDS", "RENOVATE_ALLOWED_POST_UPGRADE_COMMANDS",
		"RENOVATE_ALLOW_SHELL_EXECUTOR_FOR_POST_UPGRADE_COMMANDS", "RENOVATE_ALLOWED_UNSAFE_EXECUTIONS",
		"RENOVATE_ALLOW_SCRIPTS", "RENOVATE_ALLOW_PLUGINS", "RENOVATE_EXPOSE_ALL_ENV", "RENOVATE_ALLOWED_ENV",
		"NODE_OPTIONS",
	} {
		if regexp.MustCompile(`\b` + name + `\b`).MatchString(code) {
			t.Errorf("renovate.yml: sets %s, which lets repository content run code next to RENOVATE_TOKEN", name)
		}
	}

	if strings.Contains(report, "secrets.") {
		t.Error("dependency-report.yml: the report needs no secret")
	}
	for _, bad := range []string{"upload-artifact", "push: true", "curl -X POST", "contents: write"} {
		if strings.Contains(report, bad) {
			t.Errorf("dependency-report.yml: the report must not publish anything (%q)", bad)
		}
	}
	if !strings.Contains(report, "sh scripts/dependency-report.sh") {
		t.Error("dependency-report.yml: does not run scripts/dependency-report.sh")
	}
}

var govulncheckVersion = regexp.MustCompile(`golang\.org/x/vuln/cmd/govulncheck@(v\d+\.\d+\.\d+)`)

// The dependency report runs the same govulncheck release as CI, so both judge the code alike.
func TestDependencyReportScript(t *testing.T) {
	const file = "scripts/dependency-report.sh"
	script := readRepoFile(t, file)
	if !strings.HasPrefix(script, "#!/bin/sh\n") || !regexp.MustCompile(`(?m)^set -eu$`).MatchString(script) {
		t.Errorf("%s: want a POSIX sh script with set -eu", file)
	}
	m := govulncheckVersion.FindStringSubmatch(script)
	ci := govulncheckVersion.FindStringSubmatch(readRepoFile(t, ".github/workflows/ci.yml"))
	if m == nil || ci == nil || m[1] != ci[1] {
		t.Errorf("%s: govulncheck %v, CI %v: keep them in step", file, m, ci)
	}
}
