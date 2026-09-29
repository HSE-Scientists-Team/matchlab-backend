package smtp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Host            string
	Port            int
	Username        string
	Password        string
	From            string
	VerificationURL string
	RequireStartTLS bool
}

type Sender struct{ config Config }

func NewSender(config Config) *Sender { return &Sender{config: config} }

func (s *Sender) SendVerification(ctx context.Context, recipient, token string, expiresAt time.Time) error {
	from, err := mail.ParseAddress(s.config.From)
	if err != nil {
		return fmt.Errorf("parse sender address: %w", err)
	}
	to, err := mail.ParseAddress(recipient)
	if err != nil {
		return fmt.Errorf("parse recipient address: %w", err)
	}
	verificationURL, err := url.Parse(s.config.VerificationURL)
	if err != nil || (verificationURL.Scheme != "https" && verificationURL.Scheme != "http") || verificationURL.Host == "" {
		return fmt.Errorf("invalid email verification URL")
	}
	query := verificationURL.Query()
	query.Set("token", token)
	verificationURL.RawQuery = query.Encode()

	message := strings.Join([]string{
		"From: " + from.String(),
		"To: " + to.String(),
		"Subject: Confirm your MatchLab email",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"",
		"Confirm this email address using the link below:",
		verificationURL.String(),
		"",
		"The link expires at " + expiresAt.UTC().Format(time.RFC3339) + ".",
		"If you did not request this, you can ignore this message.",
		"",
	}, "\r\n")

	address := net.JoinHostPort(s.config.Host, strconv.Itoa(s.config.Port))
	conn, err := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to SMTP server: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	}

	client, err := smtp.NewClient(conn, s.config.Host)
	if err != nil {
		return fmt.Errorf("start SMTP session: %w", err)
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	} else if s.config.RequireStartTLS {
		return fmt.Errorf("SMTP server does not support required STARTTLS")
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return fmt.Errorf("authenticate with SMTP server: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message: %w", err)
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("send SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}
