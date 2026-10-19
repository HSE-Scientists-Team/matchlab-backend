package chat

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type Repository struct{ DB *sql.DB }
type scanner interface{ Scan(...any) error }

const conversationSelect = `SELECT c.id,c.user_low_id,c.user_high_id,c.last_message_seq,
 c.user_low_last_read_seq,c.user_high_last_read_seq,c.created_at,c.last_message_at,
 (SELECT count(*) FROM chat.message u WHERE u.conversation_id=c.id AND u.sender_user_id<>$1
 AND u.seq>CASE WHEN c.user_low_id=$1 THEN c.user_low_last_read_seq ELSE c.user_high_last_read_seq END),
 m.id,m.seq,m.sender_user_id,m.client_message_id,m.body,m.created_at
 FROM chat.conversation c LEFT JOIN chat.message m ON m.conversation_id=c.id AND m.seq=c.last_message_seq `

func scanConversation(row scanner) (Conversation, error) {
	var c Conversation
	var id, sender, client, body sql.NullString
	var seq sql.NullInt64
	var created sql.NullTime
	err := row.Scan(&c.ID, &c.LowID, &c.HighID, &c.LastSeq, &c.LowRead, &c.HighRead, &c.CreatedAt, &c.LastAt, &c.Unread, &id, &seq, &sender, &client, &body, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrMissing
	}
	if err == nil && id.Valid {
		c.LastMessage = &Message{ID: id.String, ConversationID: c.ID, SenderID: sender.String, ClientID: client.String, Body: body.String, Seq: seq.Int64, CreatedAt: created.Time}
	}
	return c, err
}
func (r *Repository) Conversation(ctx context.Context, id, actor string) (Conversation, error) {
	return scanConversation(r.DB.QueryRowContext(ctx, conversationSelect+`WHERE c.id=$2 AND (c.user_low_id=$1 OR c.user_high_id=$1)`, actor, id))
}
func (r *Repository) Open(ctx context.Context, actor, peer string) (Conversation, bool, error) {
	low, high := actor, peer
	if strings.Compare(low, high) > 0 {
		low, high = high, low
	}
	var id string
	err := r.DB.QueryRowContext(ctx, `INSERT INTO chat.conversation(user_low_id,user_high_id) VALUES($1,$2)
    ON CONFLICT(user_low_id,user_high_id) DO NOTHING RETURNING id`, low, high).Scan(&id)
	created := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		err = r.DB.QueryRowContext(ctx, `SELECT id FROM chat.conversation WHERE user_low_id=$1 AND user_high_id=$2`, low, high).Scan(&id)
	}
	if err != nil {
		return Conversation{}, false, err
	}
	c, err := r.Conversation(ctx, id, actor)
	return c, created, err
}
func (r *Repository) List(ctx context.Context, actor string, limit int32, cursor *Cursor) ([]Conversation, error) {
	query := conversationSelect + `WHERE (c.user_low_id=$1 OR c.user_high_id=$1)`
	args := []any{actor, limit}
	if cursor != nil {
		query += ` AND (coalesce(c.last_message_at,c.created_at),c.id)<($3,$4::uuid)`
		args = append(args, cursor.At, cursor.ID)
	}
	query += ` ORDER BY coalesce(c.last_message_at,c.created_at) DESC,c.id DESC LIMIT $2`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}

const messageColumns = `id,conversation_id,seq,sender_user_id,client_message_id,body,created_at`

func scanMessage(row scanner) (Message, error) {
	var m Message
	err := row.Scan(&m.ID, &m.ConversationID, &m.Seq, &m.SenderID, &m.ClientID, &m.Body, &m.CreatedAt)
	return m, err
}
func (r *Repository) Messages(ctx context.Context, id string, limit int32, before, after *int64) ([]Message, error) {
	query := `SELECT ` + messageColumns + ` FROM chat.message WHERE conversation_id=$1`
	args := []any{id, limit}
	if before != nil {
		query += ` AND seq<$3`
		args = append(args, *before)
	}
	if after != nil {
		query += ` AND seq>$3`
		args = append(args, *after)
	}
	if after != nil {
		query += ` ORDER BY seq ASC`
	} else {
		query += ` ORDER BY seq DESC`
	}
	query += ` LIMIT $2`
	rows, err := r.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, m)
	}
	return items, rows.Err()
}

// Send serializes all writers and read markers on the conversation row.
// The callback is executed only for a new message, never for an identical retry.
func (r *Repository) Send(ctx context.Context, id, actor, client, body string, allowed func(context.Context) error) (Message, bool, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback()
	var low, high string
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT user_low_id,user_high_id,last_message_seq FROM chat.conversation WHERE id=$1 FOR UPDATE`, id).Scan(&low, &high, &seq)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, ErrMissing
	}
	if err != nil {
		return Message{}, false, err
	}
	if actor != low && actor != high {
		return Message{}, false, ErrMissing
	}
	existing, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM chat.message WHERE conversation_id=$1 AND sender_user_id=$2 AND client_message_id=$3`, id, actor, client))
	if err == nil {
		if existing.Body != body {
			return Message{}, false, ErrConflict
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, err
	}
	if err = allowed(ctx); err != nil {
		return Message{}, false, err
	}
	// clock_timestamp is taken after the row lock; GREATEST keeps activity
	// monotonic even if the database clock moves backwards.
	var m Message
	m, err = scanMessage(tx.QueryRowContext(ctx, `INSERT INTO chat.message(conversation_id,seq,sender_user_id,client_message_id,body,created_at)
    SELECT id,last_message_seq+1,$2,$3,$4,greatest(clock_timestamp(),coalesce(last_message_at,created_at)) FROM chat.conversation WHERE id=$1
    RETURNING `+messageColumns, id, actor, client, body))
	if err != nil {
		return Message{}, false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat.conversation SET last_message_seq=$2,last_message_at=$3 WHERE id=$1`, id, m.Seq, m.CreatedAt)
	if err != nil {
		return Message{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return Message{}, false, err
	}
	return m, true, nil
}
func (r *Repository) MarkRead(ctx context.Context, id, actor string, seq int64) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var low, high string
	var last int64
	err = tx.QueryRowContext(ctx, `SELECT user_low_id,user_high_id,last_message_seq FROM chat.conversation WHERE id=$1 FOR UPDATE`, id).Scan(&low, &high, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrMissing
	}
	if err != nil {
		return err
	}
	if actor != low && actor != high {
		return ErrMissing
	}
	if seq < 0 || seq > last {
		return ErrInvalid
	}
	column := "user_low_last_read_seq"
	if actor == high {
		column = "user_high_last_read_seq"
	}
	_, err = tx.ExecContext(ctx, `UPDATE chat.conversation SET `+column+`=greatest(`+column+`,$2) WHERE id=$1`, id, seq)
	if err != nil {
		return err
	}
	return tx.Commit()
}
