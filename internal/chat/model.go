package chat

import (
	"errors"
	"time"
)

var (
	ErrMissing  = errors.New("conversation not found")
	ErrConflict = errors.New("client_message_id already used with different text")
	ErrInvalid  = errors.New("invalid read position")
)

type Conversation struct {
	ID, LowID, HighID                  string
	LastSeq, LowRead, HighRead, Unread int64
	CreatedAt                          time.Time
	LastAt                             *time.Time
	LastMessage                        *Message
}

func (c Conversation) Peer(actor string) string {
	if actor == c.LowID {
		return c.HighID
	}
	return c.LowID
}
func (c Conversation) Read(actor string) (int64, int64) {
	if actor == c.LowID {
		return c.LowRead, c.HighRead
	}
	return c.HighRead, c.LowRead
}
func (c Conversation) Activity() time.Time {
	if c.LastAt != nil {
		return *c.LastAt
	}
	return c.CreatedAt
}

type Message struct {
	ID, ConversationID, SenderID, ClientID, Body string
	Seq                                          int64
	CreatedAt                                    time.Time
}
type Cursor struct {
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	UserID string    `json:"user_id"`
}
