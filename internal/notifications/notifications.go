// Package notifications sends Dupearr events to external services (Settings → Connect): discord,
// slack, telegram, pushover, gotify, ntfy, apprise, webhook, email.
//
// A connection (models.NotificationConfig) stores its provider settings as a JSON object keyed by
// FieldSchema.Name (see Schema). Secret settings (and credential-bearing webhook header values) are
// replaced by MaskedValue in API responses (MaskSecrets) and restored from the stored connection on
// update (MergeSecrets) — only while the destination they are sent to (server URL, webhook URL,
// SMTP host/port) is unchanged, so an edit can never redirect a stored secret to another server.
// The mask itself is rejected by ValidateConfig and by delivery, so it is never saved or sent.
//
// Delivery: Notify is fire-and-forget — it fans out asynchronously to every enabled connection
// subscribed to the event with bounded concurrency and a per-send timeout, logging failures with
// secrets redacted. Test is synchronous and returns the provider error for the UI.
package notifications

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// Field is one key/value line of a Message (rendered as an embed field / table row).
type Field struct {
	Name, Value string
	Inline      bool
	// Paths marks a value that lists file paths on the server: it only reaches connections that
	// include file paths (SettingIncludePaths).
	Paths bool
}

// Message is a provider-independent notification.
type Message struct {
	Event string // models.On*
	Title string
	Body  string
	// BodyWithoutPaths replaces Body for connections that do not include file paths
	// (SettingIncludePaths) when Body names files on the server; "" means Body names none.
	BodyWithoutPaths string
	Fields           []Field
	URL              string // deep link into the Dupearr UI when known
	Severity         string // info|warning|error
}

// FieldSchema describes one provider setting for the UI form.
// Type: text|password|url|number|checkbox|select|textarea.
type FieldSchema struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	HelpText string   `json:"helpText,omitempty"`
	Required bool     `json:"required"`
	Advanced bool     `json:"advanced"`
	Secret   bool     `json:"secret"`
	Options  []string `json:"options,omitempty"`
	Default  any      `json:"default,omitempty"`
}

// ProviderSchema describes one provider (GET /api/v1/notification/schema).
type ProviderSchema struct {
	Kind    string        `json:"kind"`
	Name    string        `json:"name"`
	InfoURL string        `json:"infoUrl,omitempty"`
	Fields  []FieldSchema `json:"fields"`
}

// Delivery limits.
const (
	// SendTimeout bounds every single delivery (one connection, one message).
	SendTimeout = 15 * time.Second
	// maxConcurrentSends bounds simultaneous deliveries across all Notify calls.
	maxConcurrentSends = 4
	// maxPendingDispatches bounds queued Notify calls; beyond it messages are dropped (logged)
	// rather than accumulating goroutines behind a hanging provider.
	maxPendingDispatches = 256
	// listTimeout bounds reading the connections from the store.
	listTimeout = 10 * time.Second
	// defaultInstanceName is used until SetInstanceName is called.
	defaultInstanceName = "Dupearr"
)

// Test notification content.
const (
	TestTitle = "Dupearr test notification"
	testBody  = "This is a test notification from Dupearr. If you can read this, the connection works."
)

// Service dispatches notifications to the configured connections. It is safe for concurrent use.
type Service struct {
	st  store.Store
	log *slog.Logger

	client  *http.Client
	sem     chan struct{}
	pending atomic.Int64
	wg      sync.WaitGroup

	// baseCtx is cancelled by Shutdown; every asynchronous delivery derives from it.
	baseCtx context.Context
	cancel  context.CancelFunc

	mu           sync.RWMutex
	closed       bool
	instanceName string

	// Endpoints and TLS roots; overridden only by tests.
	telegramAPI string
	pushoverAPI string
	smtpRootCAs *x509.CertPool
	now         func() time.Time
}

// New returns a Service reading connections from st. log may be nil (discard).
func New(st store.Store, log *slog.Logger) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		st:           st,
		log:          log,
		client:       newHTTPClient(),
		sem:          make(chan struct{}, maxConcurrentSends),
		baseCtx:      ctx,
		cancel:       cancel,
		instanceName: defaultInstanceName,
		telegramAPI:  DefaultTelegramAPI,
		pushoverAPI:  DefaultPushoverAPI,
		now:          time.Now,
	}
}

// SetInstanceName sets the instance name shown in notifications (config.xml InstanceName; e.g.
// the webhook "instanceName", Discord footer, email subject prefix). Empty restores "Dupearr".
// Safe to call at any time, e.g. from config.Manager.OnChange.
func (s *Service) SetInstanceName(name string) {
	if s == nil {
		return
	}
	name = cleanLine(name)
	if name == "" {
		name = defaultInstanceName
	}
	s.mu.Lock()
	s.instanceName = name
	s.mu.Unlock()
}

// instance returns the current instance name.
func (s *Service) instance() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.instanceName
}

// Notify sends asynchronously to every enabled config whose Triggers include msg.Event. Never blocks
// the caller for network I/O; failures are logged. Calling Notify on a nil *Service is a no-op.
//
// Delivery is deliberately detached from ctx's cancellation (notifications must outlive the
// request or command that triggered them); it is bounded by SendTimeout per connection and stopped
// by Shutdown. Messages are dropped (and logged) after Shutdown or when too many are pending.
func (s *Service) Notify(ctx context.Context, msg Message) {
	if s == nil || s.st == nil {
		return
	}
	if strings.TrimSpace(msg.Event) == "" {
		s.log.Debug("notification without event ignored", "title", msg.Title)
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		s.log.Debug("notification dropped: service is shut down", "event", msg.Event)
		return
	}
	if s.pending.Add(1) > maxPendingDispatches {
		s.pending.Add(-1)
		s.log.Warn("notification dropped: too many pending notifications", "event", msg.Event)
		return
	}
	s.wg.Add(1)
	msg.Fields = slices.Clone(msg.Fields) // the caller may reuse its slice after we return
	go s.dispatch(msg)
}

// dispatch loads the connections and starts one bounded delivery per subscribed connection.
func (s *Service) dispatch(msg Message) {
	defer s.wg.Done()
	defer s.pending.Add(-1)
	defer s.recoverPanic("dispatch", msg.Event)

	ctx, cancel := context.WithTimeout(s.baseCtx, listTimeout)
	cfgs, err := s.st.Notifications().List(ctx)
	cancel()
	if err != nil {
		s.log.Warn("listing notification connections failed", "event", msg.Event, "error", err)
		return
	}
	for _, cfg := range cfgs {
		if !cfg.Enabled || !slices.Contains(cfg.Triggers, msg.Event) {
			continue
		}
		select {
		case s.sem <- struct{}{}:
		case <-s.baseCtx.Done():
			s.log.Debug("notification dropped: service is shut down", "event", msg.Event, "connection", cfg.Name)
			return
		}
		s.wg.Add(1)
		go func(cfg models.NotificationConfig) {
			defer s.wg.Done()
			defer func() { <-s.sem }()
			defer s.recoverPanic("send", msg.Event)
			s.send(cfg, msg)
		}(cfg)
	}
}

// send delivers msg to one connection and logs the outcome.
func (s *Service) send(cfg models.NotificationConfig, msg Message) {
	ctx, cancel := context.WithTimeout(s.baseCtx, SendTimeout)
	defer cancel()
	start := time.Now()
	attrs := []any{"connection", cfg.Name, "id", cfg.ID, "kind", cfg.Kind, "event", msg.Event}
	if err := s.deliver(ctx, cfg, msg); err != nil {
		s.log.Warn("notification failed", append(attrs, "error", err.Error())...)
		return
	}
	s.log.Debug("notification sent", append(attrs, "duration", time.Since(start).Round(time.Millisecond))...)
}

// recoverPanic keeps a misbehaving provider from crashing the process.
func (s *Service) recoverPanic(where, event string) {
	if r := recover(); r != nil {
		s.log.Error("notification panic recovered", "where", where, "event", event,
			"panic", fmt.Sprint(r), "stack", string(debug.Stack()))
	}
}

// Shutdown stops accepting notifications and waits for in-flight deliveries until ctx is done;
// then it cancels whatever is still running and returns ctx.Err(). Safe to call more than once.
func (s *Service) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.cancel()
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}

// Test sends a test message synchronously with an unsaved config and returns the provider error.
//
// The config is validated first (a *ValidationFailedError lists the problems); Enabled and
// Triggers are ignored. Call MergeSecrets first when testing an edited, masked connection. The
// returned error never contains secrets, nor — unless the destination is a provider's own public
// endpoint (Telegram, Pushover, Discord, Slack, ntfy.sh) — any part of the server's response body:
// the test must not become a way to read internal services' answers. That detail is not logged
// either (deliveries withhold it the same way, see httpResult.withhold).
func (s *Service) Test(ctx context.Context, cfg models.NotificationConfig) error {
	if s == nil {
		return errors.New("notifications are not available")
	}
	if errs := ValidateConfig(cfg); len(errs) > 0 {
		return &ValidationFailedError{Errors: errs}
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	msg := Message{
		Event:    EventTest,
		Title:    TestTitle,
		Body:     testBody,
		Severity: SeverityInfo,
		Fields:   []Field{{Name: "Connection", Value: cfg.Name, Inline: true}},
	}
	if p, ok := providerSchema(cfg.Kind); ok {
		msg.Fields = append(msg.Fields, Field{Name: "Provider", Value: p.Name, Inline: true})
	}
	return s.deliver(ctx, cfg, msg)
}

// withoutPaths returns msg without the file paths on the server: the path fields are dropped and
// BodyWithoutPaths (when set) replaces Body. Notifications leave the instance (Discord, Slack,
// Telegram, Pushover, public ntfy servers …), so by default they carry titles, sizes and methods,
// never the library's layout (GAP-14).
func withoutPaths(msg Message) Message {
	if msg.BodyWithoutPaths != "" {
		msg.Body = msg.BodyWithoutPaths
	}
	msg.BodyWithoutPaths = ""
	msg.Fields = slices.DeleteFunc(slices.Clone(msg.Fields), func(f Field) bool { return f.Paths })
	return msg
}

// deliver validates the provider settings and sends msg to one connection. Errors are prefixed
// with the provider name and have every secret redacted.
func (s *Service) deliver(ctx context.Context, cfg models.NotificationConfig, msg Message) error {
	schema, ok := providerSchema(cfg.Kind)
	if !ok {
		return fmt.Errorf("unknown notification kind %q", cfg.Kind)
	}
	set, err := parseSettings(schema, cfg.Settings)
	if err != nil {
		return fmt.Errorf("%s: %w", schema.Name, err)
	}
	// Re-check the stored settings so a connection saved before a rule existed (or edited in the
	// DB) can never produce a malformed request or send the mask as a credential.
	if errs := validateSettings(set); len(errs) > 0 {
		return fmt.Errorf("%s: %w", schema.Name, &ValidationFailedError{Errors: errs})
	}
	if !set.boolean(SettingIncludePaths) {
		msg = withoutPaths(msg)
	}
	msg = normalizeMessage(msg)

	var sendErr error
	switch cfg.Kind {
	case KindDiscord:
		sendErr = s.sendDiscord(ctx, set, msg)
	case KindSlack:
		sendErr = s.sendSlack(ctx, set, msg)
	case KindTelegram:
		sendErr = s.sendTelegram(ctx, set, msg)
	case KindPushover:
		sendErr = s.sendPushover(ctx, set, msg)
	case KindGotify:
		sendErr = s.sendGotify(ctx, set, msg)
	case KindNtfy:
		sendErr = s.sendNtfy(ctx, set, msg)
	case KindApprise:
		sendErr = s.sendApprise(ctx, set, msg)
	case KindWebhook:
		sendErr = s.sendWebhook(ctx, set, msg)
	case KindEmail:
		sendErr = s.sendEmail(ctx, set, msg)
	default:
		sendErr = errors.New("provider not implemented")
	}
	if sendErr == nil {
		return nil
	}
	sensitive := set.sensitiveValues()
	return &redactedError{
		msg:   redact(schema.Name+": "+sendErr.Error(), sensitive),
		cause: sendErr,
	}
}
