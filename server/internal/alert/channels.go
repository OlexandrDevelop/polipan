// SPDX-License-Identifier: Apache-2.0

package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

func (e *Engine) client(ctx context.Context, orgID string) *http.Client {
	if e.Client != nil {
		return e.Client(ctx, orgID)
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (e *Engine) dialer(ctx context.Context, orgID string) *net.Dialer {
	if e.Dialer != nil {
		return e.Dialer(ctx, orgID)
	}
	return &net.Dialer{Timeout: 15 * time.Second}
}

// deliver sends one message to one channel.
func (e *Engine) deliver(ctx context.Context, ch *Channel, m *Message) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	secret := ""
	if ch.SecretID != nil {
		v, err := e.Store.SecretValue(ctx, *ch.SecretID)
		if err != nil {
			return fmt.Errorf("the secret of the channel cannot be read: %w", err)
		}
		secret = v
	}
	switch ch.Kind {
	case "telegram":
		return e.sendTelegram(ctx, ch, secret, m)
	case "slack":
		return e.sendSlack(ctx, ch, secret, m)
	case "email":
		return e.sendEmail(ctx, ch, secret, m)
	case "webhook":
		return e.sendWebhook(ctx, ch, secret, m)
	}
	return fmt.Errorf("unknown channel kind %q", ch.Kind)
}

// postJSON sends a JSON body. A status outside 2xx is an error that has the start of the answer.
func (e *Engine) postJSON(ctx context.Context, orgID, url string, body any, headers map[string]string) (*http.Response, []byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}
	return e.postRaw(ctx, orgID, url, raw, headers)
}

func (e *Engine) postRaw(ctx context.Context, orgID, url string, raw []byte, headers map[string]string) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Polipan-Alerts")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.client(ctx, orgID).Do(req)
	if err != nil {
		return nil, nil, redactURL(err, url)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return resp, b, nil
}

// redactURL keeps a secret URL (a Slack webhook, a bot token) out of an error text.
func redactURL(err error, url string) error {
	msg := strings.ReplaceAll(err.Error(), url, "<url>")
	var ue interface{ Unwrap() error }
	if errors.As(err, &ue) && ue.Unwrap() != nil {
		msg = strings.ReplaceAll(ue.Unwrap().Error(), url, "<url>")
	}
	return errors.New("request failed: " + msg)
}

func statusErr(resp *http.Response, body []byte) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("the channel answered %d: %s", resp.StatusCode, truncate(strings.TrimSpace(string(body)), 200))
}

// ---- Telegram ----

// telegramBatcher holds messages when more than 5 go to one chat in 10 seconds: they leave as one message with a
// list (F09, Telegram rate limit).
type telegramBatcher struct {
	mu      sync.Mutex
	sent    map[string][]time.Time
	pending map[string][]string
}

const (
	telegramBurst  = 5
	telegramWindow = 10 * time.Second
)

func (e *Engine) sendTelegram(ctx context.Context, ch *Channel, token string, m *Message) error {
	if e.batcher == nil {
		e.batcher = &telegramBatcher{sent: map[string][]time.Time{}, pending: map[string][]string{}}
	}
	b := e.batcher
	now := e.now()
	b.mu.Lock()
	recent := b.sent[ch.ID][:0]
	for _, t := range b.sent[ch.ID] {
		if now.Sub(t) < telegramWindow {
			recent = append(recent, t)
		}
	}
	b.sent[ch.ID] = recent
	if len(recent) >= telegramBurst && !m.Test {
		first := len(b.pending[ch.ID]) == 0
		b.pending[ch.ID] = append(b.pending[ch.ID], m.text())
		b.mu.Unlock()
		if first {
			go e.flushTelegram(ch, token)
		}
		return nil // the message is in the list that goes out in a moment
	}
	b.sent[ch.ID] = append(b.sent[ch.ID], now)
	b.mu.Unlock()
	return e.telegramSend(ctx, ch, token, m.text())
}

func (e *Engine) flushTelegram(ch *Channel, token string) {
	time.Sleep(telegramWindow)
	b := e.batcher
	b.mu.Lock()
	list := b.pending[ch.ID]
	delete(b.pending, ch.ID)
	b.sent[ch.ID] = append(b.sent[ch.ID], e.now())
	b.mu.Unlock()
	if len(list) == 0 {
		return
	}
	text := fmt.Sprintf("%d alert messages:\n\n%s", len(list), strings.Join(list, "\n"))
	if len(text) > 3900 {
		text = text[:3900] + "\n…"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.telegramSend(ctx, ch, token, text); err != nil {
		e.log().Warn("alert: telegram batch", "channel", ch.ID, "err", err)
	}
}

func (e *Engine) telegramSend(ctx context.Context, ch *Channel, token, text string) error {
	if len(text) > 4000 {
		text = text[:4000] + "\n…"
	}
	api := strings.TrimRight(e.TelegramAPI, "/")
	if api == "" {
		api = "https://api.telegram.org"
	}
	body := map[string]any{"chat_id": str(ch.Config, "chat_id"), "text": text, "disable_web_page_preview": true}
	url := api + "/bot" + token + "/sendMessage"
	resp, b, err := e.postJSON(ctx, ch.OrgID, url, body, nil)
	if err != nil {
		return err
	}
	if err := statusErr(resp, b); err != nil {
		return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	return nil
}

// ---- Slack ----

func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func (e *Engine) sendSlack(ctx context.Context, ch *Channel, hook string, m *Message) error {
	var sb strings.Builder
	sb.WriteString("*" + slackEscape(m.subject()) + "*\n")
	if w := m.whereLine(); w != "" {
		sb.WriteString("Where: " + slackEscape(w) + "\n")
	}
	sb.WriteString("Since: " + m.Since.UTC().Format("2006-01-02 15:04:05 UTC") + "\n")
	if m.Reason != "" {
		sb.WriteString("Reason: `" + slackEscape(m.Reason) + "`\n")
	}
	if len(m.LastLines) > 0 {
		n := len(m.LastLines)
		sb.WriteString("```" + slackEscape(strings.Join(m.LastLines[max(0, n-10):], "\n")) + "```\n")
	}
	if m.Link != "" {
		sb.WriteString("<" + m.Link + "|Open in Polipan>\n")
	}
	body := map[string]any{"text": sb.String()}
	if c := str(ch.Config, "channel"); c != "" {
		body["channel"] = c
	}
	resp, b, err := e.postJSON(ctx, ch.OrgID, hook, body, nil)
	if err != nil {
		return err
	}
	return statusErr(resp, b)
}

// ---- webhook ----

// Sign makes the value of the X-Polipan-Signature header: sha256= and the HMAC of the body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (e *Engine) sendWebhook(ctx context.Context, ch *Channel, secret string, m *Message) error {
	raw, _ := json.Marshal(m.webhookBody(e.now()))
	headers := map[string]string{"X-Polipan-Event": "alert." + m.Kind}
	if secret != "" {
		headers["X-Polipan-Signature"] = Sign(secret, raw)
	}
	resp, b, err := e.postRaw(ctx, ch.OrgID, str(ch.Config, "url"), raw, headers)
	if err != nil {
		return err
	}
	return statusErr(resp, b)
}

// ---- email ----

// plainAuth is SMTP PLAIN. net/smtp refuses it on a connection without TLS; the admin chose tls none on purpose.
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

func headerSafe(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func (e *Engine) sendEmail(ctx context.Context, ch *Channel, password string, m *Message) error {
	host, port := str(ch.Config, "host"), str(ch.Config, "port")
	from := str(ch.Config, "from")
	var rcpt []string
	switch v := ch.Config["recipients"].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				rcpt = append(rcpt, s)
			}
		}
	case []string:
		rcpt = v
	}
	if len(rcpt) == 0 {
		return errors.New("the channel has no recipients")
	}
	mode := str(ch.Config, "tls")
	addr := net.JoinHostPort(host, port)
	conn, err := e.dialer(ctx, ch.OrgID).DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("cannot connect to the mail server: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if mode == "tls" {
		conn = tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("the mail server did not greet: %w", err)
	}
	defer c.Close()
	if mode == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if user := str(ch.Config, "username"); user != "" && password != "" {
		if err := c.Auth(plainAuth{user, password}); err != nil {
			return fmt.Errorf("the mail server refused the login: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	for _, r := range rcpt {
		if err := c.Rcpt(r); err != nil {
			return fmt.Errorf("RCPT TO %s: %w", r, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\n", headerSafe(from))
	fmt.Fprintf(&msg, "To: %s\r\n", headerSafe(strings.Join(rcpt, ", ")))
	fmt.Fprintf(&msg, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", headerSafe(m.subject())))
	fmt.Fprintf(&msg, "Date: %s\r\n", e.now().Format(time.RFC1123Z))
	fmt.Fprintf(&msg, "X-Polipan-Event: alert.%s\r\n", m.Kind)
	msg.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	msg.WriteString(strings.ReplaceAll(strings.ReplaceAll(m.text(), "\r\n", "\n"), "\n", "\r\n"))
	if _, err := w.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("the mail server refused the message: %w", err)
	}
	return c.Quit()
}
