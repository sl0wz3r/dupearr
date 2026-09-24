package deploy

import (
	"regexp"
	"strings"
	"testing"
)

var (
	changelogRelease = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+[^\]]*)\] - (\d{4}-\d{2}-\d{2})\s*$`)
	templateChanges  = regexp.MustCompile(`(?s)<Changes>(.*?)</Changes>`)
	changesHeading   = regexp.MustCompile(`(?m)^### (\S+) \((\d{4}-\d{2}-\d{2})\)`)
)

// The Unraid template's <Changes> is the change log Community Applications shows for the app, and
// <Date> puts it in CA's "Updated Apps" list. Both are maintained by hand (the template source and
// unraid/ca/publish.env), so a release could leave them naming the previous version: pin that the
// newest entry of each names the newest release in CHANGELOG.md.
func TestUnraidTemplateChangesMatchChangelog(t *testing.T) {
	release := changelogRelease.FindStringSubmatch(readRepoFile(t, "CHANGELOG.md"))
	if release == nil {
		t.Fatal("CHANGELOG.md: no released version (## [X.Y.Z] - YYYY-MM-DD)")
	}
	version, date := release[1], release[2]

	env := readRepoFile(t, "unraid/ca/publish.env")
	for _, kv := range [][2]string{{"VERSION", version}, {"RELEASE_DATE", date}} {
		m := regexp.MustCompile(`(?m)^` + kv[0] + `=(.*)$`).FindStringSubmatch(env)
		if m == nil || strings.Trim(strings.TrimSpace(m[1]), `"'`) != kv[1] {
			t.Errorf("unraid/ca/publish.env: want %s=%s (the newest release in CHANGELOG.md)", kv[0], kv[1])
		}
	}

	for _, file := range []string{"unraid/ca/dupearr.xml.tmpl", "unraid/dupearr.xml"} {
		changes := templateChanges.FindStringSubmatch(readRepoFile(t, file))
		if changes == nil {
			t.Errorf("%s: no <Changes> element", file)
			continue
		}
		headings := changesHeading.FindAllStringSubmatch(changes[1], -1)
		if len(headings) == 0 || headings[0][1] != version || headings[0][2] != date {
			t.Errorf("%s: <Changes> must start with \"### %s (%s)\", the newest release in CHANGELOG.md", file, version, date)
		}
		seen := map[string]bool{}
		for _, h := range headings {
			if seen[h[1]] {
				t.Errorf("%s: <Changes> lists %s twice", file, h[1])
			}
			seen[h[1]] = true
		}
	}

	if !strings.Contains(readRepoFile(t, "unraid/dupearr.xml"), "<Date>"+date+"</Date>") {
		t.Errorf("unraid/dupearr.xml: want <Date>%s</Date>; run make ca-template", date)
	}
}
