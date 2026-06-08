package webhook

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
)

// jobTimeout limita o tempo de processamento de cada evento (inclui chamadas ao
// LLM e à API do Chatwoot).
const jobTimeout = 60 * time.Second

// Start sobe o pool de workers e bloqueia até ctx ser cancelado, drenando a fila.
func (s *Server) Start(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < s.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx)
		}()
	}
	wg.Wait()
}

func (s *Server) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.jobs:
			s.process(ctx, j)
		}
	}
}

func (s *Server) process(parent context.Context, j job) {
	ctx, cancel := context.WithTimeout(parent, jobTimeout)
	defer cancel()

	start := time.Now()
	s.log.Info("job iniciado", "event", j.event)

	defer func() {
		if rec := recover(); rec != nil {
			s.log.Error("panic no processamento do webhook", "event", j.event, "recover", rec)
		}
		s.log.Info("job finalizado", "event", j.event, "duracao_ms", time.Since(start).Milliseconds())
	}()

	switch j.event {
	case "message_created":
		var msg chatwoot.MessageCreated
		if err := json.Unmarshal(j.body, &msg); err != nil {
			s.log.Error("unmarshal message_created", "err", err)
			return
		}
		s.engine.HandleMessageCreated(ctx, &msg)

	case "conversation_updated":
		var ev chatwoot.ConversationUpdated
		if err := json.Unmarshal(j.body, &ev); err != nil {
			s.log.Error("unmarshal conversation_updated", "err", err)
			return
		}
		s.engine.HandleConversationUpdated(ctx, &ev)

	default:
		s.log.Debug("evento sem handler, ignorado", "event", j.event)
	}
}
