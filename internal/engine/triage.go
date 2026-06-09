package engine

import (
	"context"
	"strings"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/llm"
	"bot-atendimento-promosystem/internal/store"
)

// handoffFallback é usado quando o modelo decide rotear mas não fornece uma frase
// de encaminhamento própria.
const handoffFallback = "Obrigado! Já encaminhei o seu atendimento para o setor responsável. " +
	"Em instantes alguém vai continuar com você. 🙂"

// askFallback é usado quando o modelo pede para responder mas não fornece texto.
const askFallback = "Pode me dar mais detalhes sobre a sua necessidade?"

// maxReplyLen limita o tamanho da mensagem enviada ao cliente. Defesa em
// profundidade contra manipulação do modelo (ex.: prompt injection induzindo uma
// resposta enorme); uma fala normal de triagem cabe com folga nesse limite.
const maxReplyLen = 800

// clampReply normaliza a mensagem do modelo antes de enviá-la ao cliente: remove
// espaços nas bordas e trunca ao limite cortando em runas (não em bytes), para não
// quebrar caracteres multibyte.
func clampReply(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxReplyLen {
		return s
	}
	return strings.TrimSpace(string(r[:maxReplyLen])) + "…"
}

// handleTriage conduz a triagem de uma conversa. customer é o conteúdo da mensagem
// do cliente; nil indica uma entrada proativa (a etiqueta do bot foi aplicada antes
// de qualquer mensagem), caso em que o modelo inicia cumprimentando.
//
// Toda decisão conversacional (saudar, perguntar, rotear) é do modelo — o código só
// executa a decisão e mantém os guarda-corpos (setor válido, fila padrão, disjuntor
// de turnos e dedup de saudação via estado).
func (e *Engine) handleTriage(ctx context.Context, conv *chatwoot.Conversation, customer *string) {
	// Serializa o processamento desta conversa para fechar a janela read-check-act
	// do estado (saudação/triagem duplicadas em webhooks concorrentes; ver lockConv).
	defer e.lockConv(conv.ID)()

	st := e.getState(ctx, conv.ID)
	if st != nil && st.State == store.StateRouted {
		e.log.Info("triagem ignorada: conversa já roteada", "conversation_id", conv.ID)
		return
	}

	td := triageData{}
	if st != nil {
		td = loadTriage(st.Data)
	}

	if customer != nil {
		td.Turns = append(td.Turns, llm.Turn{Role: "customer", Content: *customer})
	} else if len(td.Turns) > 0 || (st != nil && st.State == store.StateTriage) {
		// Entrada proativa, mas a conversa já foi saudada/iniciada: nada a fazer.
		e.log.Info("entrada proativa ignorada: triagem já iniciada", "conversation_id", conv.ID)
		return
	}

	e.log.Info("triagem: consultando modelo",
		"conversation_id", conv.ID, "turnos", len(td.Turns), "proativa", customer == nil)

	intent, err := e.llm.Classify(ctx, e.systemPrompt, td.Turns)
	if err != nil {
		e.log.Error("llm classify", "conversation_id", conv.ID, "err", err)
		// Só pedimos para reformular quando há uma mensagem do cliente a responder.
		if customer != nil {
			_ = e.cw.SendMessage(ctx, conv.ID, "Desculpe, não consegui entender. Pode descrever novamente a sua necessidade?", false)
		}
		e.saveState(ctx, conv.ID, resolveAccountID(conv), store.StateTriage, td.encode())
		return
	}

	e.log.Info("triagem: decisão do modelo",
		"conversation_id", conv.ID,
		"action", intent.Action,
		"sector", intent.Sector,
		"reason", intent.Reason,
		"turnos", len(td.Turns),
	)

	// Guarda-corpo: encaminha à fila padrão após muitos turnos, evitando loops.
	if intent.Action != "route" && len(td.Turns) >= maxTriageTurns {
		e.log.Warn("triagem: máx. de turnos atingido, roteando para fila padrão", "conversation_id", conv.ID)
		e.route(ctx, conv, e.cfg.DefaultSectorLabel, handoffFallback)
		return
	}

	if intent.Action == "route" {
		label, known := e.cfg.SectorLabel(intent.Sector)
		if !known {
			e.log.Info("setor desconhecido do modelo, usando fila padrão", "conversation_id", conv.ID, "sector", intent.Sector)
		}
		msg := clampReply(intent.Reply)
		if msg == "" {
			msg = handoffFallback
		}
		e.route(ctx, conv, label, msg)
		return
	}

	// action = reply (inclui a saudação inicial).
	reply := clampReply(intent.Reply)
	if reply == "" {
		reply = askFallback
	}
	if err := e.cw.SendMessage(ctx, conv.ID, reply, false); err != nil {
		e.log.Error("triagem: envio de resposta", "conversation_id", conv.ID, "err", err)
	} else {
		e.log.Info("triagem: resposta enviada ao cliente", "conversation_id", conv.ID, "reply", reply)
	}
	td.Turns = append(td.Turns, llm.Turn{Role: "bot", Content: reply})
	e.saveState(ctx, conv.ID, resolveAccountID(conv), store.StateTriage, td.encode())
}

// route troca a etiqueta do bot pela etiqueta da fila de destino, avisa o cliente
// (message, se não vazia) e marca a conversa como roteada.
func (e *Engine) route(ctx context.Context, conv *chatwoot.Conversation, sectorLabel, message string) {
	newLabels := chatwoot.ReplaceLabel(conv.Labels, e.cfg.LabelBot, sectorLabel)
	if err := e.cw.SetLabels(ctx, conv.ID, newLabels); err != nil {
		e.log.Error("route set labels", "conversation_id", conv.ID, "err", err)
		return // não muda o estado se o roteamento falhou
	}
	if message != "" {
		if err := e.cw.SendMessage(ctx, conv.ID, message, false); err != nil {
			e.log.Error("route handoff message", "conversation_id", conv.ID, "err", err)
		}
	}
	e.saveState(ctx, conv.ID, resolveAccountID(conv), store.StateRouted, "")
	e.log.Info("conversa roteada", "conversation_id", conv.ID, "label", sectorLabel)
}
