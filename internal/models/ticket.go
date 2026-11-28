package models

import (
	"flowops-ai/internal/intake"
	"time"
)

type Ticket struct {
	FirstResponseDueAt    *time.Time      `json:"first_response_due_at,omitempty"`
	ResolutionDueAt       *time.Time      `json:"resolution_due_at,omitempty"`
	DisplayStatus         string          `json:"display_status"`
	RequesterEmail        string          `json:"requester_email"`
	ClosureReason         string          `json:"closure_reason"`
	UnreadEmployeeMessage bool            `json:"unread_employee_message"`
	Messages              []TicketMessage `json:"messages,omitempty"`
	DeliveryStatus        string          `json:"delivery_status"`
	ID                    int             `json:"id"`
	UserEmail             string          `json:"user_email"`
	Channel               string          `json:"channel"`
	Subject               string          `json:"subject,omitempty"`
	CallbackURL           string          `json:"callback_url,omitempty"`
	RawText               string          `json:"raw_text"`
	Department            string          `json:"department"`
	Priority              string          `json:"priority"`
	ExtractedSummary      *string         `json:"extracted_summary,omitempty"`
	RAGAnswer             *string         `json:"rag_answer,omitempty"`
	OperatorReply         *string         `json:"operator_reply"`
	PIIMaskedCount        int             `json:"pii_masked_count"`
	PIIMaskedTypes        []string        `json:"pii_masked_types"`
	Status                string          `json:"status"`
	CreatedAt             time.Time       `json:"created_at"`
}

type TicketMessage struct {
	ID            int64     `json:"id"`
	Role          string    `json:"role"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
	Rating        int       `json:"rating"`
	RatingComment string    `json:"rating_comment,omitempty"`
}

type TicketUpload struct {
	Filename string `json:"filename"`
	Content  []byte `json:"content"`
}
type CreateTicketRequest struct {
	AssetID      string                  `json:"asset_id,omitempty"`
	FormID       string                  `json:"form_id,omitempty"`
	Attachments  map[string]TicketUpload `json:"attachments,omitempty"`
	PriorityID   string                  `json:"priority_id,omitempty"`
	Tags         []string                `json:"tags,omitempty"`
	Fields       map[string]any          `json:"fields,omitempty"`
	FormRevision int64                   `json:"form_revision,omitempty"`
	Category     string                  `json:"category,omitempty"`
	TicketType   string                  `json:"ticket_type,omitempty"`
	Intake       *intake.Details         `json:"intake,omitempty"`
	UserEmail    string                  `json:"user_email"`
	RawText      string                  `json:"raw_text"`
}

type IngestRequest struct {
	BotMessageKeys      []string        `json:"-"`
	Intake              *intake.Details `json:"intake,omitempty"`
	IdempotencyKey      string          `json:"idempotency_key,omitempty"`
	Channel             string          `json:"channel"`
	Sender              string          `json:"sender"`
	Subject             string          `json:"subject"`
	Message             string          `json:"message"`
	CallbackURL         string          `json:"callback_url"`
	RequireConfirmation bool            `json:"require_confirmation"`
}

type FeedbackRequest struct {
	Resolved bool `json:"resolved"`
}

type UpdateTicketRequest struct {
	Category       *string   `json:"category"`
	TicketType     *string   `json:"ticket_type"`
	PriorityID     *string   `json:"priority_id"`
	Tags           *[]string `json:"tags"`
	WorkflowStatus *string   `json:"workflow_status"`
	Assignee       *string   `json:"assignee_id"`
	ClosureReason  *string   `json:"closure_reason"`
	Status         *string   `json:"status"`
	Department     *string   `json:"department"`
	Priority       *string   `json:"priority"`
	OperatorReply  *string   `json:"operator_reply"`
}
