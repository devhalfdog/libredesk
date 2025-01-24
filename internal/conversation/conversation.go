// Package conversation manages conversations and messages.
package conversation

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abhinavxd/libredesk/internal/automation"
	amodels "github.com/abhinavxd/libredesk/internal/automation/models"
	"github.com/abhinavxd/libredesk/internal/conversation/models"
	pmodels "github.com/abhinavxd/libredesk/internal/conversation/priority/models"
	smodels "github.com/abhinavxd/libredesk/internal/conversation/status/models"
	"github.com/abhinavxd/libredesk/internal/dbutil"
	"github.com/abhinavxd/libredesk/internal/envelope"
	"github.com/abhinavxd/libredesk/internal/inbox"
	mmodels "github.com/abhinavxd/libredesk/internal/media/models"
	notifier "github.com/abhinavxd/libredesk/internal/notification"
	slaModels "github.com/abhinavxd/libredesk/internal/sla/models"
	tmodels "github.com/abhinavxd/libredesk/internal/team/models"
	"github.com/abhinavxd/libredesk/internal/template"
	umodels "github.com/abhinavxd/libredesk/internal/user/models"
	"github.com/abhinavxd/libredesk/internal/ws"
	"github.com/jmoiron/sqlx"
	"github.com/knadh/go-i18n"
	"github.com/lib/pq"
	"github.com/zerodha/logf"
)

var (
	//go:embed queries.sql
	efs                                  embed.FS
	ErrConversationNotFound              = errors.New("conversation not found")
	ConversationsListAllowedFilterFields = []string{"status_id", "priority_id", "assigned_team_id", "assigned_user_id", "inbox_id"}
	ConversationStatusesFilterFields     = []string{"id", "name"}
)

const (
	conversationsListMaxPageSize = 100
)

// Manager handles the operations related to conversations
type Manager struct {
	q                          queries
	inboxStore                 inboxStore
	userStore                  userStore
	teamStore                  teamStore
	mediaStore                 mediaStore
	statusStore                statusStore
	priorityStore              priorityStore
	slaStore                   slaStore
	notifier                   *notifier.Service
	lo                         *logf.Logger
	db                         *sqlx.DB
	i18n                       *i18n.I18n
	automation                 *automation.Engine
	wsHub                      *ws.Hub
	template                   *template.Manager
	incomingMessageQueue       chan models.IncomingMessage
	outgoingMessageQueue       chan models.Message
	outgoingProcessingMessages sync.Map
	closed                     bool
	closedMu                   sync.RWMutex
	wg                         sync.WaitGroup
}

type slaStore interface {
	ApplySLA(conversationID, assignedTeamID, slaID int) (slaModels.SLAPolicy, error)
}

type statusStore interface {
	Get(int) (smodels.Status, error)
}

type priorityStore interface {
	Get(int) (pmodels.Priority, error)
}

type teamStore interface {
	Get(int) (tmodels.Team, error)
	UserBelongsToTeam(userID, teamID int) (bool, error)
}

type userStore interface {
	Get(int) (umodels.User, error)
	GetSystemUser() (umodels.User, error)
	CreateContact(user *umodels.User) error
}

type mediaStore interface {
	GetBlob(name string) ([]byte, error)
	Attach(id int, model string, modelID int) error
	GetByModel(id int, model string) ([]mmodels.Media, error)
	ContentIDExists(contentID string) (bool, error)
	UploadAndInsert(fileName, contentType, contentID, modelType string, modelID int, content io.ReadSeeker, fileSize int, disposition string, meta []byte) (mmodels.Media, error)
}

type inboxStore interface {
	Get(int) (inbox.Inbox, error)
}

// Opts holds the options for creating a new Manager.
type Opts struct {
	DB                       *sqlx.DB
	Lo                       *logf.Logger
	OutgoingMessageQueueSize int
	IncomingMessageQueueSize int
}

// New initializes a new conversation Manager.
func New(
	wsHub *ws.Hub,
	i18n *i18n.I18n,
	notifier *notifier.Service,
	sla slaStore,
	status statusStore,
	priority priorityStore,
	inboxStore inboxStore,
	userStore userStore,
	teamStore teamStore,
	mediaStore mediaStore,
	automation *automation.Engine,
	template *template.Manager,
	opts Opts) (*Manager, error) {

	var q queries
	if err := dbutil.ScanSQLFile("queries.sql", &q, opts.DB, efs); err != nil {
		return nil, err
	}

	c := &Manager{
		q:                          q,
		wsHub:                      wsHub,
		i18n:                       i18n,
		notifier:                   notifier,
		inboxStore:                 inboxStore,
		userStore:                  userStore,
		teamStore:                  teamStore,
		mediaStore:                 mediaStore,
		slaStore:                   sla,
		statusStore:                status,
		priorityStore:              priority,
		automation:                 automation,
		template:                   template,
		db:                         opts.DB,
		lo:                         opts.Lo,
		incomingMessageQueue:       make(chan models.IncomingMessage, opts.IncomingMessageQueueSize),
		outgoingMessageQueue:       make(chan models.Message, opts.OutgoingMessageQueueSize),
		outgoingProcessingMessages: sync.Map{},
	}

	return c, nil
}

type queries struct {
	// Conversation queries.
	GetLatestReceivedMessageSourceID   *sqlx.Stmt `query:"get-latest-received-message-source-id"`
	GetToAddress                       *sqlx.Stmt `query:"get-to-address"`
	GetConversationUUID                *sqlx.Stmt `query:"get-conversation-uuid"`
	GetConversation                    *sqlx.Stmt `query:"get-conversation"`
	GetConversationsCreatedAfter       *sqlx.Stmt `query:"get-conversations-created-after"`
	GetUnassignedConversations         *sqlx.Stmt `query:"get-unassigned-conversations"`
	GetConversations                   string     `query:"get-conversations"`
	GetConversationParticipants        *sqlx.Stmt `query:"get-conversation-participants"`
	UpdateConversationFirstReplyAt     *sqlx.Stmt `query:"update-conversation-first-reply-at"`
	UpdateConversationAssigneeLastSeen *sqlx.Stmt `query:"update-conversation-assignee-last-seen"`
	UpdateConversationAssignedUser     *sqlx.Stmt `query:"update-conversation-assigned-user"`
	UpdateConversationAssignedTeam     *sqlx.Stmt `query:"update-conversation-assigned-team"`
	RemoveConversationAssignee         *sqlx.Stmt `query:"remove-conversation-assignee"`
	UpdateConversationPriority         *sqlx.Stmt `query:"update-conversation-priority"`
	UpdateConversationStatus           *sqlx.Stmt `query:"update-conversation-status"`
	UpdateConversationLastMessage      *sqlx.Stmt `query:"update-conversation-last-message"`
	InsertConversationParticipant      *sqlx.Stmt `query:"insert-conversation-participant"`
	InsertConversation                 *sqlx.Stmt `query:"insert-conversation"`
	UpsertConversationTags             *sqlx.Stmt `query:"upsert-conversation-tags"`
	UnassignOpenConversations          *sqlx.Stmt `query:"unassign-open-conversations"`
	UnsnoozeAll                        *sqlx.Stmt `query:"unsnooze-all"`

	// Dashboard queries.
	GetDashboardCharts string `query:"get-dashboard-charts"`
	GetDashboardCounts string `query:"get-dashboard-counts"`

	// Message queries.
	GetMessage                         *sqlx.Stmt `query:"get-message"`
	GetMessages                        string     `query:"get-messages"`
	GetPendingMessages                 *sqlx.Stmt `query:"get-pending-messages"`
	GetConversationUUIDFromMessageUUID *sqlx.Stmt `query:"get-conversation-uuid-from-message-uuid"`
	InsertMessage                      *sqlx.Stmt `query:"insert-message"`
	UpdateMessageStatus                *sqlx.Stmt `query:"update-message-status"`
	MessageExistsBySourceID            *sqlx.Stmt `query:"message-exists-by-source-id"`
	GetConversationByMessageID         *sqlx.Stmt `query:"get-conversation-by-message-id"`
}

// CreateConversation creates a new conversation and returns its ID and UUID.
func (c *Manager) CreateConversation(contactID, contactChannelID, inboxID int, lastMessage string, lastMessageAt time.Time, subject string) (int, string, error) {
	var (
		id   int
		uuid string
	)
	if err := c.q.InsertConversation.QueryRow(contactID, contactChannelID, models.StatusOpen, inboxID, lastMessage, lastMessageAt, subject).Scan(&id, &uuid); err != nil {
		c.lo.Error("error inserting new conversation into the DB", "error", err)
		return id, uuid, err
	}
	return id, uuid, nil
}

// GetConversation retrieves a conversation by its UUID.
func (c *Manager) GetConversation(id int, uuid string) (models.Conversation, error) {
	var conversation models.Conversation
	if err := c.q.GetConversation.Get(&conversation, id, uuid); err != nil {
		if err == sql.ErrNoRows {
			return conversation, envelope.NewError(envelope.InputError, "Conversation not found", nil)
		}
		c.lo.Error("error fetching conversation", "error", err)
		return conversation, envelope.NewError(envelope.GeneralError, "Error fetching conversation", nil)
	}
	return conversation, nil
}

// GetConversationsCreatedAfter retrieves conversations created after the specified time.
func (c *Manager) GetConversationsCreatedAfter(time time.Time) ([]models.Conversation, error) {
	var conversations = make([]models.Conversation, 0)
	if err := c.q.GetConversationsCreatedAfter.Select(&conversations, time); err != nil {
		c.lo.Error("error fetching conversation", "error", err)
		return conversations, err
	}
	return conversations, nil
}

// UpdateConversationAssigneeLastSeen updates the last seen timestamp of assignee.
func (c *Manager) UpdateConversationAssigneeLastSeen(uuid string) error {
	if _, err := c.q.UpdateConversationAssigneeLastSeen.Exec(uuid); err != nil {
		c.lo.Error("error updating conversation", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error updating assignee last seen", nil)
	}

	// Broadcast the property update to all subscribers.
	c.BroadcastConversationUpdate(uuid, "assignee_last_seen_at", time.Now().Format(time.RFC3339))
	return nil
}

// GetConversationParticipants retrieves the participants of a conversation.
func (c *Manager) GetConversationParticipants(uuid string) ([]models.ConversationParticipant, error) {
	conv := make([]models.ConversationParticipant, 0)
	if err := c.q.GetConversationParticipants.Select(&conv, uuid); err != nil {
		c.lo.Error("error fetching conversation", "error", err)
		return conv, envelope.NewError(envelope.GeneralError, "Error fetching conversation participants", nil)
	}
	return conv, nil
}

// GetUnassignedConversations retrieves unassigned conversations.
func (c *Manager) GetUnassignedConversations() ([]models.Conversation, error) {
	var conv []models.Conversation
	if err := c.q.GetUnassignedConversations.Select(&conv); err != nil {
		if err != sql.ErrNoRows {
			c.lo.Error("error fetching conversations", "error", err)
			return conv, err
		}
	}
	return conv, nil
}

// GetConversationUUID retrieves the UUID of a conversation by its ID.
func (c *Manager) GetConversationUUID(id int) (string, error) {
	var uuid string
	if err := c.q.GetConversationUUID.QueryRow(id).Scan(&uuid); err != nil {
		if err == sql.ErrNoRows {
			return uuid, err
		}
		c.lo.Error("fetching conversation from DB", "error", err)
		return uuid, err
	}
	return uuid, nil
}

// GetAllConversationsList retrieves all conversations with optional filtering, ordering, and pagination.
func (c *Manager) GetAllConversationsList(order, orderBy, filters string, page, pageSize int) ([]models.Conversation, error) {
	return c.GetConversations(0, []int{}, []string{models.AllConversations}, order, orderBy, filters, page, pageSize)
}

// GetAssignedConversationsList retrieves conversations assigned to a specific user with optional filtering, ordering, and pagination.
func (c *Manager) GetAssignedConversationsList(userID int, order, orderBy, filters string, page, pageSize int) ([]models.Conversation, error) {
	return c.GetConversations(userID, []int{}, []string{models.AssignedConversations}, order, orderBy, filters, page, pageSize)
}

// GetUnassignedConversationsList retrieves conversations assigned to a team the user is part of with optional filtering, ordering, and pagination.
func (c *Manager) GetUnassignedConversationsList(order, orderBy, filters string, page, pageSize int) ([]models.Conversation, error) {
	return c.GetConversations(0, []int{}, []string{models.UnassignedConversations}, order, orderBy, filters, page, pageSize)
}

// GetTeamUnassignedConversationsList retrieves conversations assigned to a team with optional filtering, ordering, and pagination.
func (c *Manager) GetTeamUnassignedConversationsList(teamID int, order, orderBy, filters string, page, pageSize int) ([]models.Conversation, error) {
	return c.GetConversations(0, []int{teamID}, []string{models.TeamUnassignedConversations}, order, orderBy, filters, page, pageSize)
}

func (c *Manager) GetViewConversationsList(userID int, teamIDs []int, listType []string, order, orderBy, filters string, page, pageSize int) ([]models.Conversation, error) {
	return c.GetConversations(userID, teamIDs, listType, order, orderBy, filters, page, pageSize)
}

// GetConversations retrieves conversations list based on user ID, type, and optional filtering, ordering, and pagination.
func (c *Manager) GetConversations(userID int, teamIDs []int, listTypes []string, order, orderBy, filters string, page, pageSize int) ([]models.Conversation, error) {
	var conversations = make([]models.Conversation, 0)

	// Make the query.
	query, qArgs, err := c.makeConversationsListQuery(userID, teamIDs, listTypes, c.q.GetConversations, order, orderBy, page, pageSize, filters)
	if err != nil {
		c.lo.Error("error making conversations query", "error", err)
		return conversations, envelope.NewError(envelope.GeneralError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.entities.conversations}"), nil)
	}

	tx, err := c.db.BeginTxx(context.Background(), nil)
	defer tx.Rollback()
	if err != nil {
		c.lo.Error("error preparing get conversations query", "error", err)
		return conversations, envelope.NewError(envelope.GeneralError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.entities.conversations}"), nil)
	}

	if err := tx.Select(&conversations, query, qArgs...); err != nil {
		c.lo.Error("error fetching conversations", "error", err)
		return conversations, envelope.NewError(envelope.GeneralError, c.i18n.Ts("globals.messages.errorFetching", "name", "{globals.entities.conversations}"), nil)
	}
	return conversations, nil
}

// UpdateConversationLastMessage updates the last message details for a conversation.
func (c *Manager) UpdateConversationLastMessage(convesationID int, conversationUUID, lastMessage string, lastMessageAt time.Time) error {
	if _, err := c.q.UpdateConversationLastMessage.Exec(convesationID, conversationUUID, lastMessage, lastMessageAt); err != nil {
		c.lo.Error("error updating conversation last message", "error", err)
		return err
	}
	return nil
}

// UpdateConversationFirstReplyAt updates the first reply timestamp for a conversation.
func (c *Manager) UpdateConversationFirstReplyAt(conversationUUID string, conversationID int, at time.Time) error {
	res, err := c.q.UpdateConversationFirstReplyAt.Exec(conversationID, at)
	if err != nil {
		c.lo.Error("error updating conversation first reply at", "error", err)
		return err
	}

	rows, _ := res.RowsAffected()
	if rows > 0 {
		c.BroadcastConversationUpdate(conversationUUID, "first_reply_at", at.Format(time.RFC3339))
	}
	return nil
}

// UpdateConversationUserAssignee sets the assignee of a conversation to a specifc user.
func (c *Manager) UpdateConversationUserAssignee(uuid string, assigneeID int, actor umodels.User) error {
	if err := c.UpdateAssignee(uuid, assigneeID, models.AssigneeTypeUser); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error updating assignee", nil)
	}

	conversation, err := c.GetConversation(0, uuid)
	if err != nil {
		return err
	}

	// Send email to assignee.
	if err := c.SendAssignedConversationEmail([]int{assigneeID}, conversation); err != nil {
		c.lo.Error("error sending assigned conversation email", "error", err)
	}

	if err := c.RecordAssigneeUserChange(uuid, assigneeID, actor); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error recording assignee change", nil)
	}
	return nil
}

// UpdateConversationTeamAssignee sets the assignee of a conversation to a specific team and sets the assigned user id to NULL.
func (c *Manager) UpdateConversationTeamAssignee(uuid string, teamID int, actor umodels.User) error {
	if err := c.UpdateAssignee(uuid, teamID, models.AssigneeTypeTeam); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error updating assignee", nil)
	}
	if err := c.RecordAssigneeTeamChange(uuid, teamID, actor); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error recording assignee change", nil)
	}
	return nil
}

// UpdateAssignee updates the assignee of a conversation.
func (c *Manager) UpdateAssignee(uuid string, assigneeID int, assigneeType string) error {
	var prop string
	switch assigneeType {
	case models.AssigneeTypeUser:
		prop = "assigned_user_id"
		if _, err := c.q.UpdateConversationAssignedUser.Exec(uuid, assigneeID); err != nil {
			c.lo.Error("error updating conversation assignee", "error", err)
			return fmt.Errorf("updating assignee: %w", err)
		}
	case models.AssigneeTypeTeam:
		prop = "assigned_team_id"
		if _, err := c.q.UpdateConversationAssignedTeam.Exec(uuid, assigneeID); err != nil {
			c.lo.Error("error updating conversation assignee", "error", err)
			return fmt.Errorf("updating assignee: %w", err)
		}
	default:
		return fmt.Errorf("invalid assignee type: %s", assigneeType)
	}
	// Broadcast update to all subscribers.
	c.BroadcastConversationUpdate(uuid, prop, assigneeID)
	return nil
}

// UpdateConversationPriority updates the priority of a conversation.
func (c *Manager) UpdateConversationPriority(uuid string, priorityID int, priority string, actor umodels.User) error {
	// Fetch the priority name if priority ID is provided.
	if priorityID > 0 {
		p, err := c.priorityStore.Get(priorityID)
		if err != nil {
			return envelope.NewError(envelope.InputError, err.Error(), nil)
		}
		priority = p.Name
	}
	if _, err := c.q.UpdateConversationPriority.Exec(uuid, priority); err != nil {
		c.lo.Error("error updating conversation priority", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error updating priority", nil)
	}
	if err := c.RecordPriorityChange(priority, uuid, actor); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error recording priority change", nil)
	}
	c.BroadcastConversationUpdate(uuid, "priority", priority)
	return nil
}

// UpdateConversationStatus updates the status of a conversation.
func (c *Manager) UpdateConversationStatus(uuid string, statusID int, status, snoozeDur string, actor umodels.User) error {
	// Fetch the status name if status ID is provided.
	if statusID > 0 {
		s, err := c.statusStore.Get(statusID)
		if err != nil {
			return envelope.NewError(envelope.InputError, err.Error(), nil)
		}
		status = s.Name
	}

	if status == models.StatusSnoozed && snoozeDur == "" {
		return envelope.NewError(envelope.InputError, "Snooze duration is required", nil)
	}

	// Parse the snooze duration if status is snoozed.
	snoozeUntil := time.Time{}
	if status == models.StatusSnoozed {
		duration, err := time.ParseDuration(snoozeDur)
		if err != nil {
			c.lo.Error("error parsing snooze duration", "error", err)
			return envelope.NewError(envelope.InputError, "Invalid snooze duration format", nil)
		}
		snoozeUntil = time.Now().Add(duration)
	}

	// Update the conversation status.
	if _, err := c.q.UpdateConversationStatus.Exec(uuid, status, snoozeUntil); err != nil {
		c.lo.Error("error updating conversation status", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error updating status", nil)
	}

	// Record the status change as an activity.
	if err := c.RecordStatusChange(status, uuid, actor); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error recording status change", nil)
	}

	// Send WS update to all subscribers.
	c.BroadcastConversationUpdate(uuid, "status", status)
	return nil
}

// GetDashboardCounts returns dashboard counts
// TODO: Rename to overview [reports/overview].
func (c *Manager) GetDashboardCounts(userID, teamID int) (json.RawMessage, error) {
	var counts = json.RawMessage{}
	tx, err := c.db.BeginTxx(context.Background(), nil)
	if err != nil {
		c.lo.Error("error starting db txn", "error", err)
		return nil, envelope.NewError(envelope.GeneralError, "Error fetching dashboard counts", nil)
	}
	defer tx.Rollback()

	var (
		cond  string
		qArgs []interface{}
	)
	if userID > 0 {
		cond = " AND assigned_user_id = $1"
		qArgs = append(qArgs, userID)
	} else if teamID > 0 {
		cond = " AND assigned_team_id = $1"
		qArgs = append(qArgs, teamID)
	}
	// TODO: Add date range filter support.
	cond += " AND c.created_at >= NOW() - INTERVAL '30 days'"

	query := fmt.Sprintf(c.q.GetDashboardCounts, cond)
	if err := tx.Get(&counts, query, qArgs...); err != nil {
		c.lo.Error("error fetching dashboard counts", "error", err)
		return nil, envelope.NewError(envelope.GeneralError, "Error fetching dashboard counts", nil)
	}

	if err := tx.Commit(); err != nil {
		c.lo.Error("error committing db txn", "error", err)
		return nil, envelope.NewError(envelope.GeneralError, "Error fetching dashboard counts", nil)
	}

	return counts, nil
}

// GetDashboardChart returns dashboard chart data
func (c *Manager) GetDashboardChart(userID, teamID int) (json.RawMessage, error) {
	var stats = json.RawMessage{}
	tx, err := c.db.BeginTxx(context.Background(), nil)
	if err != nil {
		c.lo.Error("error starting db txn", "error", err)
		return nil, envelope.NewError(envelope.GeneralError, "Error fetching dashboard charts", nil)
	}
	defer tx.Rollback()

	var (
		cond  string
		qArgs []interface{}
	)

	// TODO: Add date range filter support.
	if userID > 0 {
		cond = " AND assigned_user_id = $1"
		qArgs = append(qArgs, userID)
	} else if teamID > 0 {
		cond = " AND assigned_team_id = $1"
		qArgs = append(qArgs, teamID)
	}
	cond += " AND c.created_at >= NOW() - INTERVAL '90 days'"

	// Apply the same condition across queries.
	query := fmt.Sprintf(c.q.GetDashboardCharts, cond, cond, cond, cond)
	if err := tx.Get(&stats, query, qArgs...); err != nil {
		c.lo.Error("error fetching dashboard charts", "error", err)
		return nil, envelope.NewError(envelope.GeneralError, "Error fetching dashboard charts", nil)
	}
	return stats, nil
}

// UpsertConversationTags upserts the tags associated with a conversation.
func (t *Manager) UpsertConversationTags(uuid string, tagNames []string) error {
	if _, err := t.q.UpsertConversationTags.Exec(uuid, pq.Array(tagNames)); err != nil {
		t.lo.Error("error upserting conversation tags", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error upserting tags", nil)
	}
	return nil
}

// makeConversationsListQuery prepares a SQL query string for conversations list
func (c *Manager) makeConversationsListQuery(userID int, teamIDs []int, listTypes []string, baseQuery, order, orderBy string, page, pageSize int, filtersJSON string) (string, []interface{}, error) {
	var qArgs []interface{}

	// Set defaults
	if orderBy == "" {
		orderBy = "last_message_at"
	}
	if order == "" {
		order = "DESC"
	}
	if filtersJSON == "" {
		filtersJSON = "[]"
	}

	// Validate inputs
	if pageSize > conversationsListMaxPageSize || pageSize < 1 {
		return "", nil, fmt.Errorf("invalid page size: must be between 1 and %d", conversationsListMaxPageSize)
	}
	if page < 1 {
		return "", nil, fmt.Errorf("page must be greater than 0")
	}

	if len(listTypes) == 0 {
		return "", nil, fmt.Errorf("no conversation list types specified")
	}

	// Prepare the conditions based on the list types.
	conditions := []string{}
	for _, lt := range listTypes {
		switch lt {
		case models.AssignedConversations:
			conditions = append(conditions, fmt.Sprintf("conversations.assigned_user_id = $%d", len(qArgs)+1))
			qArgs = append(qArgs, userID)
		case models.UnassignedConversations:
			conditions = append(conditions, "conversations.assigned_user_id IS NULL AND conversations.assigned_team_id IS NULL")
		case models.TeamUnassignedConversations:
			placeholders := make([]string, len(teamIDs))
			for i := range teamIDs {
				placeholders[i] = fmt.Sprintf("$%d", len(qArgs)+i+1)
			}
			conditions = append(conditions, fmt.Sprintf("(conversations.assigned_team_id IN (%s) AND conversations.assigned_user_id IS NULL)", strings.Join(placeholders, ",")))
			for _, id := range teamIDs {
				qArgs = append(qArgs, id)
			}
		case models.AllConversations:
			// No conditions needed for all conversations.
		default:
			return "", nil, fmt.Errorf("unknown conversation type: %s", lt)
		}
	}

	if len(conditions) > 0 {
		baseQuery = fmt.Sprintf(baseQuery, "AND ("+strings.Join(conditions, " OR ")+")")
	} else {
		// Replace the `%s` in the base query with an empty string.
		baseQuery = fmt.Sprintf(baseQuery, "")
	}

	return dbutil.BuildPaginatedQuery(baseQuery, qArgs, dbutil.PaginationOptions{
		Order:    order,
		OrderBy:  orderBy,
		Page:     page,
		PageSize: pageSize,
	}, filtersJSON, dbutil.AllowedFields{
		"conversations":         ConversationsListAllowedFilterFields,
		"conversation_statuses": ConversationStatusesFilterFields,
	})
}

// GetToAddress retrieves the recipient addresses for a conversation and channel.
func (m *Manager) GetToAddress(conversationID int) ([]string, error) {
	var addr []string
	if err := m.q.GetToAddress.Select(&addr, conversationID); err != nil {
		m.lo.Error("error fetching `to` address for message", "error", err, "conversation_id", conversationID)
		return addr, err
	}
	return addr, nil
}

// GetLatestReceivedMessageSourceID returns the last received message source ID.
func (m *Manager) GetLatestReceivedMessageSourceID(conversationID int) (string, error) {
	var out string
	if err := m.q.GetLatestReceivedMessageSourceID.Get(&out, conversationID); err != nil {
		m.lo.Error("error fetching message source id", "error", err, "conversation_id", conversationID)
		return out, err
	}
	return out, nil
}

// SendAssignedConversationEmail sends a email for an assigned conversation to the passed user ids.
func (m *Manager) SendAssignedConversationEmail(userIDs []int, conversation models.Conversation) error {
	agent, err := m.userStore.Get(userIDs[0])
	if err != nil {
		m.lo.Error("error fetching agent", "error", err)
		return fmt.Errorf("fetching agent: %w", err)
	}

	content, subject, err := m.template.RenderNamedTemplate(template.TmplConversationAssigned,
		map[string]interface{}{
			"conversation": map[string]string{
				"subject":          conversation.Subject.String,
				"uuid":             conversation.UUID,
				"reference_number": conversation.ReferenceNumber,
				"priority":         conversation.Priority.String,
			},
			"agent": map[string]string{
				"full_name": agent.FullName(),
			},
		})
	if err != nil {
		m.lo.Error("error rendering template", "template", template.TmplConversationAssigned, "conversation_uuid", conversation.UUID, "error", err)
		return fmt.Errorf("rendering template: %w", err)
	}
	nm := notifier.Message{
		UserIDs:  userIDs,
		Subject:  subject,
		Content:  content,
		Provider: notifier.ProviderEmail,
	}
	if err := m.notifier.Send(nm); err != nil {
		m.lo.Error("error sending notification message", "error", err)
		return fmt.Errorf("sending notification message: %w", err)
	}
	return nil
}

// UnassignOpen unassigns all open conversations belonging to a user.
// i.e conversations without status `Closed` and `Resolved`.
func (m *Manager) UnassignOpen(userID int) error {
	if _, err := m.q.UnassignOpenConversations.Exec(userID); err != nil {
		m.lo.Error("error unassigning open conversations", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error unassigning open conversations", nil)
	}
	return nil
}

// ApplySLA applies the SLA policy to a conversation.
func (m *Manager) ApplySLA(conversationUUID string, conversationID, policyID int, actor umodels.User) error {
	policy, err := m.slaStore.ApplySLA(conversationID, 0, policyID)
	if err != nil {
		return envelope.NewError(envelope.GeneralError, "Error applying SLA", nil)
	}

	// Record the SLA application as an activity.
	if err := m.RecordSLASet(conversationUUID, policy.Name, actor); err != nil {
		return envelope.NewError(envelope.GeneralError, "Error recording SLA application", nil)
	}
	return nil
}

// ApplyAction applies an action to a conversation, this can be called from multiple packages across the app to perform actions on conversations.
// all actions are executed on behalf of the provided user if the user is not provided, system user is used.
func (m *Manager) ApplyAction(action amodels.RuleAction, conversation models.Conversation, user umodels.User) error {
	if len(action.Value) == 0 {
		m.lo.Warn("no value provided for action", "action", action.Type, "conversation_uuid", conversation.UUID)
		return fmt.Errorf("no value provided for action %s", action.Type)
	}

	// If user is not provided, use system user.
	if user.ID == 0 {
		systemUser, err := m.userStore.GetSystemUser()
		if err != nil {
			return fmt.Errorf("could not apply %s action. could not fetch system user: %w", action.Type, err)
		}
		user = systemUser
	}

	switch action.Type {
	case amodels.ActionAssignTeam:
		m.lo.Debug("executing assign team action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		teamID, _ := strconv.Atoi(action.Value[0])
		if err := m.UpdateConversationTeamAssignee(conversation.UUID, teamID, user); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionAssignUser:
		m.lo.Debug("executing assign user action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		agentID, _ := strconv.Atoi(action.Value[0])
		if err := m.UpdateConversationUserAssignee(conversation.UUID, agentID, user); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionSetPriority:
		m.lo.Debug("executing set priority action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		priorityID, _ := strconv.Atoi(action.Value[0])
		if err := m.UpdateConversationPriority(conversation.UUID, priorityID, "", user); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionSetStatus:
		m.lo.Debug("executing set status action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		statusID, _ := strconv.Atoi(action.Value[0])
		if err := m.UpdateConversationStatus(conversation.UUID, statusID, "", "", user); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionSendPrivateNote:
		m.lo.Debug("executing send private note action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		if err := m.SendPrivateNote([]mmodels.Media{}, user.ID, conversation.UUID, action.Value[0]); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionReply:
		m.lo.Debug("executing reply action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		if err := m.SendReply([]mmodels.Media{}, user.ID, conversation.UUID, action.Value[0], []string{}, []string{}, map[string]interface{}{}); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionSetSLA:
		m.lo.Debug("executing apply SLA action", "value", action.Value[0], "conversation_uuid", conversation.UUID)
		slaPolicyID, _ := strconv.Atoi(action.Value[0])
		if err := m.ApplySLA(conversation.UUID, conversation.ID, slaPolicyID, user); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	case amodels.ActionSetTags:
		m.lo.Debug("executing set tags action", "value", action.Value, "conversation_uuid", conversation.UUID)
		if err := m.UpsertConversationTags(conversation.UUID, action.Value); err != nil {
			return fmt.Errorf("could not apply %s action: %w", action.Type, err)
		}
	default:
		return fmt.Errorf("unrecognized action type %s", action.Type)
	}
	return nil
}

// RemoveConversationAssignee removes the assignee from the conversation.
func (m *Manager) RemoveConversationAssignee(uuid, typ string) error {
	if _, err := m.q.RemoveConversationAssignee.Exec(uuid, typ); err != nil {
		m.lo.Error("error removing conversation assignee", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error removing conversation assignee", nil)
	}
	return nil
}

// addConversationParticipant adds a user as participant to a conversation.
func (c *Manager) addConversationParticipant(userID int, conversationUUID string) error {
	if _, err := c.q.InsertConversationParticipant.Exec(userID, conversationUUID); err != nil {
		if !dbutil.IsUniqueViolationError(err) {
			c.lo.Error("error adding conversation participant", "user_id", userID, "conversation_uuid", conversationUUID, "error", err)
			return fmt.Errorf("adding conversation participant: %w", err)
		}
	}
	return nil
}
