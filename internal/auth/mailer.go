package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

type Mailer interface {
	SendMagicLink(email, link string) error
}

type NoopMailer struct {
	Logger *slog.Logger
}

func (m *NoopMailer) SendMagicLink(email, link string) error {
	m.Logger.Info("magic link (noop)", "email", email, "link", link)
	return nil
}

type SMTPMailer struct {
	Host string
	Port int
	User string
	Pass string
	From string
}

func (m *SMTPMailer) SendMagicLink(email, link string) error {
	auth := smtp.PlainAuth("", m.User, m.Pass, m.Host)
	addr := fmt.Sprintf("%s:%d", m.Host, m.Port)

	msg := strings.Join([]string{
		"From: " + m.From,
		"To: " + email,
		"Subject: HireBridge — Your Login Link",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"Click the link below to log in to HireBridge:",
		"",
		link,
		"",
		"This link expires in 15 minutes. If you did not request this, ignore this email.",
	}, "\r\n")

	return smtp.SendMail(addr, auth, m.From, []string{email}, []byte(msg))
}

type ResendMailer struct {
	APIKey string
	From   string
	Client *http.Client
}

func (m *ResendMailer) SendMagicLink(email, link string) error {
	body := map[string]any{
		"from":    m.From,
		"to":      email,
		"subject": "HireBridge — Your Login Link",
		"text": "Click the link below to log in to HireBridge:\n\n" + link +
			"\n\nThis link expires in 15 minutes. If you did not request this, ignore this email.",
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("resend: marshal: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("resend: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.Client.Do(req)
	if err != nil {
		return fmt.Errorf("resend: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("resend: http %d: %s", resp.StatusCode, string(b))
	}

	return nil
}

type PurelymailMailer struct {
	APIToken string
	From     string
	Client   *http.Client
}

func (m *PurelymailMailer) SendMagicLink(email, link string) error {
	body := map[string]any{
		"from":    m.From,
		"to":      email,
		"subject": "HireBridge — Your Login Link",
		"text": "Click the link below to log in to HireBridge:\n\n" + link +
			"\n\nThis link expires in 15 minutes. If you did not request this, ignore this email.",
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("purelymail: marshal: %w", err)
	}

	req, err := http.NewRequest("POST", "https://purelymail.com/api/v0/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("purelymail: request: %w", err)
	}
	req.Header.Set("Purelymail-Token", m.APIToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.Client.Do(req)
	if err != nil {
		return fmt.Errorf("purelymail: post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("purelymail: http %d: %s", resp.StatusCode, string(b))
	}

	return nil
}

func NewMailer(cfg MailerConfig, logger *slog.Logger) Mailer {
	switch cfg.Provider {
	case "resend":
		return &ResendMailer{APIKey: cfg.ResendAPIKey, From: cfg.SMTPFrom, Client: &http.Client{Timeout: 10 * time.Second}}
	case "purelymail":
		return &PurelymailMailer{APIToken: cfg.PurelymailAPIToken, From: cfg.SMTPFrom, Client: &http.Client{Timeout: 10 * time.Second}}
	case "smtp":
		return &SMTPMailer{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Pass: cfg.SMTPPass, From: cfg.SMTPFrom}
	}

	if cfg.ResendAPIKey != "" {
		return &ResendMailer{APIKey: cfg.ResendAPIKey, From: cfg.SMTPFrom, Client: &http.Client{Timeout: 10 * time.Second}}
	}
	if cfg.PurelymailAPIToken != "" {
		return &PurelymailMailer{APIToken: cfg.PurelymailAPIToken, From: cfg.SMTPFrom, Client: &http.Client{Timeout: 10 * time.Second}}
	}
	if cfg.SMTPHost != "" {
		return &SMTPMailer{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Pass: cfg.SMTPPass, From: cfg.SMTPFrom}
	}
	return &NoopMailer{Logger: logger}
}

type MailerConfig struct {
	Provider            string
	ResendAPIKey        string
	PurelymailAPIToken  string
	SMTPHost            string
	SMTPPort            int
	SMTPUser            string
	SMTPPass            string
	SMTPFrom            string
}
