package notifications

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/version"
)

// defaultSMTPPort returns the conventional port of an encryption mode.
func defaultSMTPPort(encryption string) int {
	switch encryption {
	case EncryptionTLS:
		return 465
	case EncryptionNone:
		return 25
	default:
		return 587
	}
}

// smtpPort returns the configured port; when none is set explicitly, the conventional port of the
// encryption mode (the schema default 587 only fits starttls).
func smtpPort(set *settings) int {
	port := defaultSMTPPort(set.str("encryption"))
	if v, k := decodeScalar(set.raw["port"]); k != valueAbsent && strings.TrimSpace(v) != "" {
		if p, ok := set.num("port"); ok && p > 0 && p <= 65535 {
			port = int(p)
		}
	}
	return port
}

// smtpHost returns the configured SMTP host without IPv6 literal brackets ("[::1]" → "::1"), as
// needed by net.JoinHostPort, TLS ServerName and the SMTP AUTH host check.
func smtpHost(set *settings) string {
	h := set.str("host")
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return h
}

// smtpPortMismatch explains an encryption mode used on the other mode's registered port (RFC
// 8314: 465 = implicit TLS, 587 = STARTTLS), which otherwise fails with an obscure handshake
// error or hangs until the timeout. Returns "" when the combination is plausible.
func smtpPortMismatch(encryption string, port int) string {
	switch {
	case encryption == EncryptionTLS && port == 587:
		return "Port 587 expects starttls encryption; use port 465 for tls"
	case encryption == EncryptionStartTLS && port == 465:
		return "Port 465 expects tls encryption; use port 587 for starttls"
	default:
		return ""
	}
}

// parseRecipients parses a comma-separated address list ("" → none).
func parseRecipients(v string) ([]*mail.Address, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	return mail.ParseAddressList(v)
}

// sendEmail delivers m over SMTP (net/smtp), with STARTTLS or implicit TLS as configured.
// Credentials are never sent over an unencrypted connection to a non-local server (net/smtp's
// PLAIN auth enforces this; loginAuth mirrors it).
func (s *Service) sendEmail(ctx context.Context, set *settings, m Message) error {
	host := smtpHost(set)
	encryption := set.str("encryption")
	port := smtpPort(set)
	from, err := mail.ParseAddress(set.str("from"))
	if err != nil {
		return errors.New("invalid from address")
	}
	to, err := parseRecipients(set.str("to"))
	if err != nil || len(to) == 0 {
		return errors.New("invalid or empty to addresses")
	}
	cc, err := parseRecipients(set.str("cc"))
	if err != nil {
		return errors.New("invalid cc addresses")
	}
	bcc, err := parseRecipients(set.str("bcc"))
	if err != nil {
		return errors.New("invalid bcc addresses")
	}
	msg, err := buildEmail(from, to, cc, m, s.instance(), s.now())
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12, RootCAs: s.smtpRootCAs}
	dialer := newDialer()
	var conn net.Conn
	if encryption == EncryptionTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return smtpError(ctx, "connect to "+addr, err)
	}
	// net/smtp has no context support: bound every read/write by the deadline and unblock
	// immediately on cancellation.
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return smtpError(ctx, "SMTP greeting", err)
	}
	defer func() { _ = c.Close() }()

	if err := c.Hello("localhost"); err != nil {
		return smtpError(ctx, "SMTP EHLO", err)
	}
	if encryption == EncryptionStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS; choose tls or none encryption")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return smtpError(ctx, "SMTP STARTTLS", err)
		}
	}
	if user := set.str("username"); user != "" {
		auth, err := chooseAuth(c, user, set.str("password"), host)
		if err != nil {
			return err
		}
		if err := c.Auth(auth); err != nil {
			return smtpError(ctx, "SMTP authentication", err)
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return smtpError(ctx, "SMTP MAIL FROM", err)
	}
	for _, rcpt := range uniqueAddresses(to, cc, bcc) {
		if err := c.Rcpt(rcpt); err != nil {
			return smtpError(ctx, "SMTP RCPT TO <"+rcpt+">", err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return smtpError(ctx, "SMTP DATA", err)
	}
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return smtpError(ctx, "SMTP DATA", err)
	}
	if err := w.Close(); err != nil {
		return smtpError(ctx, "SMTP DATA", err)
	}
	// The message was accepted (250 after DATA); a failed QUIT is not a delivery failure.
	_ = c.Quit()
	return nil
}

// smtpError wraps an SMTP step failure, preferring the context error when cancelled/timed out.
func smtpError(ctx context.Context, step string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return fmt.Errorf("%s: timed out: %w", step, ctxErr)
		}
		return fmt.Errorf("%s: %w", step, ctxErr)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		// The connection deadline (set from the context deadline) can fire a moment before the
		// context itself reports expiry.
		return fmt.Errorf("%s: timed out: %w", step, err)
	}
	var pe textproto.ProtocolError
	if errors.As(err, &pe) {
		// The text quotes whatever the peer sent; for a service that is not an SMTP server (the
		// host and port are free-form) that would echo its banner. Never useful for SMTP either.
		return fmt.Errorf("%s: the server's answer is not SMTP (not an SMTP server, or the wrong port or encryption?)", step)
	}
	return fmt.Errorf("%s: %w", step, err)
}

// uniqueAddresses returns the bare addresses of all lists, de-duplicated case-insensitively.
func uniqueAddresses(lists ...[]*mail.Address) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, a := range l {
			k := strings.ToLower(a.Address)
			if seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, a.Address)
		}
	}
	return out
}

// chooseAuth picks the strongest mechanism the server offers that we support. Credentials are
// never offered over an unencrypted connection to a non-local server, whatever the mechanism:
// net/smtp's PLAIN and loginAuth check this themselves, CRAM-MD5 does not (its response is an
// offline-crackable hash of the password). Validation already refuses such configs; this keeps the
// guarantee local.
func chooseAuth(c *smtp.Client, user, pass, host string) (smtp.Auth, error) {
	if _, isTLS := c.TLSConnectionState(); !isTLS && !isLocalhost(host) {
		return nil, errors.New("refusing to send credentials over an unencrypted connection to a non-local server; choose starttls or tls")
	}
	ok, params := c.Extension("AUTH")
	if !ok {
		return nil, errors.New("SMTP server does not offer authentication (AUTH); remove the username or enable encryption")
	}
	mechs := strings.Fields(strings.ToUpper(params))
	has := func(m string) bool {
		for _, x := range mechs {
			if x == m {
				return true
			}
		}
		return false
	}
	switch {
	case has("PLAIN"):
		return smtp.PlainAuth("", user, pass, host), nil
	case has("LOGIN"):
		return &loginAuth{username: user, password: pass, host: host}, nil
	case has("CRAM-MD5"):
		return smtp.CRAMMD5Auth(user, pass), nil
	default:
		return nil, fmt.Errorf("SMTP server offers no supported authentication mechanism (%s)", params)
	}
}

// loginAuth implements the AUTH LOGIN mechanism (used by e.g. Office 365), which net/smtp lacks.
type loginAuth struct {
	username, password, host string
}

// Start implements smtp.Auth; like smtp.PlainAuth it refuses unencrypted non-local connections.
func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if !server.TLS && !isLocalhost(server.Name) {
		return "", nil, errors.New("unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("wrong host name")
	}
	return "LOGIN", nil, nil
}

// Next implements smtp.Auth.
func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	challenge := strings.ToLower(strings.TrimSpace(string(fromServer)))
	switch {
	case strings.HasPrefix(challenge, "user"):
		return []byte(a.username), nil
	case strings.HasPrefix(challenge, "pass"):
		return []byte(a.password), nil
	default:
		return nil, errors.New("unexpected AUTH LOGIN challenge")
	}
}

// headerEncode encodes a header value (RFC 2047) when it is not plain ASCII.
func headerEncode(v string) string {
	return mime.QEncoding.Encode("utf-8", cleanLine(v))
}

// joinAddresses renders an address list header.
func joinAddresses(list []*mail.Address) string {
	parts := make([]string, 0, len(list))
	total := 0
	for _, a := range list {
		p := a.String()
		total += len(p) + 2
		parts = append(parts, p)
	}
	if total > 900 {
		// Fold long lists so no header line exceeds RFC 5322's 998-character limit.
		return strings.Join(parts, ",\r\n ")
	}
	return strings.Join(parts, ", ")
}

// messageID returns a unique Message-ID in the sender's domain.
func messageID(from *mail.Address) string {
	domain := "dupearr.local"
	if i := strings.LastIndexByte(from.Address, '@'); i >= 0 && isValidHost(from.Address[i+1:]) {
		domain = from.Address[i+1:]
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "<" + hex.EncodeToString(b[:]) + "@" + domain + ">"
}

// buildEmail renders an RFC 5322 multipart/alternative message (plain text + HTML). Bcc
// recipients are deliberately not written to the headers.
func buildEmail(from *mail.Address, to, cc []*mail.Address, m Message, instance string, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	subject := m.Title
	if instance != "" {
		subject = "[" + instance + "] " + subject
	}
	hdr := []struct{ k, v string }{
		{"From", from.String()},
		{"To", joinAddresses(to)},
		{"Cc", joinAddresses(cc)},
		{"Subject", headerEncode(truncate(subject, 200))},
		{"Date", now.Format(time.RFC1123Z)},
		{"Message-ID", messageID(from)},
		{"MIME-Version", "1.0"},
		{"Content-Type", `multipart/alternative; boundary="` + mw.Boundary() + `"`},
		{"X-Mailer", version.UserAgent()},
	}
	var head bytes.Buffer
	for _, h := range hdr {
		if h.v == "" {
			continue
		}
		head.WriteString(h.k + ": " + h.v + "\r\n")
	}
	head.WriteString("\r\n")

	if err := writeQPPart(mw, "text/plain; charset=UTF-8", emailPlain(m)); err != nil {
		return nil, err
	}
	if err := writeQPPart(mw, "text/html; charset=UTF-8", emailHTML(m, instance)); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("build email: %w", err)
	}
	return append(head.Bytes(), buf.Bytes()...), nil
}

// writeQPPart writes one quoted-printable MIME part.
func writeQPPart(mw *multipart.Writer, contentType, body string) error {
	pw, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {contentType},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return fmt.Errorf("build email: %w", err)
	}
	qp := quotedprintable.NewWriter(pw)
	if _, err := qp.Write([]byte(strings.ReplaceAll(body, "\n", "\r\n"))); err != nil {
		return fmt.Errorf("build email: %w", err)
	}
	if err := qp.Close(); err != nil {
		return fmt.Errorf("build email: %w", err)
	}
	return nil
}

// emailPlain renders the text/plain part.
func emailPlain(m Message) string {
	return m.Title + "\n\n" + plainText(m) + "\n"
}

// emailHTML renders the text/html part; every user-supplied string is escaped.
func emailHTML(m Message, instance string) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html><body style=\"font-family:sans-serif;color:#222\">\n")
	b.WriteString("<h2 style=\"margin:0 0 12px\">" + html.EscapeString(m.Title) + "</h2>\n")
	if m.Body != "" {
		b.WriteString("<p style=\"white-space:pre-wrap\">" + html.EscapeString(m.Body) + "</p>\n")
	}
	if len(m.Fields) > 0 {
		b.WriteString("<table style=\"border-collapse:collapse\">\n")
		for _, f := range m.Fields {
			b.WriteString("<tr><th style=\"text-align:left;padding:4px 12px 4px 0;vertical-align:top\">" +
				html.EscapeString(f.Name) + "</th><td style=\"padding:4px 0;white-space:pre-wrap\">" +
				html.EscapeString(f.Value) + "</td></tr>\n")
		}
		b.WriteString("</table>\n")
	}
	if u := m.absURL(); u != "" {
		b.WriteString("<p><a href=\"" + html.EscapeString(u) + "\">" + linkLabel + "</a></p>\n")
	}
	b.WriteString("<p style=\"color:#888;font-size:12px\">" + html.EscapeString(instance) + "</p>\n")
	b.WriteString("</body></html>\n")
	return b.String()
}
