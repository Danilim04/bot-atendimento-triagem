package engine

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/store"
)

const csatPrompt = "Seu atendimento foi finalizado. Por favor, avalie de 1 a 5 " +
	"como foi (1 = muito ruim, 5 = excelente). Basta responder com o número."

var csatOptions = []chatwoot.SelectOption{
	{Title: "1 - Muito ruim", Value: "1"},
	{Title: "2 - Ruim", Value: "2"},
	{Title: "3 - Regular", Value: "3"},
	{Title: "4 - Bom", Value: "4"},
	{Title: "5 - Excelente", Value: "5"},
}

// startCSAT assume a conversa ao detectar a etiqueta de CSAT: envia o pedido de
// nota e registra o prazo de 5 minutos.
func (e *Engine) startCSAT(ctx context.Context, conv *chatwoot.Conversation) {
	// Serializa o processamento desta conversa (ver lockConv): evita reabrir o CSAT
	// em webhooks concorrentes.
	defer e.lockConv(conv.ID)()

	st := e.getState(ctx, conv.ID)
	// Idempotência: um único ciclo de CSAT por etiqueta. Webhooks concorrentes que
	// ainda carregam a etiqueta fila-csat — em especial o conversation_updated
	// disparado pela própria gravação do csat_rating em handleCSATAnswer, que pode
	// ser processado por outro worker DEPOIS de o estado virar csat_done — não devem
	// reabrir o pedido de nota já enviado/respondido/expirado.
	if st != nil {
		switch st.State {
		case store.StateCSATPending, store.StateCSATDone, store.StateClosedTimeout:
			e.log.Info("csat já iniciado/concluído; gatilho duplicado ignorado",
				"conversation_id", conv.ID, "state", st.State)
			return
		}
	}
	accountID := resolveAccountID(conv)
	e.saveState(ctx, conv.ID, accountID, store.StateCSATPending, "")

	if err := e.cw.SendInputSelect(ctx, conv.ID, csatPrompt, csatOptions); err != nil {
		e.log.Error("csat prompt", "conversation_id", conv.ID, "err", err)
	}

	deadline := time.Now().Add(e.cfg.CSATTimeout)
	if err := e.store.CreateDeadline(ctx, conv.ID, accountID, deadline); err != nil {
		e.log.Error("csat create deadline", "conversation_id", conv.ID, "err", err)
	}
	e.log.Info("csat iniciado", "conversation_id", conv.ID, "deadline", deadline)
}

// handleCSATAnswer processa a resposta do cliente dentro da janela de tempo.
func (e *Engine) handleCSATAnswer(ctx context.Context, conv *chatwoot.Conversation, content string) {
	// Serializa o processamento desta conversa (ver lockConv).
	defer e.lockConv(conv.ID)()

	st := e.getState(ctx, conv.ID)
	if st == nil || st.State != store.StateCSATPending {
		return // não estamos aguardando nota nesta conversa
	}

	rating, ok := parseRating(content)
	if !ok {
		_ = e.cw.SendMessage(ctx, conv.ID, "Por favor, responda apenas com um número de 1 a 5.", false)
		return // mantém o prazo original
	}

	accountID := resolveAccountID(conv)

	// Registra o CSAT: custom attribute (consultável) + nota privada (visível ao time).
	if err := e.cw.SetCustomAttributes(ctx, conv.ID, map[string]any{e.cfg.CSATAttribute: rating}); err != nil {
		e.log.Error("csat set attribute", "conversation_id", conv.ID, "err", err)
	}
	if err := e.cw.SendMessage(ctx, conv.ID, fmt.Sprintf("CSAT registrado: nota %d/5.", rating), true); err != nil {
		e.log.Error("csat private note", "conversation_id", conv.ID, "err", err)
	}
	if err := e.cw.SendMessage(ctx, conv.ID, "Obrigado pela sua avaliação! 🙏", false); err != nil {
		e.log.Error("csat thanks", "conversation_id", conv.ID, "err", err)
	}

	// Remove a etiqueta de CSAT e resolve a conversa.
	newLabels := chatwoot.ReplaceLabel(conv.Labels, e.cfg.LabelCSAT, "")
	if err := e.cw.SetLabels(ctx, conv.ID, newLabels); err != nil {
		e.log.Error("csat remove label", "conversation_id", conv.ID, "err", err)
	}
	if err := e.store.MarkDeadlineAnswered(ctx, conv.ID); err != nil {
		e.log.Error("csat mark answered", "conversation_id", conv.ID, "err", err)
	}
	if err := e.cw.ToggleStatus(ctx, conv.ID, "resolved"); err != nil {
		e.log.Error("csat resolve", "conversation_id", conv.ID, "err", err)
	}

	e.saveState(ctx, conv.ID, accountID, store.StateCSATDone, "")
	e.log.Info("csat concluído", "conversation_id", conv.ID, "rating", rating)
}

// parseRating extrai uma nota válida (1–5) de um texto livre ou da seleção de um
// input_select. Exige um número isolado de 1 a 5: tokens como "10" são rejeitados.
func parseRating(content string) (int, bool) {
	tokens := strings.FieldsFunc(content, func(r rune) bool { return !unicode.IsDigit(r) })
	for _, tok := range tokens {
		if len(tok) == 1 && tok[0] >= '1' && tok[0] <= '5' {
			return int(tok[0] - '0'), true
		}
	}
	return 0, false
}
