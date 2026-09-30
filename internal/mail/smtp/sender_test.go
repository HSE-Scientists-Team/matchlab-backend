package smtp

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSendVerificationWaitsForSMTPDataAcknowledgement(t *testing.T) {
	for _, code := range []int{250, 550} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		received := make(chan struct{})
		acknowledge := make(chan struct{})
		serverDone := make(chan error, 1)
		go func() { serverDone <- serveSMTPOnce(listener, received, acknowledge, code) }()
		port := listener.Addr().(*net.TCPAddr).Port
		sender := NewSender(Config{Host: "127.0.0.1", Port: port, From: "no-reply@example.org", VerificationURL: "https://example.org/verify"})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result := make(chan error, 1)
		go func() {
			result <- sender.SendVerification(ctx, "user@example.org", "example-token", time.Now().Add(time.Minute))
		}()
		select {
		case <-received:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		select {
		case err := <-result:
			t.Fatalf("ответ до подтверждения DATA: %v", err)
		default:
		}
		close(acknowledge)
		if err := <-result; (err == nil) != (code == 250) {
			t.Fatalf("SMTP %d, результат %v", code, err)
		}
		cancel()
		if err := <-serverDone; err != nil {
			t.Fatal(err)
		}
		listener.Close()
	}
}

func serveSMTPOnce(listener net.Listener, received chan<- struct{}, acknowledge <-chan struct{}, code int) error {
	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	write := func(line string) error {
		if _, err := w.WriteString(line + "\r\n"); err != nil {
			return err
		}
		return w.Flush()
	}
	if err := write("220 localhost ready"); err != nil {
		return err
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return err
		}
		switch {
		case strings.HasPrefix(line, "EHLO "), strings.HasPrefix(line, "HELO "), strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
			if err := write("250 OK"); err != nil {
				return err
			}
		case strings.HasPrefix(line, "DATA"):
			if err := write("354 send data"); err != nil {
				return err
			}
			for {
				line, err := r.ReadString('\n')
				if err != nil {
					return err
				}
				if line == ".\r\n" {
					break
				}
			}
			received <- struct{}{}
			<-acknowledge
			if code == 250 {
				return write("250 accepted")
			}
			return write(fmt.Sprintf("%d rejected", code))
		default:
			return fmt.Errorf("неожиданная команда SMTP: %q", line)
		}
	}
}
