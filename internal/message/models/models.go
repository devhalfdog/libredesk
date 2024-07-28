package models

import (
	"net/textproto"
	"time"

	"github.com/abhinavxd/artemis/internal/attachment"
	cmodels "github.com/abhinavxd/artemis/internal/contact/models"
	mmodels "github.com/abhinavxd/artemis/internal/media/models"
	null "github.com/volatiletech/null/v9"
)

const (
	DispositionInline     = "inline"
	DispositionAttachment = "attachment"
)

// Message represents a message in the database.
type Message struct {
	ID             int                    `db:"id" json:"id"`
	CreatedAt      time.Time              `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time              `db:"updated_at" json:"updated_at"`
	UUID           string                 `db:"uuid" json:"uuid"`
	Type           string                 `db:"type" json:"type"`
	Status         string                 `db:"status" json:"status"`
	ConversationID int                    `db:"conversation_id" json:"conversation_id"`
	Content        string                 `db:"content" json:"content"`
	ContentType    string                 `db:"content_type" json:"content_type"`
	Private        bool                   `db:"private" json:"private"`
	SourceID       null.String            `db:"source_id" json:"-"`
	SenderID       int                    `db:"sender_id" json:"sender_id"`
	SenderType     string                 `db:"sender_type" json:"sender_type"`
	InboxID        int                    `db:"inbox_id" json:"-"`
	Meta           string                 `db:"meta" json:"meta"`
	Attachments    attachment.Attachments `db:"attachments" json:"attachments"`
	// Psuedo fields.
	FirstName        string               `db:"first_name" json:"first_name"`
	LastName         string               `db:"first_name" json:"last_name"`
	SenderUUID       *string              `db:"sender_uuid" json:"sender_uuid"`
	AvatarURL        string               `db:"avatar_url" json:"avatar_url"`
	ConversationUUID string               `db:"conversation_uuid" json:"-"`
	From             string               `db:"from"  json:"-"`
	To               []string             `db:"from"  json:"-"`
	AltContent       string               `db:"alt_content" json:"-"`
	Subject          string               `db:"subject" json:"-"`
	Channel          string               `db:"channel" json:"-"`
	References       []string             `json:"-"`
	InReplyTo        string               `json:"-"`
	Headers          textproto.MIMEHeader `json:"-"`
	Media            []mmodels.Media      `db:"-" json:"-"`
}

// IncomingMessage links a message with the contact information and inbox id.
type IncomingMessage struct {
	Message Message
	Contact cmodels.Contact
	InboxID int
}
