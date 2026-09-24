package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// smtpOptions configures the fake SMTP server.
type smtpOptions struct {
	implicitTLS   bool
	offerStartTLS bool
	authMechs     string // e.g. "PLAIN LOGIN"; "" = no AUTH extension
	user, pass    string
	rejectRcpt    string // RCPT address answered with 550
}

// receivedMail is one message accepted by the fake server.
type receivedMail struct {
	From     string
	To       []string
	Data     string
	TLS      bool
	AuthUser string
}

// fakeSMTP is a minimal ESMTP server (EHLO, STARTTLS, AUTH PLAIN/LOGIN, MAIL, RCPT, DATA, QUIT).
type fakeSMTP struct {
	t    *testing.T
	opts smtpOptions
	ln   net.Listener
	tls  *tls.Config
	port int

	mu           sync.Mutex
	mails        []receivedMail
	authFails    int
	authAttempts int // AUTH commands received, whatever the mechanism
	wg           sync.WaitGroup
}

// testTLS returns a server certificate valid for 127.0.0.1 and a pool trusting it.
func testTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	srv := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv.Close()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	return &tls.Config{Certificates: srv.TLS.Certificates, MinVersion: tls.VersionTLS12}, pool
}

func startSMTP(t *testing.T, opts smtpOptions, tlsCfg *tls.Config) *fakeSMTP {
	t.Helper()
	return startSMTPOn(t, "127.0.0.1:0", opts, tlsCfg)
}

// startSMTPOn starts the fake server on addr.
func startSMTPOn(t *testing.T, addr string, opts smtpOptions, tlsCfg *tls.Config) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if opts.implicitTLS {
		ln = tls.NewListener(ln, tlsCfg)
	}
	f := &fakeSMTP{t: t, opts: opts, ln: ln, tls: tlsCfg, port: port}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.wg.Add(1)
			go func() {
				defer f.wg.Done()
				f.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeSMTP) received() []receivedMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]receivedMail(nil), f.mails...)
}

func angle(arg string) string {
	if i, j := strings.IndexByte(arg, '<'), strings.IndexByte(arg, '>'); i >= 0 && j > i {
		return arg[i+1 : j]
	}
	return strings.TrimSpace(arg)
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	tp := textproto.NewConn(conn)
	isTLS := f.opts.implicitTLS
	authed := ""
	var cur *receivedMail
	reply := func(format string, args ...any) bool { return tp.PrintfLine(format, args...) == nil }
	checkCreds := func(user, pass string) {
		if user == f.opts.user && pass == f.opts.pass {
			authed = user
			reply("235 2.7.0 Authentication successful")
			return
		}
		f.mu.Lock()
		f.authFails++
		f.mu.Unlock()
		reply("535 5.7.8 Authentication credentials invalid")
	}
	decode := func(s string) string {
		b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
		return string(b)
	}
	if !reply("220 fake.test ESMTP ready") {
		return
	}
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO", "HELO":
			lines := []string{"fake.test greets you"}
			if f.opts.offerStartTLS && !isTLS {
				lines = append(lines, "STARTTLS")
			}
			if f.opts.authMechs != "" {
				lines = append(lines, "AUTH "+f.opts.authMechs)
			}
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				reply("250%s%s", sep, l)
			}
		case "STARTTLS":
			reply("220 2.0.0 Ready to start TLS")
			tlsConn := tls.Server(conn, f.tls)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			tp = textproto.NewConn(conn)
			isTLS = true
		case "AUTH":
			f.mu.Lock()
			f.authAttempts++
			f.mu.Unlock()
			mech, initial, _ := strings.Cut(arg, " ")
			switch strings.ToUpper(mech) {
			case "PLAIN":
				if initial == "" {
					reply("334 ")
					if initial, err = tp.ReadLine(); err != nil {
						return
					}
				}
				parts := strings.Split(decode(initial), "\x00")
				if len(parts) != 3 {
					reply("501 malformed")
					continue
				}
				checkCreds(parts[1], parts[2])
			case "LOGIN":
				reply("334 %s", base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, err := tp.ReadLine()
				if err != nil {
					return
				}
				reply("334 %s", base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, err := tp.ReadLine()
				if err != nil {
					return
				}
				checkCreds(decode(u), decode(p))
			default:
				reply("504 unsupported mechanism")
			}
		case "MAIL":
			cur = &receivedMail{From: angle(arg), TLS: isTLS, AuthUser: authed}
			reply("250 2.1.0 OK")
		case "RCPT":
			addr := angle(arg)
			if cur == nil {
				reply("503 need MAIL first")
				continue
			}
			if addr == f.opts.rejectRcpt {
				reply("550 5.1.1 no such user")
				continue
			}
			cur.To = append(cur.To, addr)
			reply("250 2.1.5 OK")
		case "DATA":
			reply("354 go ahead")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			cur.Data = string(data)
			f.mu.Lock()
			f.mails = append(f.mails, *cur)
			f.mu.Unlock()
			cur = nil
			reply("250 2.0.0 queued")
		case "RSET", "NOOP":
			reply("250 OK")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 command not implemented")
		}
	}
}

func emailConfig(port int, kv ...any) map[string]any {
	return with(map[string]any{
		"host": "127.0.0.1", "port": port, "encryption": EncryptionNone,
		"from": "Dupearr <dupearr@example.com>", "to": "alice@example.com, Bob <bob@example.com>",
	}, kv...)
}

// parseMail returns the headers and the decoded text/plain and text/html parts.
func parseMail(t *testing.T, data string) (mail.Header, string, string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatalf("parse message: %v\n%s", err, data)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("content type %q: %v", msg.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var plain, htmlPart string
	for {
		p, err := mr.NextPart() // decodes quoted-printable transparently
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(p)
		switch {
		case strings.HasPrefix(p.Header.Get("Content-Type"), "text/plain"):
			plain = string(b)
		case strings.HasPrefix(p.Header.Get("Content-Type"), "text/html"):
			htmlPart = string(b)
		}
	}
	return msg.Header, plain, htmlPart
}

func TestEmailPlainNoAuth(t *testing.T) {
	srv := startSMTP(t, smtpOptions{}, nil)
	s, _, _ := newTestService(t)
	cfg := newConfig(KindEmail, emailConfig(srv.port, "cc", "carol@example.com, ALICE@example.com", "bcc", "secret-bcc@example.com"))
	m := sampleMessage()
	m.Title = "Löschung fehlgeschlagen <x>"
	if err := sendDirect(t, s, cfg, m); err != nil {
		t.Fatal(err)
	}
	mails := srv.received()
	if len(mails) != 1 {
		t.Fatalf("mails = %d", len(mails))
	}
	got := mails[0]
	if got.From != "dupearr@example.com" || got.TLS || got.AuthUser != "" {
		t.Errorf("mail = %+v", got)
	}
	if strings.Join(got.To, ",") != "alice@example.com,bob@example.com,carol@example.com,secret-bcc@example.com" {
		t.Errorf("recipients = %v", got.To)
	}
	hdr, plain, htmlPart := parseMail(t, got.Data)
	subject, err := new(mime.WordDecoder).DecodeHeader(hdr.Get("Subject"))
	if err != nil || subject != "[Dupearr] Löschung fehlgeschlagen <x>" {
		t.Errorf("subject = %q (%v), raw %q", subject, err, hdr.Get("Subject"))
	}
	if strings.Contains(got.Data, "secret-bcc") {
		t.Error("Bcc recipient leaked into the message")
	}
	if hdr.Get("Cc") == "" || !strings.Contains(hdr.Get("To"), "bob@example.com") || hdr.Get("Message-Id") == "" || hdr.Get("Date") == "" {
		t.Errorf("headers = %v", hdr)
	}
	if !strings.Contains(plain, "Library: Movies") || !strings.Contains(plain, "https://dupearr.example.com/duplicates?status=pending") {
		t.Errorf("plain = %q", plain)
	}
	if !strings.Contains(htmlPart, "&lt;x&gt;") || !strings.Contains(htmlPart, "<th") || !strings.Contains(htmlPart, `href="https://dupearr.example.com/duplicates?status=pending"`) {
		t.Errorf("html = %q", htmlPart)
	}
}

func TestEmailStartTLSAuthPlain(t *testing.T) {
	tlsCfg, pool := testTLS(t)
	srv := startSMTP(t, smtpOptions{offerStartTLS: true, authMechs: "PLAIN LOGIN", user: "mailer", pass: "s3cret"}, tlsCfg)
	s, _, _ := newTestService(t)
	s.smtpRootCAs = pool
	cfg := newConfig(KindEmail, emailConfig(srv.port, "encryption", EncryptionStartTLS, "username", "mailer", "password", "s3cret"))
	if err := s.Test(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	mails := srv.received()
	if len(mails) != 1 || !mails[0].TLS || mails[0].AuthUser != "mailer" {
		t.Fatalf("mails = %+v", mails)
	}
	if !strings.Contains(mails[0].Data, "Dupearr test notification") {
		t.Errorf("data = %s", mails[0].Data)
	}
}

func TestEmailImplicitTLSAuthLogin(t *testing.T) {
	tlsCfg, pool := testTLS(t)
	srv := startSMTP(t, smtpOptions{implicitTLS: true, authMechs: "LOGIN", user: "mailer", pass: "s3cret"}, tlsCfg)
	s, _, _ := newTestService(t)
	s.smtpRootCAs = pool
	cfg := newConfig(KindEmail, emailConfig(srv.port, "encryption", EncryptionTLS, "username", "mailer", "password", "s3cret"))
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	if mails := srv.received(); len(mails) != 1 || !mails[0].TLS || mails[0].AuthUser != "mailer" {
		t.Fatalf("mails = %+v", mails)
	}
}

func TestEmailFailures(t *testing.T) {
	tlsCfg, pool := testTLS(t)
	tests := []struct {
		name    string
		opts    smtpOptions
		kv      []any
		want    string
		noRoots bool
	}{
		{
			name: "starttls required but not offered",
			opts: smtpOptions{},
			kv:   []any{"encryption", EncryptionStartTLS},
			want: "does not support STARTTLS",
		},
		{
			name: "bad credentials",
			opts: smtpOptions{offerStartTLS: true, authMechs: "PLAIN", user: "mailer", pass: "right"},
			kv:   []any{"encryption", EncryptionStartTLS, "username", "mailer", "password", "wrong-pass-SECRET"},
			want: "535",
		},
		{
			name: "no auth offered",
			opts: smtpOptions{offerStartTLS: true},
			kv:   []any{"encryption", EncryptionStartTLS, "username", "mailer", "password", "pw-SECRET"},
			want: "does not offer authentication",
		},
		{
			name: "unsupported mechanism",
			opts: smtpOptions{offerStartTLS: true, authMechs: "XOAUTH2"},
			kv:   []any{"encryption", EncryptionStartTLS, "username", "mailer", "password", "pw-SECRET"},
			want: "no supported authentication mechanism",
		},
		{
			name: "recipient rejected",
			opts: smtpOptions{rejectRcpt: "alice@example.com"},
			want: "550",
		},
		{
			name:    "untrusted certificate",
			opts:    smtpOptions{offerStartTLS: true},
			kv:      []any{"encryption", EncryptionStartTLS},
			want:    "certificate",
			noRoots: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := startSMTP(t, tt.opts, tlsCfg)
			s, _, _ := newTestService(t)
			if !tt.noRoots {
				s.smtpRootCAs = pool
			}
			err := sendDirect(t, s, newConfig(KindEmail, emailConfig(srv.port, tt.kv...)), sampleMessage())
			assertNoLeak(t, err, "SECRET")
			if !strings.Contains(err.Error(), tt.want) || !strings.HasPrefix(err.Error(), "Email: ") {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if len(srv.received()) != 0 {
				t.Fatal("mail accepted despite failure")
			}
		})
	}
}

func TestEmailRefusesCredentialsInPlaintext(t *testing.T) {
	// A non-local server without encryption must never receive the password, even if the
	// config bypassed validation. "::ffff:127.0.0.1" reaches the loopback listener but is not
	// "localhost" for net/smtp.
	const host = "::ffff:127.0.0.1"
	for _, mech := range []string{"PLAIN LOGIN", "LOGIN", "CRAM-MD5"} {
		t.Run(mech, func(t *testing.T) {
			srv := startSMTP(t, smtpOptions{authMechs: mech, user: "u", pass: "pw"}, nil)
			if c, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(srv.port))); err != nil {
				t.Skipf("IPv4-mapped loopback not dialable: %v", err)
			} else {
				_ = c.Close()
			}
			s, _, _ := newTestService(t)
			sch, _ := providerSchema(KindEmail)
			parsed, err := parseSettings(sch, newConfig(KindEmail, emailConfig(srv.port, "host", host, "username", "u", "password", "pw")).Settings)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = s.sendEmail(ctx, parsed, normalizeMessage(sampleMessage()))
			if err == nil || !strings.Contains(err.Error(), "unencrypted") {
				t.Fatalf("err = %v", err)
			}
			srv.mu.Lock()
			fails, attempts := srv.authFails, srv.authAttempts
			srv.mu.Unlock()
			if fails != 0 || attempts != 0 || len(srv.received()) != 0 {
				t.Fatal("credentials were sent in plaintext")
			}
		})
	}
}

func TestEmailContextCancel(t *testing.T) {
	// A server that accepts but never greets: the send must give up when ctx expires.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var conns []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	s, _, _ := newTestService(t)
	port := ln.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = s.Test(ctx, newConfig(KindEmail, emailConfig(port)))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
}

func TestBuildEmailHeaders(t *testing.T) {
	from, _ := mail.ParseAddress("Dupearr <dupearr@example.com>")
	var to []*mail.Address
	for i := 0; i < 60; i++ {
		to = append(to, &mail.Address{Name: "Recipient " + strconv.Itoa(i), Address: "r" + strconv.Itoa(i) + "@example.com"})
	}
	msg, err := buildEmail(from, to, nil, normalizeMessage(Message{Title: "Evil\r\nBcc: attacker@example.com"}), "Dupearr", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := bytes.Cut(msg, []byte("\r\n\r\n"))
	for _, line := range strings.Split(string(head), "\r\n") {
		if len(line) > 998 {
			t.Errorf("header line of %d bytes", len(line))
		}
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Errorf("header injection: %q", line)
		}
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(msg))
	if err != nil {
		t.Fatal(err)
	}
	list, err := parsed.Header.AddressList("To")
	if err != nil || len(list) != 60 {
		t.Fatalf("To parsed %d addresses: %v", len(list), err)
	}
	if parsed.Header.Get("Cc") != "" {
		t.Error("empty Cc header written")
	}
}

func TestSMTPPort(t *testing.T) {
	sch, _ := providerSchema(KindEmail)
	tests := []struct {
		set  map[string]any
		want int
	}{
		{map[string]any{}, 587},
		{map[string]any{"encryption": EncryptionTLS}, 465},
		{map[string]any{"encryption": EncryptionNone}, 25},
		{map[string]any{"encryption": EncryptionTLS, "port": ""}, 465},
		{map[string]any{"encryption": EncryptionTLS, "port": 2465}, 2465},
		{map[string]any{"encryption": EncryptionNone, "port": "2525"}, 2525},
	}
	for _, tt := range tests {
		set, err := parseSettings(sch, newConfig(KindEmail, tt.set).Settings)
		if err != nil {
			t.Fatal(err)
		}
		if got := smtpPort(set); got != tt.want {
			t.Errorf("smtpPort(%v) = %d, want %d", tt.set, got, tt.want)
		}
	}
}

func TestEmailBracketedIPv6Host(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback unavailable:", err)
	}
	_ = ln.Close()
	srv := startSMTPOn(t, "[::1]:0", smtpOptions{}, nil)
	s, _, _ := newTestService(t)
	cfg := newConfig(KindEmail, emailConfig(srv.port, "host", "[::1]"))
	if errs := ValidateConfig(cfg); errs != nil {
		t.Fatalf("ValidateConfig = %+v", errs)
	}
	if err := sendDirect(t, s, cfg, sampleMessage()); err != nil {
		t.Fatal(err)
	}
	if len(srv.received()) != 1 {
		t.Fatal("mail not delivered")
	}
}

func TestSMTPPortMismatch(t *testing.T) {
	tests := []struct {
		set     map[string]any
		wantErr bool
	}{
		{map[string]any{"encryption": EncryptionTLS, "port": 587}, true},
		{map[string]any{"encryption": EncryptionStartTLS, "port": 465}, true},
		{map[string]any{"encryption": EncryptionTLS, "port": 465}, false},
		{map[string]any{"encryption": EncryptionTLS, "port": nil}, false}, // defaults to 465
		{map[string]any{"encryption": EncryptionStartTLS, "port": 587}, false},
		{map[string]any{"encryption": EncryptionTLS, "port": 2465}, false},
		{map[string]any{"encryption": EncryptionNone, "port": 587}, false},
	}
	for _, tt := range tests {
		cfg := newConfig(KindEmail, with(validSettings(KindEmail), "encryption", tt.set["encryption"], "port", tt.set["port"]))
		errs := ValidateConfig(cfg)
		if got := len(errs) > 0; got != tt.wantErr {
			t.Errorf("ValidateConfig(%v) = %+v, wantErr %v", tt.set, errs, tt.wantErr)
		}
		for _, e := range errs {
			if e.PropertyName != "port" {
				t.Errorf("unexpected error %+v", e)
			}
		}
	}
}
