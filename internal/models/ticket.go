package models

import "time"

type Ticket struct {
	ID               int       `json:"id"`
	UserEmail        string    `json:"user_email"`
	Channel          string    `json:"channel"`
	Subject          string    `json:"subject,omitempty"`
	CallbackURL      string    `json:"callback_url,omitempty"`
	RawText          string    `json:"raw_text"`
	Department       string    `json:"department"`
	Priority         string    `json:"priority"`
	ExtractedSummary *string   `json:"extracted_summary,omitempty"`
	RAGAnswer        *string   `json:"rag_answer,omitempty"`
	OperatorReply    *string   `json:"operator_reply"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

type CreateTicketRequest struct {
	UserEmail string `json:"user_email"`
	RawText   string `json:"raw_text"`
}

type IngestRequest struct {
	Channel             string `json:"channel"`
	Sender              string `json:"sender"`
	Subject             string `json:"subject"`
	Message             string `json:"message"`
	CallbackURL         string `json:"callback_url"`
	RequireConfirmation bool   `json:"require_confirmation"`
}

type FeedbackRequest struct {
	Resolved bool `json:"resolved"`
}

type UpdateTicketRequest struct {
	Status        *string `json:"status"`
	Department    *string `json:"department"`
	Priority      *string `json:"priority"`
	OperatorReply *string `json:"operator_reply"`
}
