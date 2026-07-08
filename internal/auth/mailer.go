package auth

import (
	"fmt"
	"log/slog"
	"net/smtp"
	"strings"
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

func NewMailer(cfg MailerConfig, logger *slog.Logger) Mailer {
	if cfg.ResendAPIKey != "" {
		return &NoopMailer{Logger: logger}
	}
	if cfg.SMTPHost != "" {
		return &SMTPMailer{
			Host: cfg.SMTPHost,
			Port: cfg.SMTPPort,
			User: cfg.SMTPUser,
			Pass: cfg.SMTPPass,
			From: cfg.SMTPFrom,
		}
	}
	return &NoopMailer{Logger: logger}
}

type MailerConfig struct {
	ResendAPIKey string
	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPass     string
	SMTPFrom     string
}
