// Package engine implementa a máquina de estados de triagem e CSAT, orquestrando
// store, cliente Chatwoot e classificador LLM.
package engine

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"text/template"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/config"
	"bot-atendimento-promosystem/internal/llm"
	"bot-atendimento-promosystem/internal/store"
)

//go:embed system_prompt.txt
var defaultPromptTemplate string

// defaultSectorKey é a chave usada quando nenhum setor específico se aplica.
const defaultSectorKey = "humano"

// maxTriageTurns limita o número de turnos do cliente antes de encaminhar à fila
// padrão, evitando loops infinitos de perguntas.
const maxTriageTurns = 8

// convLockStripes é o número de mutexes usados para serializar o processamento por
// conversa (lock striping). Limita o uso de memória vs. um mutex por conversa;
// colisões apenas serializam conversas distintas ocasionalmente, o que é inofensivo.
const convLockStripes = 64

// debounceFlushTimeout limita o tempo de processamento da triagem disparada pelo
// fim da janela de debounce (igual ao jobTimeout do worker pool). Necessário porque
// o timer dispara depois que o contexto do worker original já foi cancelado.
const debounceFlushTimeout = 60 * time.Second

// Engine reúne as dependências e o estado pré-computado (system prompt).
type Engine struct {
	cfg          *config.Config
	store        store.Store
	cw           *chatwoot.Client
	llm          llm.Classifier
	transcriber  llm.Transcriber // pode ser nil quando o STT não está configurado
	log          *slog.Logger
	systemPrompt string
	convLocks    [convLockStripes]sync.Mutex
	debouncer    *debouncer
}

// lockConv serializa o processamento de eventos de uma mesma conversa e devolve a
// função de liberação. Fecha a janela read-check-act do estado: o Chatwoot emite
// vários conversation_updated quase simultâneos para a mesma conversa, todos com
// X-Chatwoot-Delivery vazio (então o dedup por delivery-id não os pega); sem
// serialização, dois workers passariam pelos guards de estado (ex.: "triagem já
// iniciada") antes de qualquer um persistir, gerando saudações/CSAT duplicados.
func (e *Engine) lockConv(convID int64) func() {
	mu := &e.convLocks[uint64(convID)%convLockStripes]
	mu.Lock()
	return mu.Unlock
}

// triageData é o payload JSON persistido em ConversationState.Data durante a
// triagem.
type triageData struct {
	Turns []llm.Turn `json:"turns"`
}

// New constrói a engine, montando o system prompt com os setores configurados.
// transcriber pode ser nil para desabilitar a transcrição de áudio.
func New(cfg *config.Config, st store.Store, cw *chatwoot.Client, classifier llm.Classifier, transcriber llm.Transcriber, log *slog.Logger) (*Engine, error) {
	prompt, err := buildSystemPrompt(cfg)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		cfg:          cfg,
		store:        st,
		cw:           cw,
		llm:          classifier,
		transcriber:  transcriber,
		log:          log,
		systemPrompt: prompt,
	}
	// O debouncer entrega a triagem agrupada já fora do worker original, então cria
	// um contexto próprio (o contexto do worker já terá sido cancelado).
	e.debouncer = newDebouncer(cfg.DebounceWindow, log, func(conv *chatwoot.Conversation, combined string) {
		ctx, cancel := context.WithTimeout(context.Background(), debounceFlushTimeout)
		defer cancel()
		c := combined
		e.handleTriage(ctx, conv, &c)
	})
	return e, nil
}

// HandleMessageCreated trata mensagens de entrada do cliente.
func (e *Engine) HandleMessageCreated(ctx context.Context, msg *chatwoot.MessageCreated) {
	convID := msg.Conversation.ID
	// Filtro de borda: só mensagens de entrada, do contato, não privadas.
	if !msg.MessageType.IsIncoming() || !msg.Sender.IsContact() || msg.Private {
		e.log.Info("message_created descartado pelo filtro de borda (não processado)",
			"conversation_id", convID,
			"incoming", msg.MessageType.IsIncoming(),
			"sender_contato", msg.Sender.IsContact(),
			"private", msg.Private,
		)
		return
	}

	// O lock por conversa é adquirido dentro de cada folha (handleTriage,
	// handleCSATAnswer, startCSAT), e não aqui no topo: a triagem pode rodar mais
	// tarde, fora deste worker, quando entregue pelo debouncer (ver newDebouncer).

	labels := msg.Conversation.Labels
	switch {
	case chatwoot.HasLabel(labels, e.cfg.LabelCSAT):
		e.log.Info("message_created → fluxo CSAT", "conversation_id", convID, "labels", labels)
		e.handleCSATAnswer(ctx, &msg.Conversation, msg.Content)
	case chatwoot.HasLabel(labels, e.cfg.LabelBot):
		e.log.Info("message_created → fluxo triagem", "conversation_id", convID, "labels", labels, "content", msg.Content)
		// Resolve o conteúdo (texto ou transcrição de áudio) antes de qualquer
		// estado — I/O stateless, sem segurar o lock da conversa.
		content, ok := e.resolveIncomingContent(ctx, msg)
		if !ok {
			return
		}
		if e.cfg.DebounceWindow <= 0 {
			// Debounce desligado: processa imediatamente (comportamento legado).
			e.handleTriage(ctx, &msg.Conversation, &content)
			return
		}
		// Agrupa mensagens em rajada; a triagem roda ao fim da janela de silêncio.
		e.debouncer.add(&msg.Conversation, content)
	default:
		e.log.Info("message_created ignorado: conversa sem etiqueta de controle",
			"conversation_id", convID,
			"labels", labels,
			"label_bot", e.cfg.LabelBot,
			"label_csat", e.cfg.LabelCSAT,
		)
	}
}

// HandleConversationUpdated trata mudanças de etiqueta (gatilhos de triagem e CSAT).
func (e *Engine) HandleConversationUpdated(ctx context.Context, ev *chatwoot.ConversationUpdated) {
	conv := &ev.Conversation

	// O lock por conversa é adquirido dentro de cada folha (handleTriage, startCSAT);
	// os guards spuriousLabelEntry abaixo são stateless e dispensam o lock.

	labels := conv.Labels
	switch {
	case chatwoot.HasLabel(labels, e.cfg.LabelCSAT):
		if spuriousLabelEntry(ev, e.cfg.LabelCSAT) {
			e.log.Info("conversation_updated ignorado: etiqueta CSAT já estava presente (mudança não é a aplicação da etiqueta)",
				"conversation_id", conv.ID, "labels", labels)
			return
		}
		e.log.Info("conversation_updated → iniciar CSAT", "conversation_id", conv.ID, "labels", labels)
		e.startCSAT(ctx, conv)
	case chatwoot.HasLabel(labels, e.cfg.LabelBot):
		if spuriousLabelEntry(ev, e.cfg.LabelBot) {
			e.log.Info("conversation_updated ignorado: etiqueta do bot já estava presente (mudança não é a aplicação da etiqueta)",
				"conversation_id", conv.ID, "labels", labels)
			return
		}
		e.log.Info("conversation_updated → entrada de triagem (saudação proativa)", "conversation_id", conv.ID, "labels", labels)
		e.handleTriage(ctx, conv, nil)
	default:
		e.log.Info("conversation_updated ignorado: conversa sem etiqueta de controle",
			"conversation_id", conv.ID,
			"labels", labels,
			"label_bot", e.cfg.LabelBot,
			"label_csat", e.cfg.LabelCSAT,
		)
	}
}

// spuriousLabelEntry informa que esta atualização NÃO é o gatilho de entrada da
// etiqueta target: as etiquetas mudaram, mas target já estava presente antes (ou
// apenas outra etiqueta mudou). É o caso típico de "mudei algo na conversa" — uma
// nova etiqueta, o responsável via etiqueta, etc. — enquanto a etiqueta de controle
// permanecia aplicada, que não deve reabrir saudação/CSAT.
//
// Updates sem mudança de etiquetas (designação, status, atributos) NÃO são tratados
// como espúrios aqui: o guard de estado a jusante já garante a idempotência e ainda
// permite recuperar o gatilho caso o evento de aplicação da etiqueta tenha se perdido.
func spuriousLabelEntry(ev *chatwoot.ConversationUpdated, target string) bool {
	return ev.LabelsChanged() && !ev.LabelJustAdded(target)
}

// --- helpers de estado ---

func (e *Engine) getState(ctx context.Context, convID int64) *store.ConversationState {
	st, err := e.store.GetState(ctx, convID)
	if err != nil {
		e.log.Error("getState", "conversation_id", convID, "err", err)
		return nil
	}
	return st
}

func (e *Engine) saveState(ctx context.Context, convID, accountID int64, state store.State, data string) {
	st := &store.ConversationState{
		ConversationID: convID,
		AccountID:      accountID,
		State:          state,
		Data:           data,
	}
	if err := e.store.SetState(ctx, st); err != nil {
		e.log.Error("saveState", "conversation_id", convID, "err", err)
	}
}

func loadTriage(data string) triageData {
	var td triageData
	if data != "" {
		_ = json.Unmarshal([]byte(data), &td)
	}
	return td
}

func (td triageData) encode() string {
	b, _ := json.Marshal(td)
	return string(b)
}

// resolveAccountID prioriza o account_id da conversa, com fallback configurado.
func resolveAccountID(conv *chatwoot.Conversation) int64 {
	return conv.AccountID
}

func buildSystemPrompt(cfg *config.Config) (string, error) {
	tmplStr := defaultPromptTemplate
	if cfg.PromptPath != "" {
		b, err := os.ReadFile(cfg.PromptPath)
		if err != nil {
			return "", err
		}
		tmplStr = string(b)
	}
	t, err := template.New("system_prompt").Parse(tmplStr)
	if err != nil {
		return "", err
	}
	data := struct {
		Sectors       []string
		DefaultSector string
	}{
		Sectors:       cfg.SectorNames(),
		DefaultSector: defaultSectorKey,
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
