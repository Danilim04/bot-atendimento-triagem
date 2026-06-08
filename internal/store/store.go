// Package store define a persistência do estado das conversas, dos deadlines de
// CSAT e da deduplicação de webhooks.
package store

import (
	"context"
	"time"
)

// State representa o estado da máquina de estados de uma conversa.
type State string

const (
	StateIdle          State = "idle"
	StateTriage        State = "triage"
	StateRouted        State = "routed"
	StateCSATPending   State = "csat_pending"
	StateCSATDone      State = "csat_done"
	StateClosedTimeout State = "closed_timeout"
)

// ConversationState é o estado persistido de uma conversa. Data guarda um JSON
// livre (ex.: histórico de triagem).
type ConversationState struct {
	ConversationID int64
	AccountID      int64
	State          State
	Data           string
	UpdatedAt      time.Time
}

// Deadline é um prazo de resposta de CSAT.
type Deadline struct {
	ConversationID int64
	AccountID      int64
	DeadlineAt     time.Time
	Status         string // pending | answered | closing | done
}

// Store é a interface de persistência usada pelo resto da aplicação.
type Store interface {
	// GetState devolve o estado da conversa, ou nil se inexistente.
	GetState(ctx context.Context, convID int64) (*ConversationState, error)
	// SetState insere ou atualiza o estado da conversa.
	SetState(ctx context.Context, st *ConversationState) error

	// MarkProcessed registra um delivery_id; devolve true se for novo (não
	// duplicado).
	MarkProcessed(ctx context.Context, deliveryID string) (bool, error)

	// CreateDeadline registra/atualiza o prazo de CSAT de uma conversa.
	CreateDeadline(ctx context.Context, convID, accountID int64, at time.Time) error
	// MarkDeadlineAnswered marca o prazo como respondido (não será fechado pelo
	// reaper).
	MarkDeadlineAnswered(ctx context.Context, convID int64) error
	// ClaimExpiredDeadlines reivindica atomicamente prazos vencidos (e prazos em
	// "closing" remanescentes de crashes), retornando-os para fechamento.
	ClaimExpiredDeadlines(ctx context.Context, now time.Time, limit int) ([]Deadline, error)
	// CompleteDeadline marca o prazo como concluído após o fechamento.
	CompleteDeadline(ctx context.Context, convID int64) error

	Close() error
}
