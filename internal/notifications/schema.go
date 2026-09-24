package notifications

import "github.com/sl0wz3r/dupearr/internal/models"

// Provider kinds (models.NotificationConfig.Kind).
const (
	KindDiscord  = "discord"
	KindSlack    = "slack"
	KindTelegram = "telegram"
	KindPushover = "pushover"
	KindGotify   = "gotify"
	KindNtfy     = "ntfy"
	KindApprise  = "apprise"
	KindWebhook  = "webhook"
	KindEmail    = "email"
)

// FieldSchema.Type values.
const (
	fieldText     = "text"
	fieldPassword = "password"
	fieldURL      = "url"
	fieldNumber   = "number"
	fieldCheckbox = "checkbox"
	fieldSelect   = "select"
	fieldTextarea = "textarea"
)

// Email encryption modes (email "encryption" setting).
const (
	EncryptionNone     = "none"
	EncryptionStartTLS = "starttls"
	EncryptionTLS      = "tls"
)

// DefaultNtfyServer is used when an ntfy connection has no server URL.
const DefaultNtfyServer = "https://ntfy.sh"

// TriggerOption is one selectable notification trigger (GET /api/v1/notification/triggers).
type TriggerOption struct {
	Value string `json:"value"` // models.On*
	Label string `json:"label"`
}

// TriggerOptions returns every trigger a connection can subscribe to, in display order.
func TriggerOptions() []TriggerOption {
	return []TriggerOption{
		{Value: models.OnDuplicatesFound, Label: "On Duplicates Found"},
		{Value: models.OnFileDeleted, Label: "On File Deleted"},
		{Value: models.OnDeleteFailed, Label: "On Delete Failed"},
		{Value: models.OnScanCompleted, Label: "On Scan Completed"},
		{Value: models.OnHealthIssue, Label: "On Health Issue"},
		{Value: models.OnHealthRestored, Label: "On Health Restored"},
	}
}

// isKnownTrigger reports whether t is one of the models.On* triggers.
func isKnownTrigger(t string) bool {
	for _, o := range TriggerOptions() {
		if o.Value == t {
			return true
		}
	}
	return false
}

// SettingIncludePaths is the setting (every provider) that sends the full server paths of removed
// files with removal notifications. It is off by default: notifications leave the instance, and
// the paths reveal the library's layout (GAP-14).
const SettingIncludePaths = "includePaths"

// includePathsField is the SettingIncludePaths field every provider has (last, advanced).
func includePathsField() FieldSchema {
	return FieldSchema{Name: SettingIncludePaths, Label: "Include File Paths", Type: fieldCheckbox, Advanced: true, Default: false,
		HelpText: "Send the full paths of removed files on the server. Off: removal notifications carry the title, size and " +
			"method only, so the service you send to never learns your folders' layout"}
}

// Schema returns every provider: discord, slack, telegram, pushover, gotify, ntfy, apprise, webhook, email.
//
// A connection's Settings JSON is an object keyed by FieldSchema.Name. Absent (or empty) values fall
// back to FieldSchema.Default. Secret fields are masked in API responses (see MaskSecrets).
// A fresh slice is built on every call, so callers may modify the result.
func Schema() []ProviderSchema {
	out := providers()
	for i := range out {
		out[i].Fields = append(out[i].Fields, includePathsField())
	}
	return out
}

// providers returns the provider schemas without the settings every provider shares.
func providers() []ProviderSchema {
	return []ProviderSchema{
		{
			Kind:    KindDiscord,
			Name:    "Discord",
			InfoURL: "https://support.discord.com/hc/en-us/articles/228383668-Intro-to-Webhooks",
			Fields: []FieldSchema{
				{Name: "webhookUrl", Label: "Webhook URL", Type: fieldURL, Required: true, Secret: true,
					HelpText: "Channel settings → Integrations → Webhooks → Copy Webhook URL"},
				{Name: "username", Label: "Username", Type: fieldText,
					HelpText: "Overrides the webhook's default name (optional)"},
				{Name: "avatarUrl", Label: "Avatar URL", Type: fieldURL, Advanced: true,
					HelpText: "Overrides the webhook's default avatar (optional)"},
			},
		},
		{
			Kind:    KindSlack,
			Name:    "Slack",
			InfoURL: "https://api.slack.com/messaging/webhooks",
			Fields: []FieldSchema{
				{Name: "webhookUrl", Label: "Webhook URL", Type: fieldURL, Required: true, Secret: true,
					HelpText: "Incoming Webhook URL of a Slack app (https://hooks.slack.com/services/…)"},
			},
		},
		{
			Kind:    KindTelegram,
			Name:    "Telegram",
			InfoURL: "https://core.telegram.org/bots/tutorial#obtain-your-bot-token",
			Fields: []FieldSchema{
				{Name: "botToken", Label: "Bot Token", Type: fieldPassword, Required: true, Secret: true,
					HelpText: "Token from @BotFather, e.g. 123456789:ABC-DEF…"},
				{Name: "chatId", Label: "Chat ID", Type: fieldText, Required: true,
					HelpText: "Numeric chat id (groups/channels start with -100) or @channelusername; the bot must be a member"},
				{Name: "topicId", Label: "Topic ID", Type: fieldNumber, Advanced: true,
					HelpText: "Forum topic (message thread) id for supergroups with topics (optional)"},
				{Name: "sendSilently", Label: "Send Silently", Type: fieldCheckbox, Advanced: true, Default: false,
					HelpText: "Deliver without sound"},
			},
		},
		{
			Kind:    KindPushover,
			Name:    "Pushover",
			InfoURL: "https://pushover.net/api",
			Fields: []FieldSchema{
				{Name: "appToken", Label: "Application API Token", Type: fieldPassword, Required: true, Secret: true,
					HelpText: "API token of a Pushover application you created for Dupearr"},
				{Name: "userKey", Label: "User Key", Type: fieldPassword, Required: true, Secret: true,
					HelpText: "Your user (or group) key from the Pushover dashboard"},
				{Name: "devices", Label: "Devices", Type: fieldText,
					HelpText: "Comma-separated device names; empty sends to all devices"},
				{Name: "priority", Label: "Priority", Type: fieldSelect, Default: "0",
					Options:  []string{"-2", "-1", "0", "1", "2"},
					HelpText: "-2 lowest, -1 low, 0 normal, 1 high, 2 emergency (repeats until acknowledged)"},
				{Name: "sound", Label: "Sound", Type: fieldText, Advanced: true,
					HelpText: "Pushover sound name, e.g. pushover, magic, none (optional)"},
				{Name: "retry", Label: "Retry (seconds)", Type: fieldNumber, Advanced: true, Default: 60,
					HelpText: "Emergency priority only: how often to retry (min 30)"},
				{Name: "expire", Label: "Expire (seconds)", Type: fieldNumber, Advanced: true, Default: 3600,
					HelpText: "Emergency priority only: stop retrying after this many seconds (max 10800)"},
			},
		},
		{
			Kind:    KindGotify,
			Name:    "Gotify",
			InfoURL: "https://gotify.net/docs/pushmsg",
			Fields: []FieldSchema{
				{Name: "serverUrl", Label: "Server URL", Type: fieldURL, Required: true,
					HelpText: "Gotify server URL, e.g. http://gotify:80"},
				{Name: "appToken", Label: "App Token", Type: fieldPassword, Required: true, Secret: true,
					HelpText: "Token of the Gotify application Dupearr posts as"},
				{Name: "priority", Label: "Priority", Type: fieldNumber, Default: 5,
					HelpText: "0-10; clients typically alert from 4 and up"},
			},
		},
		{
			Kind:    KindNtfy,
			Name:    "ntfy",
			InfoURL: "https://docs.ntfy.sh/publish/",
			Fields: []FieldSchema{
				{Name: "serverUrl", Label: "Server URL", Type: fieldURL, Default: DefaultNtfyServer,
					HelpText: "ntfy server URL; leave empty for " + DefaultNtfyServer},
				{Name: "topic", Label: "Topic", Type: fieldText, Required: true, Secret: true,
					HelpText: "Topic name (letters, digits, - and _; max 64). On a public server the topic name is the only protection: use a long, random one"},
				{Name: "accessToken", Label: "Access Token", Type: fieldPassword, Secret: true,
					HelpText: "Access token (tk_…) for protected topics; use this or username/password"},
				{Name: "username", Label: "Username", Type: fieldText,
					HelpText: "Username for protected topics (optional)"},
				{Name: "password", Label: "Password", Type: fieldPassword, Secret: true,
					HelpText: "Password for protected topics (optional)"},
				{Name: "priority", Label: "Priority", Type: fieldSelect, Default: "3",
					Options:  []string{"1", "2", "3", "4", "5"},
					HelpText: "1 min, 2 low, 3 default, 4 high, 5 max/urgent"},
				{Name: "tags", Label: "Tags", Type: fieldText, Advanced: true,
					HelpText: "Comma-separated tags or emoji short codes, e.g. dupearr,movie_camera"},
			},
		},
		{
			Kind:    KindApprise,
			Name:    "Apprise",
			InfoURL: "https://github.com/caronc/apprise-api",
			Fields: []FieldSchema{
				{Name: "serverUrl", Label: "Apprise API URL", Type: fieldURL, Required: true,
					HelpText: "Apprise API server URL, e.g. http://apprise:8000"},
				{Name: "configKey", Label: "Config Key", Type: fieldText, Secret: true,
					HelpText: "Key of a configuration stored on the Apprise API server (use this or Apprise URLs)"},
				{Name: "urls", Label: "Apprise URLs", Type: fieldTextarea, Secret: true,
					HelpText: "One or more Apprise URLs (one per line or comma-separated) for stateless notifications"},
				{Name: "tag", Label: "Tag", Type: fieldText,
					HelpText: "Only notify services with this tag (optional)"},
				{Name: "username", Label: "Username", Type: fieldText, Advanced: true,
					HelpText: "HTTP basic auth username, when the Apprise API is behind a proxy (optional)"},
				{Name: "password", Label: "Password", Type: fieldPassword, Advanced: true, Secret: true,
					HelpText: "HTTP basic auth password (optional)"},
			},
		},
		{
			Kind: KindWebhook,
			Name: "Webhook",
			Fields: []FieldSchema{
				{Name: "url", Label: "URL", Type: fieldURL, Required: true,
					HelpText: "Receives a JSON body {eventType, title, message, severity, fields, url, instanceName, timestamp}. The last path segment, id- or token-like path segments and query values are hidden after saving; keep the URL unchanged to reuse the stored one"},
				{Name: "method", Label: "Method", Type: fieldSelect, Default: "POST",
					Options: []string{"POST", "PUT"}},
				{Name: "username", Label: "Username", Type: fieldText,
					HelpText: "HTTP basic auth username (optional)"},
				{Name: "password", Label: "Password", Type: fieldPassword, Secret: true,
					HelpText: "HTTP basic auth password (optional)"},
				{Name: "headers", Label: "Headers", Type: fieldTextarea, Advanced: true,
					HelpText: "Extra request headers, one \"Key: Value\" per line. Values (except standard ones such as Accept) are hidden after saving; keep \"" + MaskedValue + "\" to reuse the stored value"},
			},
		},
		{
			Kind: KindEmail,
			Name: "Email",
			Fields: []FieldSchema{
				{Name: "host", Label: "Server", Type: fieldText, Required: true,
					HelpText: "SMTP server host name or IP, e.g. smtp.gmail.com"},
				{Name: "port", Label: "Port", Type: fieldNumber, Required: true, Default: 587,
					HelpText: "Usually 587 (STARTTLS), 465 (TLS) or 25 (none)"},
				{Name: "encryption", Label: "Encryption", Type: fieldSelect, Default: EncryptionStartTLS,
					Options:  []string{EncryptionNone, EncryptionStartTLS, EncryptionTLS},
					HelpText: "starttls upgrades a plain connection (port 587); tls is implicit TLS (port 465)"},
				{Name: "username", Label: "Username", Type: fieldText,
					HelpText: "SMTP username (optional)"},
				{Name: "password", Label: "Password", Type: fieldPassword, Secret: true,
					HelpText: "SMTP password or app password (optional)"},
				{Name: "from", Label: "From Address", Type: fieldText, Required: true,
					HelpText: "e.g. Dupearr <dupearr@example.com>"},
				{Name: "to", Label: "To Addresses", Type: fieldText, Required: true,
					HelpText: "Comma-separated recipient addresses"},
				{Name: "cc", Label: "CC Addresses", Type: fieldText, Advanced: true,
					HelpText: "Comma-separated addresses (optional)"},
				{Name: "bcc", Label: "BCC Addresses", Type: fieldText, Advanced: true,
					HelpText: "Comma-separated addresses (optional)"},
			},
		},
	}
}

// providerSchema returns the schema of kind.
func providerSchema(kind string) (ProviderSchema, bool) {
	for _, p := range Schema() {
		if p.Kind == kind {
			return p, true
		}
	}
	return ProviderSchema{}, false
}

// knownKinds returns every provider kind in schema order.
func knownKinds() []string {
	schemas := Schema()
	kinds := make([]string, 0, len(schemas))
	for _, p := range schemas {
		kinds = append(kinds, p.Kind)
	}
	return kinds
}
