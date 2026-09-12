package logging

import (
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestLogFormatterPrintsVersionField(t *testing.T) {
	entry := log.NewEntry(log.New())
	entry.Time = time.Date(2026, 6, 9, 11, 10, 2, 0, time.Local)
	entry.Level = log.InfoLevel
	entry.Message = "fetched latest antigravity version"
	entry.Data["version"] = "2.1.0"

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format() error = %v", errFormat)
	}

	line := string(formatted)
	if !strings.Contains(line, "version=2.1.0") {
		t.Fatalf("formatted line %q missing version field", line)
	}
}

func TestLogFormatterPrintsMediaForwardingFields(t *testing.T) {
	entry := log.NewEntry(log.New())
	entry.Time = time.Date(2026, 7, 25, 7, 36, 4, 0, time.Local)
	entry.Level = log.InfoLevel
	entry.Message = "codex live remote media forwarding started"
	entry.Data["credential"] = "Voice credential\nsecondary"
	entry.Data["connection"] = "via socks5 proxy"
	entry.Data["proxy_scheme"] = "socks5"
	entry.Data["remote_transport"] = "tcp"
	entry.Data["media_session_id"] = "media-session-id"
	entry.Data["call_id"] = "call-id"
	entry.Data["peer"] = "remote"
	entry.Data["state"] = "connected"

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format() error = %v", errFormat)
	}

	line := string(formatted)
	for _, want := range []string{
		`credential="Voice credential\nsecondary"`,
		`connection="via socks5 proxy"`,
		`proxy_scheme="socks5"`,
		`remote_transport="tcp"`,
		`media_session_id="media-session-id"`,
		`call_id="call-id"`,
		`peer="remote"`,
		`state="connected"`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted line %q missing %s", line, want)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("formatted line contains an unescaped newline: %q", line)
	}
}

func TestLogFormatterPrintsPluginFields(t *testing.T) {
	entry := log.NewEntry(log.New())
	entry.Time = time.Date(2026, 6, 25, 20, 10, 0, 0, time.Local)
	entry.Level = log.InfoLevel
	entry.Message = "pluginhost: plugin loaded"
	entry.Data["plugin_id"] = "sample-provider"
	entry.Data["plugin_name"] = "Sample Provider"
	entry.Data["version"] = "0.2.0"
	entry.Data["active_version"] = "0.1.0"
	entry.Data["retired_version"] = "0.2.0"
	entry.Data["path"] = "plugins/windows/amd64/sample-provider-v0.2.0.dll"
	entry.Data["active_path"] = "plugins/windows/amd64/sample-provider-v0.1.0.dll"
	entry.Data["retired_path"] = "plugins/windows/amd64/sample-provider-v0.2.0.dll"

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format() error = %v", errFormat)
	}

	line := string(formatted)
	for _, want := range []string{
		"plugin_id=sample-provider",
		"plugin_name=Sample Provider",
		"version=0.2.0",
		"active_version=0.1.0",
		"retired_version=0.2.0",
		"path=plugins/windows/amd64/sample-provider-v0.2.0.dll",
		"active_path=plugins/windows/amd64/sample-provider-v0.1.0.dll",
		"retired_path=plugins/windows/amd64/sample-provider-v0.2.0.dll",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted line %q missing %s", line, want)
		}
	}
}

func TestLogFormatterOmitsGenericPathField(t *testing.T) {
	entry := log.NewEntry(log.New())
	entry.Time = time.Date(2026, 6, 25, 20, 20, 0, 0, time.Local)
	entry.Level = log.WarnLevel
	entry.Message = "failed to roll back token"
	entry.Data["path"] = "auths/private-token.json"
	entry.Data["active_path"] = "plugins/windows/amd64/sample-provider-v0.1.0.dll"
	entry.Data["retired_path"] = "plugins/windows/amd64/sample-provider-v0.2.0.dll"

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format() error = %v", errFormat)
	}

	line := string(formatted)
	for _, forbidden := range []string{"path=", "active_path=", "retired_path="} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("formatted line %q contains generic %s field", line, forbidden)
		}
	}
}

func TestLogFormatterPrintsCooldownDiagnosticFields(t *testing.T) {
	entry := log.NewEntry(log.New())
	entry.Time = time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local)
	entry.Level = log.WarnLevel
	entry.Message = "antigravity executor: upstream 429 requests short cooldown, record deferred until final failure"
	entry.Data["request_id"] = "req-123"
	entry.Data["model"] = "gemini-3.6-flash-high"
	entry.Data["credential"] = "abc123"
	entry.Data["endpoint"] = "cloudcode-pa.googleapis.com"
	entry.Data["reason"] = "RATE_LIMIT_EXCEEDED"
	entry.Data["retry_after_s"] = 120.0
	entry.Data["remaining_s"] = 119.9
	entry.Data["observed_at"] = "2026-09-12T05:00:00Z"

	formatted, errFormat := (&LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format() error = %v", errFormat)
	}

	line := string(formatted)
	for _, want := range []string{
		"[req-123]",
		"model=gemini-3.6-flash-high",
		"credential=\"abc123\"",
		"endpoint=cloudcode-pa.googleapis.com",
		"reason=\"RATE_LIMIT_EXCEEDED\"",
		"retry_after_s=120",
		"remaining_s=119.9",
		"observed_at=2026-09-12T05:00:00Z",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted line %q missing %q", line, want)
		}
	}
}
