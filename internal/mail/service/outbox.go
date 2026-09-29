package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/repository"
)

var (
	ErrInvalidEmail  = errors.New("invalid recipient email")
	ErrInvalidToken  = errors.New("invalid verification token")
	ErrInvalidExpiry = errors.New("verification token expiry is invalid")
)

type Queue struct {
	outbox repository.Outbox
	aead   cipher.AEAD
	now    func() time.Time
}

func NewQueue(outbox repository.Outbox, encryptionKey []byte) (*Queue, error) {
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("create email token cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create email token authenticator: %w", err)
	}
	return &Queue{outbox: outbox, aead: aead, now: time.Now}, nil
}

func (q *Queue) EnqueueVerification(ctx context.Context, recipient, token string, expiresAt time.Time) error {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	parsed, err := mail.ParseAddress(recipient)
	if err != nil || parsed.Address != recipient || len(recipient) > 320 {
		return ErrInvalidEmail
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return ErrInvalidToken
	}
	now := q.now().UTC()
	if !expiresAt.After(now) || expiresAt.After(now.Add(2*time.Hour)) {
		return ErrInvalidExpiry
	}
	keyHash := messageKey(recipient, token)
	nonce := make([]byte, q.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("create email token nonce: %w", err)
	}
	ciphertext := q.aead.Seal(nil, nonce, []byte(token), keyHash[:])
	if err := q.outbox.Enqueue(ctx, hex.EncodeToString(keyHash[:]), recipient, ciphertext, nonce, expiresAt); err != nil {
		return fmt.Errorf("persist verification email: %w", err)
	}
	return nil
}

func (q *Queue) decrypt(tokenHash, recipient string, ciphertext, nonce []byte) (string, error) {
	keyHash, err := hex.DecodeString(tokenHash)
	if err != nil || len(keyHash) != sha256.Size {
		return "", fmt.Errorf("invalid outbox idempotency key")
	}
	plaintext, err := q.aead.Open(nil, nonce, ciphertext, keyHash)
	if err != nil {
		return "", fmt.Errorf("decrypt verification token: %w", err)
	}
	if expected := messageKey(recipient, string(plaintext)); !bytes.Equal(keyHash, expected[:]) {
		return "", fmt.Errorf("verification token does not match outbox recipient")
	}
	return string(plaintext), nil
}

func messageKey(recipient, token string) [sha256.Size]byte {
	return sha256.Sum256([]byte(recipient + "\x00" + token))
}
