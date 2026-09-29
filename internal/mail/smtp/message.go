package smtp

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	texttemplate "text/template"
	"time"
)

//go:embed templates/verification.html templates/verification.txt
var templateFiles embed.FS

var verificationHTML = htmltemplate.Must(htmltemplate.ParseFS(templateFiles, "templates/verification.html"))
var verificationText = texttemplate.Must(texttemplate.ParseFS(templateFiles, "templates/verification.txt"))

type verificationData struct {
	VerificationURL string
	ExpiresAt       string
}

func buildVerificationMessage(from, to *mail.Address, verificationURL string, expiresAt time.Time) ([]byte, error) {
	data := verificationData{
		VerificationURL: verificationURL,
		ExpiresAt:       expiresAt.UTC().Format("02.01.2006 15:04 UTC"),
	}
	var textBody, htmlBody bytes.Buffer
	if err := verificationText.Execute(&textBody, data); err != nil {
		return nil, fmt.Errorf("формирование текстового письма: %w", err)
	}
	if err := verificationHTML.Execute(&htmlBody, data); err != nil {
		return nil, fmt.Errorf("формирование HTML-письма: %w", err)
	}

	var body bytes.Buffer
	multi := multipart.NewWriter(&body)
	for _, part := range []struct {
		contentType string
		body        []byte
	}{
		{"text/plain; charset=UTF-8", textBody.Bytes()},
		{"text/html; charset=UTF-8", htmlBody.Bytes()},
	} {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Type", part.contentType)
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := multi.CreatePart(header)
		if err != nil {
			return nil, fmt.Errorf("создание части письма: %w", err)
		}
		encoded := quotedprintable.NewWriter(writer)
		if _, err := encoded.Write(part.body); err != nil {
			return nil, fmt.Errorf("кодирование части письма: %w", err)
		}
		if err := encoded.Close(); err != nil {
			return nil, fmt.Errorf("завершение кодирования письма: %w", err)
		}
	}
	if err := multi.Close(); err != nil {
		return nil, fmt.Errorf("завершение письма: %w", err)
	}

	var message bytes.Buffer
	fmt.Fprintf(&message, "From: %s\r\n", from.String())
	fmt.Fprintf(&message, "To: %s\r\n", to.String())
	fmt.Fprintf(&message, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", "Подтвердите почту в MatchLab"))
	message.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&message, "Content-Type: multipart/alternative; boundary=%q\r\n", multi.Boundary())
	message.WriteString("\r\n")
	message.Write(body.Bytes())
	return message.Bytes(), nil
}
