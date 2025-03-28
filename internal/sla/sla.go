package sla

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	businessHours "github.com/abhinavxd/libredesk/internal/business_hours"
	bmodels "github.com/abhinavxd/libredesk/internal/business_hours/models"
	"github.com/abhinavxd/libredesk/internal/dbutil"
	"github.com/abhinavxd/libredesk/internal/envelope"
	models "github.com/abhinavxd/libredesk/internal/sla/models"
	tmodels "github.com/abhinavxd/libredesk/internal/team/models"
	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/volatiletech/null/v9"
	"github.com/zerodha/logf"
)

var (
	//go:embed queries.sql
	efs embed.FS
)

const (
	SLATypeFirstResponse = "first_response"
	SLATypeResolution    = "resolution"
)

// Manager manages SLA policies and calculations.
type Manager struct {
	q                queries
	lo               *logf.Logger
	teamStore        teamStore
	appSettingsStore appSettingsStore
	businessHrsStore businessHrsStore
	wg               sync.WaitGroup
	opts             Opts
}

// Opts defines the options for creating SLA manager.
type Opts struct {
	DB *sqlx.DB
	Lo *logf.Logger
}

// Deadlines holds the deadlines for an SLA policy.
type Deadlines struct {
	FirstResponse time.Time
	Resolution    time.Time
}

type teamStore interface {
	Get(id int) (tmodels.Team, error)
}

type appSettingsStore interface {
	GetByPrefix(prefix string) (types.JSONText, error)
}

type businessHrsStore interface {
	Get(id int) (bmodels.BusinessHours, error)
}

// queries hold prepared SQL queries.
type queries struct {
	GetSLA             *sqlx.Stmt `query:"get-sla-policy"`
	GetAllSLA          *sqlx.Stmt `query:"get-all-sla-policies"`
	InsertSLA          *sqlx.Stmt `query:"insert-sla-policy"`
	DeleteSLA          *sqlx.Stmt `query:"delete-sla-policy"`
	UpdateSLA          *sqlx.Stmt `query:"update-sla-policy"`
	ApplySLA           *sqlx.Stmt `query:"apply-sla"`
	GetPendingSLAs     *sqlx.Stmt `query:"get-pending-slas"`
	UpdateBreach       *sqlx.Stmt `query:"update-breach"`
	UpdateMet          *sqlx.Stmt `query:"update-met"`
	SetNextSLADeadline *sqlx.Stmt `query:"set-next-sla-deadline"`
	UpdateSLAStatus    *sqlx.Stmt `query:"update-sla-status"`
}

// New creates a new SLA manager.
func New(opts Opts, teamStore teamStore, appSettingsStore appSettingsStore, businessHrsStore businessHrsStore) (*Manager, error) {
	var q queries
	if err := dbutil.ScanSQLFile("queries.sql", &q, opts.DB, efs); err != nil {
		return nil, err
	}
	return &Manager{q: q, lo: opts.Lo, teamStore: teamStore, appSettingsStore: appSettingsStore, businessHrsStore: businessHrsStore, opts: opts}, nil
}

// Get retrieves an SLA by ID.
func (m *Manager) Get(id int) (models.SLAPolicy, error) {
	var sla models.SLAPolicy
	if err := m.q.GetSLA.Get(&sla, id); err != nil {
		m.lo.Error("error fetching SLA", "error", err)
		return sla, envelope.NewError(envelope.GeneralError, "Error fetching SLA", nil)
	}
	return sla, nil
}

// GetAll fetches all SLA policies.
func (m *Manager) GetAll() ([]models.SLAPolicy, error) {
	var slas = make([]models.SLAPolicy, 0)
	if err := m.q.GetAllSLA.Select(&slas); err != nil {
		m.lo.Error("error fetching SLAs", "error", err)
		return nil, envelope.NewError(envelope.GeneralError, "Error fetching SLAs", nil)
	}
	return slas, nil
}

// Create creates a new SLA policy.
func (m *Manager) Create(name, description string, firstResponseTime, resolutionTime string) error {
	if _, err := m.q.InsertSLA.Exec(name, description, firstResponseTime, resolutionTime); err != nil {
		m.lo.Error("error inserting SLA", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error creating SLA", nil)
	}
	return nil
}

// Delete deletes an SLA policy.
func (m *Manager) Delete(id int) error {
	if _, err := m.q.DeleteSLA.Exec(id); err != nil {
		m.lo.Error("error deleting SLA", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error deleting SLA", nil)
	}
	return nil
}

// Update updates an existing SLA policy.
func (m *Manager) Update(id int, name, description string, firstResponseTime, resolutionTime string) error {
	if _, err := m.q.UpdateSLA.Exec(id, name, description, firstResponseTime, resolutionTime); err != nil {
		m.lo.Error("error updating SLA", "error", err)
		return envelope.NewError(envelope.GeneralError, "Error updating SLA", nil)
	}
	return nil
}

// getBusinessHoursAndTimezone returns the business hours ID and timezone for a team, falling back to app settings.
func (m *Manager) getBusinessHoursAndTimezone(assignedTeamID int) (bmodels.BusinessHours, string, error) {
	var (
		businessHrsID int
		timezone      string
		bh            bmodels.BusinessHours
	)

	// Fetch from team if assignedTeamID is provided.
	if assignedTeamID != 0 {
		team, err := m.teamStore.Get(assignedTeamID)
		if err != nil {
			return bh, "", err
		}
		businessHrsID = team.BusinessHoursID.Int
		timezone = team.Timezone
	}

	// Else fetch from app settings, this is System default.
	if businessHrsID == 0 || timezone == "" {
		settingsJ, err := m.appSettingsStore.GetByPrefix("app")
		if err != nil {
			return bh, "", err
		}

		var out map[string]interface{}
		if err := json.Unmarshal([]byte(settingsJ), &out); err != nil {
			return bh, "", fmt.Errorf("parsing settings: %v", err)
		}

		businessHrsIDStr, _ := out["app.business_hours_id"].(string)
		businessHrsID, _ = strconv.Atoi(businessHrsIDStr)
		timezone, _ = out["app.timezone"].(string)
	}

	// If still not found, return error.
	if businessHrsID == 0 || timezone == "" {
		return bh, "", fmt.Errorf("business hours or timezone not configured")
	}

	bh, err := m.businessHrsStore.Get(businessHrsID)
	if err != nil {
		if err == businessHours.ErrBusinessHoursNotFound {
			m.lo.Warn("business hours not found", "team_id", assignedTeamID)
			return bh, "", fmt.Errorf("business hours not found")
		}
		m.lo.Error("error fetching business hours for SLA", "error", err)
		return bh, "", err
	}
	return bh, timezone, nil
}

// CalculateDeadline calculates the deadline for a given start time and duration.
func (m *Manager) CalculateDeadlines(startTime time.Time, slaPolicyID, assignedTeamID int) (Deadlines, error) {
	var deadlines Deadlines

	businessHrs, timezone, err := m.getBusinessHoursAndTimezone(assignedTeamID)
	if err != nil {
		return deadlines, err
	}

	m.lo.Info("calculating deadlines", "timezone", timezone, "business_hours_always_open", businessHrs.IsAlwaysOpen, "business_hours", businessHrs.Hours)

	sla, err := m.Get(slaPolicyID)
	if err != nil {
		return deadlines, err
	}

	// Helper function to calculate deadlines by parsing the duration string.
	calculateDeadline := func(durationStr string) (time.Time, error) {
		if durationStr == "" {
			return time.Time{}, nil
		}
		dur, err := time.ParseDuration(durationStr)
		if err != nil {
			return time.Time{}, fmt.Errorf("parsing SLA duration: %v", err)
		}
		deadline, err := m.CalculateDeadline(startTime, int(dur.Minutes()), businessHrs, timezone)
		if err != nil {
			return time.Time{}, err
		}
		return deadline, nil
	}

	if deadlines.FirstResponse, err = calculateDeadline(sla.FirstResponseTime); err != nil {
		return deadlines, err
	}
	if deadlines.Resolution, err = calculateDeadline(sla.ResolutionTime); err != nil {
		return deadlines, err
	}
	return deadlines, nil
}

// ApplySLA applies an SLA policy to a conversation.
func (m *Manager) ApplySLA(startTime time.Time, conversationID, assignedTeamID, slaPolicyID int) (models.SLAPolicy, error) {
	var sla models.SLAPolicy

	deadlines, err := m.CalculateDeadlines(startTime, slaPolicyID, assignedTeamID)
	if err != nil {
		return sla, err
	}
	if _, err := m.q.ApplySLA.Exec(
		conversationID,
		slaPolicyID,
		deadlines.FirstResponse,
		deadlines.Resolution,
	); err != nil {
		m.lo.Error("error applying SLA", "error", err)
		return sla, envelope.NewError(envelope.GeneralError, "Error applying SLA", nil)
	}
	sla, err = m.Get(slaPolicyID)
	if err != nil {
		return sla, err
	}
	return sla, nil
}

// Run starts the SLA evaluation loop and evaluates pending SLAs.
func (m *Manager) Run(ctx context.Context, evalInterval time.Duration) {
	ticker := time.NewTicker(evalInterval)
	m.wg.Add(1)
	defer func() {
		m.wg.Done()
		ticker.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.evaluatePendingSLAs(ctx); err != nil {
				m.lo.Error("error processing pending SLAs", "error", err)
			}
		}
	}
}

// Close closes the SLA evaluation loop by stopping the worker pool.
func (m *Manager) Close() error {
	m.wg.Wait()
	return nil
}

// evaluatePendingSLAs fetches unbreached SLAs and evaluates them.
// Here evaluation means checking if the SLA deadlines have been met or breached and updating timestamps accordingly.
func (m *Manager) evaluatePendingSLAs(ctx context.Context) error {
	var pendingSLAs []models.AppliedSLA
	if err := m.q.GetPendingSLAs.SelectContext(ctx, &pendingSLAs); err != nil {
		m.lo.Error("error fetching pending SLAs", "error", err)
		return err
	}
	m.lo.Info("evaluating pending SLAs", "count", len(pendingSLAs))
	for _, sla := range pendingSLAs {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := m.evaluateSLA(sla); err != nil {
				m.lo.Error("error evaluating SLA", "error", err)
			}
		}
	}
	m.lo.Info("evaluated pending SLAs", "count", len(pendingSLAs))
	return nil
}

// evaluateSLA evaluates an SLA policy on an applied SLA.
func (m *Manager) evaluateSLA(sla models.AppliedSLA) error {
	m.lo.Debug("evaluating SLA", "conversation_id", sla.ConversationID, "applied_sla_id", sla.ID)
	checkDeadline := func(deadline time.Time, metAt null.Time, slaType string) error {
		if deadline.IsZero() {
			m.lo.Debug("deadline zero, skipping checking the deadline")
			return nil
		}

		now := time.Now()
		if !metAt.Valid && now.After(deadline) {
			m.lo.Debug("SLA breached as current time is after deadline", "deadline", deadline, "now", now, "sla_type", slaType)
			if _, err := m.q.UpdateBreach.Exec(sla.ID, slaType); err != nil {
				return fmt.Errorf("updating SLA breach: %w", err)
			}
			return nil
		}

		if metAt.Valid {
			if metAt.Time.After(deadline) {
				m.lo.Debug("SLA breached as met_at is after deadline", "deadline", deadline, "met_at", metAt.Time, "sla_type", slaType)
				if _, err := m.q.UpdateBreach.Exec(sla.ID, slaType); err != nil {
					return fmt.Errorf("updating SLA breach: %w", err)
				}
			} else {
				m.lo.Debug("SLA type met", "deadline", deadline, "met_at", metAt.Time, "sla_type", slaType)
				if _, err := m.q.UpdateMet.Exec(sla.ID, slaType); err != nil {
					return fmt.Errorf("updating SLA met: %w", err)
				}
			}
		}
		return nil
	}

	// If first response is not breached and not met, check the deadline and set them.
	if !sla.FirstResponseBreachedAt.Valid && !sla.FirstResponseMetAt.Valid {
		m.lo.Debug("checking deadline", "deadline", sla.FirstResponseDeadlineAt, "met_at", sla.ConversationFirstResponseAt.Time, "sla_type", SLATypeFirstResponse)
		if err := checkDeadline(sla.FirstResponseDeadlineAt, sla.ConversationFirstResponseAt, SLATypeFirstResponse); err != nil {
			return err
		}
	}

	// If resolution is not breached and not met, check the deadine and set them.
	if !sla.ResolutionBreachedAt.Valid && !sla.ResolutionMetAt.Valid {
		m.lo.Debug("checking deadline", "deadline", sla.ResolutionDeadlineAt, "met_at", sla.ConversationResolvedAt.Time, "sla_type", SLATypeResolution)
		if err := checkDeadline(sla.ResolutionDeadlineAt, sla.ConversationResolvedAt, SLATypeResolution); err != nil {
			return err
		}
	}

	// Update the conversation next SLA deadline.
	if _, err := m.q.SetNextSLADeadline.Exec(sla.ConversationID); err != nil {
		return fmt.Errorf("setting conversation next SLA deadline: %w", err)
	}

	// Update status of applied SLA.
	if _, err := m.q.UpdateSLAStatus.Exec(sla.ID); err != nil {
		return fmt.Errorf("updating applied SLA status: %w", err)
	}

	return nil
}
