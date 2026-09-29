package smtp

import (
	"bytes"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestVerificationMessageContainsHTMLAndTextWithSameLink(t *testing.T) {
	from := &mail.Address{Address: "no-reply@example.org"}
	to := &mail.Address{Address: "person@example.org"}
	link := "https://example.org/verify?source=mail&token=abc%26xyz"
	message, err := buildVerificationMessage(from, to, link, time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(message))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (&mime.WordDecoder{}).DecodeHeader(parsed.Header.Get("Subject")); err != nil || got != "Подтвердите почту в MatchLab" {
		t.Fatalf("тема письма: %q, ошибка %v", got, err)
	}
	mediaType, params, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/alternative" {
		t.Fatalf("тип письма: %q, ошибка %v", mediaType, err)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	var parts []string
	var types []string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, string(body))
		types = append(types, part.Header.Get("Content-Type"))
	}
	if len(parts) != 2 || !strings.HasPrefix(types[0], "text/plain;") || !strings.HasPrefix(types[1], "text/html;") {
		t.Fatalf("ожидались текстовая и HTML-части, получены %v", types)
	}
	if !strings.Contains(parts[0], link) || !strings.Contains(parts[0], "01.10.2026 12:30 UTC") {
		t.Fatalf("текстовая часть не содержит ссылку или срок действия: %q", parts[0])
	}
	if !strings.Contains(parts[1], `href="https://example.org/verify?source=mail&amp;token=abc%26xyz"`) ||
		!strings.Contains(parts[1], "Подтвердить почту") ||
		!strings.Contains(parts[1], "01.10.2026 12:30 UTC") {
		t.Fatal("HTML-часть не содержит экранированную ссылку, кнопку или срок действия")
	}
}
