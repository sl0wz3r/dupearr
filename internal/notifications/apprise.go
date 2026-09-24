package notifications

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type apprisePayload struct {
	URLs   string `json:"urls,omitempty"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Type   string `json:"type"`   // info | success | warning | failure
	Format string `json:"format"` // text | markdown | html
	Tag    string `json:"tag,omitempty"`
}

// appriseType maps severities onto Apprise notification types.
func appriseType(severity string) string {
	switch severity {
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "failure"
	default:
		return "info"
	}
}

// appriseURLs normalizes the "urls" textarea (one per line or comma-separated; # comments) into
// Apprise's comma-separated form.
func appriseURLs(v string) string {
	var out []string
	for _, line := range strings.Split(v, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, u := range strings.Split(line, ",") {
			if u = strings.TrimSpace(u); u != "" {
				out = append(out, u)
			}
		}
	}
	return strings.Join(out, ",")
}

// sendApprise posts to an Apprise API server: /notify/{configKey} (stateful, stored config) or
// /notify/ with the URLs in the body (stateless).
func (s *Service) sendApprise(ctx context.Context, set *settings, m Message) error {
	payload := apprisePayload{
		Title:  truncate(m.Title, 250),
		Body:   truncate(plainText(m), 8000),
		Type:   appriseType(m.Severity),
		Format: "text",
		Tag:    set.str("tag"),
	}
	base, err := url.Parse(set.str("serverUrl"))
	if err != nil {
		return errors.New("invalid server URL")
	}
	var endpoint *url.URL
	if key := set.str("configKey"); key != "" {
		if !reAppriseKey.MatchString(key) {
			return errors.New("invalid config key")
		}
		endpoint = base.JoinPath("notify", key)
	} else {
		payload.URLs = appriseURLs(set.str("urls"))
		if payload.URLs == "" {
			return errors.New("no config key or Apprise URLs configured")
		}
		endpoint = withTrailingSlash(base.JoinPath("notify"))
	}
	req, err := jsonRequest(endpoint.String(), payload)
	if err != nil {
		return err
	}
	if user := set.str("username"); user != "" {
		req.header.Set("Authorization", basicAuth(user, set.str("password")))
	}
	res, err := s.do(ctx, req)
	if err != nil {
		return err
	}
	switch {
	case res.status == http.StatusNoContent:
		// UNVERIFIED against every apprise-api release: 204 is used for "no configuration found
		// for this key" rather than success, so never report it as delivered.
		return errors.New("apprise found no configuration to notify (HTTP 204); check the config key and tag")
	case res.status == http.StatusFailedDependency:
		return res.statusError(firstNonEmpty(res.jsonField("error"), "one or more Apprise notifications failed"))
	case !res.ok2xx():
		return res.statusError(res.jsonField("error"))
	}
	return nil
}
