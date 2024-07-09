package inbox

import (
	"context"
	"embed"
	"errors"

	"github.com/abhinavxd/artemis/internal/dbutil"
	"github.com/abhinavxd/artemis/internal/envelope"
	imodels "github.com/abhinavxd/artemis/internal/inbox/models"
	"github.com/abhinavxd/artemis/internal/message/models"
	"github.com/jmoiron/sqlx"
	"github.com/zerodha/logf"
)

var (
	// Embedded filesystem
	//go:embed queries.sql
	efs embed.FS

	ErrInboxNotFound = errors.New("inbox not found")
)

// Closer provides function for closing an inbox.
type Closer interface {
	Close() error
}

// Identifier provides a method for obtaining a unique identifier for the inbox.
type Identifier interface {
	Identifier() int
}

// MessageHandler defines methods for handling message operations.
type MessageHandler interface {
	Receive(context.Context) error
	Send(models.Message) error
}

// Inbox combines the operations of an inbox including its lifecycle, identification, and message handling.
type Inbox interface {
	Closer
	Identifier
	MessageHandler
	FromAddress() string
	Channel() string
}

type MessageStore interface {
	MessageExists(string) (bool, error)
	ProcessMessage(models.IncomingMessage) error
}

// Opts contains the options for the initializing the inbox manager.
type Opts struct {
	QueueSize   int
	Concurrency int
}

// Manager manages the inbox.
type Manager struct {
	queries queries
	inboxes map[int]Inbox
	lo      *logf.Logger
}

// Prepared queries.
type queries struct {
	GetActive   *sqlx.Stmt `query:"get-active-inboxes"`
	GetAll      *sqlx.Stmt `query:"get-all-inboxes"`
	InsertInbox *sqlx.Stmt `query:"insert-inbox"`
}

// New returns a new inbox manager.
func New(lo *logf.Logger, db *sqlx.DB) (*Manager, error) {
	var q queries

	// Scan the sql	file into the queries struct.
	if err := dbutil.ScanSQLFile("queries.sql", &q, db, efs); err != nil {
		return nil, err
	}

	m := &Manager{
		lo:      lo,
		inboxes: make(map[int]Inbox),
		queries: q,
	}
	return m, nil
}

// Register registers the inbox with the manager.
func (m *Manager) Register(i Inbox) {
	m.inboxes[i.Identifier()] = i
}

// Get returns the inbox with the given ID.
func (m *Manager) Get(id int) (Inbox, error) {
	i, ok := m.inboxes[id]
	if !ok {
		return nil, ErrInboxNotFound
	}
	return i, nil
}

// GetActive returns all active inboxes.
func (m *Manager) GetActive() ([]imodels.Inbox, error) {
	var inboxes []imodels.Inbox
	if err := m.queries.GetActive.Select(&inboxes); err != nil {
		m.lo.Error("fetching active inboxes", "error", err)
		return nil, err
	}
	return inboxes, nil
}

// GetAll returns all inboxes.
func (m *Manager) GetAll() ([]imodels.Inbox, error) {
	var inboxes []imodels.Inbox
	if err := m.queries.GetAll.Select(&inboxes); err != nil {
		m.lo.Error("error fetching active inboxes", "error", err)
		return nil, err
	}
	return inboxes, nil
}

// Create creates an inbox.
func (m *Manager) Create(inbox imodels.Inbox) error {
	if _, err := m.queries.InsertInbox.Exec(true, inbox.Channel, inbox.Config, inbox.Name, inbox.From, nil); err != nil {
		m.lo.Error("error creating inbox", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error creating inbox", nil)
	}
	return nil
}

// Receive starts receiver for each inbox.
func (m *Manager) Receive(ctx context.Context) {
	for _, inb := range m.inboxes {
		go inb.Receive(ctx)
	}
}
