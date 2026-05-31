// Go port of Python's POST /notifications/channels/{id}/test
// (api/notifications.py:103). Builds a synthetic warning-severity
// "fire" Alert and dispatches it through a single channel so an
// operator can verify Slack/SMTP/webhook wiring before relying on
// it for real incidents.
//
// Diverges from Python in one way: Python's `dispatch()` re-fetches
// every enabled channel from the DB and iterates all of them just
// to find this one's outcome. The Go port skips the full loop and
// sends only to the channel under test — same return shape
// ({delivered, error}), faster path, no chance of "test channel X
// silently fired channel Y" if a stale config row exists.
//
// All three senders (webhook, slack, email) live in this file so
// callers don't need to import a separate notif_svc package. The
// pure helper `channelMatches` is unit-tested.
package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbq "github.com/usg-dcim/packages/otter-go/db/generated"
	"github.com/usg-dcim/packages/otter-go/internal/httpx"
)

// severity ordering mirrors Python's _SEV_ORDER in
// services/notifications.py:34. Tied to the catalog convention
// info < warning < minor < major < critical.
var sevOrder = map[string]int{
	"info":     0,
	"warning":  1,
	"minor":    2,
	"major":    3,
	"critical": 4,
}

func sevValue(s string) int {
	if v, ok := sevOrder[s]; ok {
		return v
	}
	return 0
}

// channelMatches mirrors Python's channel_matches helper. Returns
// true if the channel should receive this (severity, event) pair.
// `event` is one of "fire" | "resolve".
func channelMatches(c dbq.NotificationChannel, severity string, event string) bool {
	if !c.Enabled {
		return false
	}
	if event == "fire" && !c.NotifyOnFire {
		return false
	}
	if event == "resolve" && !c.NotifyOnResolve {
		return false
	}
	return sevValue(severity) >= sevValue(c.MinSeverity)
}

// syntheticAlert mirrors the fake Alert Python builds in the test
// endpoint — warning severity, "firing" state, synthetic dedupe key.
// Site/asset/collector are NULL because this is a probe, not a real
// alert tied to inventory.
type syntheticAlert struct {
	ID          uuid.UUID
	Severity    string
	State       string
	DedupeKey   string
	Summary     string
	Detail      string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	Labels      map[string]any
}

// webhookPayload mirrors format_webhook_payload in Python.
// Stable contract — downstream automations key off these field names.
func webhookPayload(a syntheticAlert, event string) map[string]any {
	return map[string]any{
		"event": "alert." + event,
		"alert": map[string]any{
			"id":             a.ID.String(),
			"severity":       a.Severity,
			"state":          a.State,
			"summary":        a.Summary,
			"detail":         a.Detail,
			"site_id":        nil,
			"asset_id":       nil,
			"first_seen_at":  a.FirstSeenAt.UTC().Format(time.RFC3339Nano),
			"last_seen_at":   a.LastSeenAt.UTC().Format(time.RFC3339Nano),
			"labels":         a.Labels,
		},
	}
}

// slackColor maps severity to the Slack attachment color band.
// Same hex values as Python's _SLACK_COLOR.
var slackColor = map[string]string{
	"critical": "#d92626",
	"major":    "#e08e1b",
	"minor":    "#e6c01a",
	"warning":  "#5b9bd5",
	"info":     "#777777",
}

// slackPayload mirrors format_slack_payload at services/notifications.py:108.
// Title is the severity+event label only (summary lives in the text body);
// fields surface State + Site so the message is scannable at a glance in #ops.
// The synthetic test alert has no site, so Site renders as Python's em-dash
// placeholder.
func slackPayload(a syntheticAlert, event string) map[string]any {
	color := slackColor[a.Severity]
	if color == "" {
		color = slackColor["info"]
	}
	return map[string]any{
		"attachments": []map[string]any{{
			"color": color,
			"title": fmt.Sprintf("[%s] alert.%s", strings.ToUpper(a.Severity), event),
			"text":  a.Summary,
			"fields": []map[string]any{
				{"title": "State", "value": a.State, "short": true},
				{"title": "Site", "value": "—", "short": true},
			},
		}},
	}
}

func emailSubjectBody(a syntheticAlert, event string) (string, string) {
	subject := fmt.Sprintf("[DCIM][%s] alert.%s: %s",
		strings.ToUpper(a.Severity), event, a.Summary)
	body := strings.Join([]string{
		"Event: alert." + event,
		"Severity: " + a.Severity,
		"State: " + a.State,
		"Summary: " + a.Summary,
		"",
		"Detail: " + nonEmpty(a.Detail, "(none)"),
		"Site: —",
		"Asset: —",
		"First seen: " + a.FirstSeenAt.UTC().Format(time.RFC3339Nano),
		"Last seen: " + a.LastSeenAt.UTC().Format(time.RFC3339Nano),
	}, "\n")
	return subject, body
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// httpPoster is the interface webhook + slack senders use, so tests
// can substitute a fake without spinning up a real HTTP server.
type httpPoster interface {
	Do(*http.Request) (*http.Response, error)
}

// smtpSender is the interface the email sender uses. The default
// production binding wraps net/smtp; tests substitute a fake that
// records (host, addrs, from, msg) and returns whatever error the
// scenario calls for. Lifted to package scope so it can be swapped
// by tests; never mutate in production.
type smtpSender func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

var defaultSMTP smtpSender = smtp.SendMail

// dispatchOne sends `alert` through a single channel and returns
// (delivered, error). Per-kind logic mirrors Python's `_send_*`
// helpers; failures are returned as the second value (not panicked).
func dispatchOne(
	ctx context.Context, client httpPoster, sendMail smtpSender,
	c dbq.NotificationChannel, alert syntheticAlert, event string,
) (delivered bool, err error) {
	cfg := map[string]any{}
	if len(c.ConfigJson) > 0 {
		if uerr := json.Unmarshal(c.ConfigJson, &cfg); uerr != nil {
			return false, fmt.Errorf("config_json invalid: %w", uerr)
		}
	}
	switch c.Kind {
	case "webhook":
		return sendWebhook(ctx, client, cfg, webhookPayload(alert, event))
	case "slack":
		return sendSlack(ctx, client, cfg, slackPayload(alert, event))
	case "email":
		return sendEmail(sendMail, cfg, alert, event)
	default:
		return false, fmt.Errorf("unsupported channel kind %q", c.Kind)
	}
}

func sendWebhook(ctx context.Context, client httpPoster, cfg map[string]any, payload map[string]any) (bool, error) {
	url, _ := cfg["url"].(string)
	if url == "" {
		return false, errors.New("webhook channel missing config_json.url")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	// Apply operator-provided headers first, then force Content-Type
	// so a misconfigured channel can't accidentally re-label the JSON
	// payload as text/plain (downstream automations key on the
	// content type to decide whether to JSON-parse).
	if headers, ok := cfg["headers"].(map[string]any); ok {
		for k, v := range headers {
			if vs, ok := v.(string); ok {
				req.Header.Set(k, vs)
			}
		}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return true, nil
}

func sendSlack(ctx context.Context, client httpPoster, cfg map[string]any, payload map[string]any) (bool, error) {
	url, _ := cfg["webhook_url"].(string)
	if url == "" {
		return false, errors.New("slack channel missing config_json.webhook_url")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return false, fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return true, nil
}

func sendEmail(send smtpSender, cfg map[string]any, alert syntheticAlert, event string) (bool, error) {
	host := os.Getenv("DCIM_SMTP_HOST")
	if host == "" {
		// Soft no-op so dev/CI doesn't error on missing SMTP —
		// matches Python's behavior at services/notifications.py:184.
		return false, nil
	}
	rawRecipients, _ := cfg["recipients"].([]any)
	if len(rawRecipients) == 0 {
		return false, errors.New("email channel missing config_json.recipients")
	}
	recipients := make([]string, 0, len(rawRecipients))
	for _, r := range rawRecipients {
		if rs, ok := r.(string); ok && rs != "" {
			recipients = append(recipients, rs)
		}
	}
	if len(recipients) == 0 {
		return false, errors.New("email channel recipients list is empty after filtering non-strings")
	}
	// 587 (submission) is the only port that mandates STARTTLS per
	// RFC 6409; Python's settings.smtp_port defaulted to 587 too, so
	// operators carrying over configs see no change. Port 25 (smtp.SendMail's
	// default) frequently skips STARTTLS negotiation, which would leak the
	// password set via DCIM_SMTP_USERNAME / DCIM_SMTP_PASSWORD below.
	port := os.Getenv("DCIM_SMTP_PORT")
	if port == "" {
		port = "587"
	}
	sender := os.Getenv("DCIM_SMTP_SENDER")
	if sender == "" {
		sender = "dcim@localhost"
	}
	subject, body := emailSubjectBody(alert, event)
	var msg bytes.Buffer
	hdr := textproto.MIMEHeader{}
	hdr.Set("From", sender)
	hdr.Set("To", strings.Join(recipients, ", "))
	hdr.Set("Subject", subject)
	hdr.Set("MIME-Version", "1.0")
	hdr.Set("Content-Type", "text/plain; charset=utf-8")
	for k, vs := range hdr {
		for _, v := range vs {
			msg.WriteString(k)
			msg.WriteString(": ")
			msg.WriteString(v)
			msg.WriteString("\r\n")
		}
	}
	msg.WriteString("\r\n")
	msg.WriteString(body)
	var auth smtp.Auth
	if user := os.Getenv("DCIM_SMTP_USERNAME"); user != "" {
		auth = smtp.PlainAuth("", user, os.Getenv("DCIM_SMTP_PASSWORD"), host)
	}
	if err := send(host+":"+port, auth, sender, recipients, msg.Bytes()); err != nil {
		return false, err
	}
	return true, nil
}

// httpPosterFunc adapts a function to the httpPoster interface so
// the default production client can be swapped via http.Client.Do.
// (http.Client already has a Do method; this is just so the test
// substitution stays one line.)

type testChannelResponse struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

// testChannelClient is set by Mount-time wiring in tests so the
// handler can substitute a fake httpPoster + smtpSender. Production
// uses defaultHTTPClient and defaultSMTP.
var (
	defaultHTTPClient httpPoster = &http.Client{Timeout: 10 * time.Second}
)

func (h *Handler) testChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := idFromURL(w, r)
	if !ok {
		return
	}
	channel, err := h.Q.GetNotificationChannel(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, http.StatusNotFound, "channel not found")
			return
		}
		status, msg := httpx.Mapped(err)
		httpx.Error(w, status, msg)
		return
	}
	now := time.Now().UTC()
	alert := syntheticAlert{
		ID:          uuid.New(),
		Severity:    "warning",
		State:       "firing",
		DedupeKey:   "test|" + id.String(),
		Summary:     fmt.Sprintf("Test notification for channel %q", channel.Name),
		Detail:      fmt.Sprintf("Triggered at %s", now.Format(time.RFC3339Nano)),
		FirstSeenAt: now,
		LastSeenAt:  now,
		Labels:      map[string]any{"test": true},
	}
	// Filter checks mirror Python's early-exit branches in
	// api/notifications.py:131-134. These return 200 with a
	// structured {delivered:false, error:reason} body — operators
	// see the reason in the UI and adjust the channel config.
	// Python's test endpoint did NOT emit an audit event on any
	// path (the dispatcher itself doesn't audit either — only real
	// alert mutations do). Matching that here so an operator's
	// click-storm on a misconfigured channel during debugging
	// doesn't flood the audit log.
	if !channel.Enabled {
		httpx.JSON(w, http.StatusOK, testChannelResponse{Delivered: false, Error: "channel is disabled"})
		return
	}
	if !channelMatches(channel, alert.Severity, "fire") {
		httpx.JSON(w, http.StatusOK, testChannelResponse{Delivered: false, Error: "channel filters skip warning-severity fires"})
		return
	}
	delivered, derr := dispatchOne(r.Context(), defaultHTTPClient, defaultSMTP, channel, alert, "fire")
	resp := testChannelResponse{Delivered: delivered}
	if derr != nil {
		resp.Error = derr.Error()
	}
	httpx.JSON(w, http.StatusOK, resp)
}
