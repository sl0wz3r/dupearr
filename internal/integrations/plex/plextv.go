package plex

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// plex.tv
// ---------------------------------------------------------------------------

// Pin is a plex.tv sign-in PIN.
type Pin struct {
	ID        int64
	Code      string
	AuthToken string
	ExpiresAt time.Time
}

// Resource is a plex.tv resource (only "server" resources are returned by Servers).
type Resource struct {
	Name             string       `json:"name"`
	ClientIdentifier string       `json:"clientIdentifier"`
	ProductVersion   string       `json:"productVersion"`
	Platform         string       `json:"platform"`
	Owned            bool         `json:"owned"`
	AccessToken      string       `json:"accessToken"`
	Connections      []Connection `json:"connections"`
}

// Connection is one way to reach a Resource.
type Connection struct {
	URI      string `json:"uri"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	Local    bool   `json:"local"`
	Relay    bool   `json:"relay"`
}

// CreatePin creates a strong plex.tv PIN (POST https://plex.tv/api/v2/pins?strong=true). A strong
// PIN is meant for the app.plex.tv auth page (see AuthURL), not for typing at plex.tv/link.
// opts.ClientIdentifier is required and must be the same for CheckPin.
func CreatePin(ctx context.Context, opts Options) (*Pin, error) {
	opts = opts.withDefaults()
	if opts.ClientIdentifier == "" {
		return nil, fmt.Errorf("%w: a client identifier is required", ErrInvalidArgument)
	}
	body, err := plexTVDo(ctx, opts, http.MethodPost, opts.PlexTVURL, "/api/v2/pins", url.Values{"strong": {"true"}}, "")
	if err != nil {
		return nil, err
	}
	pin, err := decodePin(body, time.Now())
	if err != nil {
		return nil, fmt.Errorf("plex.tv: create pin: %w", err)
	}
	if pin.ID <= 0 || pin.Code == "" {
		return nil, errors.New("plex.tv: create pin: response has no pin id/code")
	}
	return pin, nil
}

// CheckPin polls a PIN (GET https://plex.tv/api/v2/pins/{id}); AuthToken is "" until the user
// signs in. An expired/unknown PIN returns ErrNotFound.
func CheckPin(ctx context.Context, opts Options, id int64) (*Pin, error) {
	opts = opts.withDefaults()
	if id <= 0 {
		return nil, fmt.Errorf("%w: pin id %d", ErrInvalidArgument, id)
	}
	if opts.ClientIdentifier == "" {
		return nil, fmt.Errorf("%w: a client identifier is required", ErrInvalidArgument)
	}
	body, err := plexTVDo(ctx, opts, http.MethodGet, opts.PlexTVURL, "/api/v2/pins/"+strconv.FormatInt(id, 10), nil, "")
	if err != nil {
		return nil, err
	}
	pin, err := decodePin(body, time.Now())
	if err != nil {
		return nil, fmt.Errorf("plex.tv: check pin: %w", err)
	}
	if pin.ID == 0 {
		pin.ID = id
	}
	return pin, nil
}

// AuthURL returns the https://app.plex.tv/auth#?... URL the user opens to approve the PIN:
// clientID, code and context[device][product] (the official "auth#?" form, values
// percent-encoded, spaces as %20).
func AuthURL(opts Options, code string) string {
	opts = opts.withDefaults()
	return authAppURL + "#?clientID=" + fragmentEscape(opts.ClientIdentifier) +
		"&code=" + fragmentEscape(strings.TrimSpace(code)) +
		"&context%5Bdevice%5D%5Bproduct%5D=" + fragmentEscape(opts.Product)
}

func fragmentEscape(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }

// Servers lists the account's "server" resources (GET https://clients.plex.tv/api/v2/resources
// ?includeHttps=1&includeRelay=1&includeIPv6=1) with their per-server access tokens and
// connections. Resources whose "provides" does not contain "server" are dropped; ownership is
// reported (only the owner can delete media). The JSON shape is only documented by the community
// spec, so the decoder accepts an array or wrapped object root and falls back to XML.
func Servers(ctx context.Context, opts Options, token string) ([]Resource, error) {
	opts = opts.withDefaults()
	token = sanitizeToken(token)
	if token == "" {
		return nil, fmt.Errorf("%w: a plex.tv token is required", ErrInvalidArgument)
	}
	q := url.Values{"includeHttps": {"1"}, "includeRelay": {"1"}, "includeIPv6": {"1"}}
	body, err := plexTVDo(ctx, opts, http.MethodGet, opts.ClientsPlexTVURL, "/api/v2/resources", q, token)
	if err != nil {
		return nil, err
	}
	dtos, err := decodeResources(body)
	if err != nil {
		// Never include the body: it carries access tokens.
		return nil, fmt.Errorf("plex.tv: GET /api/v2/resources: %w", err)
	}
	out := make([]Resource, 0, len(dtos))
	for _, d := range dtos {
		if !providesServer(d.Provides.String()) {
			continue
		}
		r := Resource{
			Name:             d.Name.String(),
			ClientIdentifier: d.ClientIdentifier.String(),
			ProductVersion:   d.ProductVersion.String(),
			Platform:         d.Platform.String(),
			Owned:            bool(d.Owned),
			AccessToken:      d.AccessToken.String(),
			Connections:      make([]Connection, 0, len(d.Connections)+len(d.Connection)),
		}
		conns := d.Connections
		if len(conns) == 0 {
			conns = d.Connection
		}
		for _, c := range conns {
			r.Connections = append(r.Connections, Connection{
				URI:      c.URI.String(),
				Address:  c.Address.String(),
				Port:     c.Port.Int(),
				Protocol: c.Protocol.String(),
				Local:    bool(c.Local),
				Relay:    bool(c.Relay),
			})
		}
		out = append(out, r)
	}
	return out, nil
}

// OwnerOf reports whether token belongs to the owner of the Plex server whose machine identifier
// is machineID, by finding that server among the token's plex.tv resources (Servers). Plex only
// accepts media deletions made with the owner's token (docs/DECISIONS.md D2). known is false when
// plex.tv does not list the server for this token (e.g. a server-specific or local token); err is
// non-nil when plex.tv could not be asked (unreachable in LAN-only setups, 401 for tokens it does
// not know). Callers should bound ctx tightly and treat errors as "unknown".
func OwnerOf(ctx context.Context, opts Options, token, machineID string) (owned, known bool, err error) {
	machineID = strings.TrimSpace(machineID)
	if machineID == "" {
		return false, false, fmt.Errorf("%w: a machine identifier is required", ErrInvalidArgument)
	}
	list, err := Servers(ctx, opts, token)
	if err != nil {
		return false, false, err
	}
	for _, r := range list {
		if strings.EqualFold(strings.TrimSpace(r.ClientIdentifier), machineID) {
			return r.Owned, true, nil
		}
	}
	return false, false, nil
}

// Ownership is OwnerOf for this client's token (and options, e.g. the plex.tv URLs).
func (c *Client) Ownership(ctx context.Context, machineID string) (owned, known bool, err error) {
	return OwnerOf(ctx, c.opts, c.token, machineID)
}

func providesServer(provides string) bool {
	for _, p := range strings.Split(provides, ",") {
		if strings.EqualFold(strings.TrimSpace(p), "server") {
			return true
		}
	}
	return false
}

// plexTVDo performs one plex.tv request and returns the body. TLS is always verified (unless a
// caller-supplied HTTPClient says otherwise); error bodies are never echoed.
func plexTVDo(ctx context.Context, opts Options, method, base, p string, q url.Values, token string) ([]byte, error) {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid plex.tv base URL", ErrInvalidArgument)
	}
	u.Path = strings.TrimRight(u.Path, "/") + p
	u.RawPath = ""
	u.RawQuery = ""
	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("plex.tv: %s %s: build request: %w", method, p, err)
	}
	opts.setHeaders(req.Header, token)
	resp, err := newHTTPClient(opts, true).Do(req)
	if err != nil {
		if re, ok := asRedirectError(err); ok {
			return nil, fmt.Errorf("plex.tv: %s %s: %w", method, p, re)
		}
		return nil, fmt.Errorf("plex.tv: %s %s: %w", method, p, redactURLError(err))
	}
	defer drainClose(resp.Body)
	if err := checkStatus(resp, method, p, false); err != nil {
		return nil, err
	}
	body, err := readLimited(resp.Body, maxPlexTVBody)
	if err != nil {
		return nil, fmt.Errorf("plex.tv: %s %s: read response: %w", method, p, err)
	}
	return body, nil
}

// ---------------------------------------------------------------------------
// PIN decoding
// ---------------------------------------------------------------------------

type pinDTO struct {
	ID        flexInt    `json:"id"`
	Code      flexString `json:"code"`
	AuthToken flexString `json:"authToken"` // null until claimed
	ExpiresAt flexString `json:"expiresAt"`
	ExpiresIn flexInt    `json:"expiresIn"`
}

// pinXML covers both the v2 attribute form (<pin id=".." code=".." authToken=".."/>) and the
// legacy element form (<pin><id>..</id><code>..</code><auth-token>..</auth-token></pin>).
type pinXML struct {
	ID           string `xml:"id,attr"`
	Code         string `xml:"code,attr"`
	AuthToken    string `xml:"authToken,attr"`
	ExpiresAt    string `xml:"expiresAt,attr"`
	ExpiresIn    string `xml:"expiresIn,attr"`
	IDEl         string `xml:"id"`
	CodeEl       string `xml:"code"`
	AuthTokenEl  string `xml:"auth-token"`
	AuthTokenEl2 string `xml:"authToken"`
	ExpiresAtEl  string `xml:"expires-at"`
}

func decodePin(body []byte, now time.Time) (*Pin, error) {
	b := trimBody(body)
	if len(b) == 0 {
		return nil, errors.New("empty response body")
	}
	var d pinDTO
	if b[0] == '<' {
		var x pinXML
		if err := xml.Unmarshal(b, &x); err != nil {
			return nil, fmt.Errorf("decode XML response: %w", err)
		}
		d = pinDTO{
			ID:        flexInt(parseIntText(firstNonEmpty(x.ID, x.IDEl))),
			Code:      flexString(firstNonEmpty(x.Code, x.CodeEl)),
			AuthToken: flexString(firstNonEmpty(x.AuthToken, x.AuthTokenEl, x.AuthTokenEl2)),
			ExpiresAt: flexString(firstNonEmpty(x.ExpiresAt, x.ExpiresAtEl)),
			ExpiresIn: flexInt(parseIntText(x.ExpiresIn)),
		}
	} else if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	p := &Pin{
		ID:        int64(d.ID),
		Code:      d.Code.String(),
		AuthToken: d.AuthToken.String(),
		ExpiresAt: parsePlexTime(d.ExpiresAt.String()),
	}
	if p.ExpiresAt.IsZero() && d.ExpiresIn > 0 {
		p.ExpiresAt = now.Add(time.Duration(d.ExpiresIn) * time.Second).UTC()
	}
	return p, nil
}

// parsePlexTime parses an RFC 3339 timestamp or a Unix epoch (seconds); zero when unparseable.
func parsePlexTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return unixTime(flexInt(n))
	}
	return time.Time{}
}

// ---------------------------------------------------------------------------
// Resource decoding
// ---------------------------------------------------------------------------

type resourceDTO struct {
	Name             flexString          `json:"name"`
	Product          flexString          `json:"product"`
	ProductVersion   flexString          `json:"productVersion"`
	Platform         flexString          `json:"platform"`
	ClientIdentifier flexString          `json:"clientIdentifier"`
	Provides         flexString          `json:"provides"`
	Owned            flexBool            `json:"owned"`
	AccessToken      flexString          `json:"accessToken"`
	Connections      list[connectionDTO] `json:"connections"`
	Connection       list[connectionDTO] `json:"Connection"` // legacy/XML-converted shape
}

type connectionDTO struct {
	Protocol flexString `json:"protocol"`
	Address  flexString `json:"address"`
	Port     flexInt    `json:"port"`
	URI      flexString `json:"uri"`
	Local    flexBool   `json:"local"`
	Relay    flexBool   `json:"relay"`
	IPv6     flexBool   `json:"IPv6"`
}

// decodeResources accepts a top-level JSON array, an object wrapping it ({"MediaContainer":
// {"Device": [...]}}, {"Device": [...]}, {"resources": [...]}) or a single resource object, and
// falls back to XML (<MediaContainer><Device …><Connection …/></Device></MediaContainer> or
// <resources><resource …><connections><connection …/></connections></resource></resources>).
func decodeResources(body []byte) ([]resourceDTO, error) {
	b := trimBody(body)
	if len(b) == 0 {
		return nil, errors.New("empty response body")
	}
	switch b[0] {
	case '[':
		var l list[resourceDTO]
		if err := json.Unmarshal(b, &l); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		return l, nil
	case '{':
		var obj struct {
			MediaContainer *struct {
				Device list[resourceDTO] `json:"Device"`
			} `json:"MediaContainer"`
			Device    list[resourceDTO] `json:"Device"`
			Resources list[resourceDTO] `json:"resources"`
		}
		if err := json.Unmarshal(b, &obj); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}
		var out []resourceDTO
		if obj.MediaContainer != nil {
			out = append(out, obj.MediaContainer.Device...)
		}
		out = append(out, obj.Device...)
		out = append(out, obj.Resources...)
		if len(out) == 0 {
			var one resourceDTO
			if err := json.Unmarshal(b, &one); err == nil && one.ClientIdentifier.String() != "" {
				out = append(out, one)
			}
		}
		return out, nil
	case '<':
		return decodeResourcesXML(b)
	default:
		return nil, errors.New("unrecognized response format")
	}
}

func decodeResourcesXML(b []byte) ([]resourceDTO, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	var out []resourceDTO
	var cur *resourceDTO
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode XML response: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch strings.ToLower(t.Name.Local) {
			case "device", "resource":
				r := resourceFromAttrs(t.Attr)
				cur = &r
			case "connection":
				if cur != nil {
					cur.Connections = append(cur.Connections, connectionFromAttrs(t.Attr))
				}
			}
		case xml.EndElement:
			switch strings.ToLower(t.Name.Local) {
			case "device", "resource":
				if cur != nil {
					out = append(out, *cur)
					cur = nil
				}
			}
		}
	}
	return out, nil
}

func attrMap(attrs []xml.Attr) map[string]string {
	m := make(map[string]string, len(attrs))
	for _, a := range attrs {
		m[strings.ToLower(a.Name.Local)] = a.Value
	}
	return m
}

func resourceFromAttrs(attrs []xml.Attr) resourceDTO {
	m := attrMap(attrs)
	return resourceDTO{
		Name:             flexString(m["name"]),
		Product:          flexString(m["product"]),
		ProductVersion:   flexString(m["productversion"]),
		Platform:         flexString(m["platform"]),
		ClientIdentifier: flexString(m["clientidentifier"]),
		Provides:         flexString(m["provides"]),
		Owned:            flexBool(parseBoolText(m["owned"])),
		AccessToken:      flexString(m["accesstoken"]),
	}
}

func connectionFromAttrs(attrs []xml.Attr) connectionDTO {
	m := attrMap(attrs)
	return connectionDTO{
		Protocol: flexString(m["protocol"]),
		Address:  flexString(m["address"]),
		Port:     flexInt(parseIntText(m["port"])),
		URI:      flexString(m["uri"]),
		Local:    flexBool(parseBoolText(m["local"])),
		Relay:    flexBool(parseBoolText(m["relay"])),
		IPv6:     flexBool(parseBoolText(m["ipv6"])),
	}
}
