// Package scheduler implementa o reaper que fecha conversas de CSAT cujo prazo
// de resposta expirou.
package scheduler

import (
	"context"
	"log/slog"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/store"
)

// Reaper varre periodicamente os prazos de CSAT vencidos e resolve as conversas.
type Reaper struct {
	store    store.Store
	cw       *chatwoot.Client
	interval time.Duration
	log      *slog.Logger
}

// New cria o reaper.
func New(st store.Store, cw *chatwoot.Client, interval time.Duration, log *slog.Logger) *Reaper {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Reaper{store: st, cw: cw, interval: interval, log: log}
}

// Run executa a reconciliação inicial (prazos vencidos durante downtime) e então
// entra no laço periódico até ctx ser cancelado.
func (r *Reaper) Run(ctx context.Context) {
	r.tick(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Reaper) tick(ctx context.Context) {
	deadlines, err := r.store.ClaimExpiredDeadlines(ctx, time.Now(), 100)
	if err != nil {
		r.log.Error("reaper claim", "err", err)
		return
	}
	for _, d := range deadlines {
		if err := r.cw.ToggleStatus(ctx, d.ConversationID, "resolved"); err != nil {
			r.log.Error("reaper resolve", "conversation_id", d.ConversationID, "err", err)
			continue // mantém em 'closing' para nova tentativa no próximo tick
		}
		if err := r.store.CompleteDeadline(ctx, d.ConversationID); err != nil {
			r.log.Error("reaper complete", "conversation_id", d.ConversationID, "err", err)
		}
		_ = r.store.SetState(ctx, &store.ConversationState{
			ConversationID: d.ConversationID,
			AccountID:      d.AccountID,
			State:          store.StateClosedTimeout,
		})
		r.log.Info("csat encerrado por timeout", "conversation_id", d.ConversationID)
	}
}
