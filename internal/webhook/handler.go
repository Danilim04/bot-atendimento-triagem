// Package webhook recebe os eventos do Chatwoot, valida assinatura, deduplica e
// despacha o processamento para um pool de workers.
package webhook

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/store"
)

// Engine é a interface do motor de triagem consumida pela camada HTTP.
type Engine interface {
	HandleMessageCreated(ctx context.Context, msg *chatwoot.MessageCreated)
	HandleConversationUpdated(ctx context.Context, ev *chatwoot.ConversationUpdated)
}

// Server recebe webhooks e os enfileira para processamento assíncrono.
type Server struct {
	secret  string
	store   store.Store
	engine  Engine
	log     *slog.Logger
	jobs    chan job
	workers int
}

type job struct {
	event string
	body  []byte
}

const maxBodyBytes = 2 << 20 // 2 MiB

// NewServer cria o servidor de webhooks.
func NewServer(secret string, st store.Store, eng Engine, log *slog.Logger, workers, queueSize int) *Server {
	if workers <= 0 {
		workers = 4
	}
	if queueSize <= 0 {
		queueSize = 256
	}
	return &Server{
		secret:  secret,
		store:   st,
		engine:  eng,
		log:     log,
		jobs:    make(chan job, queueSize),
		workers: workers,
	}
}

// ServeHTTP valida e enfileira o evento, respondendo 200 rapidamente.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}

	timestamp := r.Header.Get("X-Chatwoot-Timestamp")
	signature := r.Header.Get("X-Chatwoot-Signature")
	if !chatwoot.VerifySignature(s.secret, timestamp, body, signature) {
		s.log.Warn("assinatura inválida", "remote", r.RemoteAddr)
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var env chatwoot.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	delivery := r.Header.Get("X-Chatwoot-Delivery")
	s.log.Info("webhook recebido", "event", env.Event, "delivery", delivery, "bytes", len(body))

	// Deduplicação por delivery id (o Chatwoot pode reentregar/duplicar eventos).
	if delivery != "" {
		fresh, err := s.store.MarkProcessed(r.Context(), delivery)
		if err != nil {
			s.log.Error("dedup", "delivery", delivery, "err", err)
		} else if !fresh {
			s.log.Info("evento duplicado ignorado (não enfileirado)", "event", env.Event, "delivery", delivery)
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	select {
	case s.jobs <- job{event: env.Event, body: body}:
		s.log.Info("evento enfileirado", "event", env.Event, "delivery", delivery, "fila_pendente", len(s.jobs))
	default:
		s.log.Error("fila de jobs cheia, evento descartado", "event", env.Event, "delivery", delivery, "fila_pendente", len(s.jobs))
	}
	w.WriteHeader(http.StatusOK)
}
