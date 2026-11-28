package models

import (
	"flowops-ai/internal/intake"
	"time"
)

type Ticket struct {
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

type CreateTicketRequest struct {
	Intake    *intake.Details `json:"intake,omitempty"`
	UserEmail string          `json:"user_email"`
	RawText   string          `json:"raw_text"`
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
	ClosureReason *string `json:"closure_reason"`
	Status        *string `json:"status"`
	Department    *string `json:"department"`
	Priority      *string `json:"priority"`
	OperatorReply *string `json:"operator_reply"`
}
