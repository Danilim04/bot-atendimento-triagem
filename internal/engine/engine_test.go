package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
	"bot-atendimento-promosystem/internal/config"
	"bot-atendimento-promosystem/internal/llm"
	"bot-atendimento-promosystem/internal/store"
)

// --- fakes ---

type fakeClassifier struct {
	intent llm.Intent
	err    error
}

func (f fakeClassifier) Classify(_ context.Context, _ string, _ []llm.Turn) (llm.Intent, error) {
	return f.intent, f.err
}

type recordedReq struct {
	method string
	path   string
	body   map[string]any
}

type fakeChatwoot struct {
	mu   sync.Mutex
	reqs []recordedReq
	srv  *httptest.Server
}

func newFakeChatwoot() *fakeChatwoot {
	f := &fakeChatwoot{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		f.mu.Lock()
		f.reqs = append(f.reqs, recordedReq{method: r.Method, path: r.URL.Path, body: body})
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"payload":[]}`))
	}))
	return f
}

func (f *fakeChatwoot) find(t *testing.T, method, suffix string) recordedReq {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.reqs {
		if r.method == method && hasSuffix(r.path, suffix) {
			return r
		}
	}
	t.Fatalf("requisição %s ...%s não encontrada. recebidas: %+v", method, suffix, f.reqs)
	return recordedReq{}
}

func (f *fakeChatwoot) findAll(method, suffix string) []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedReq
	for _, r := range f.reqs {
		if r.method == method && hasSuffix(r.path, suffix) {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeChatwoot) assertNo(t *testing.T, method, suffix string) {
	t.Helper()
	if got := f.findAll(method, suffix); len(got) > 0 {
		t.Errorf("não esperava requisição %s ...%s, recebidas: %+v", method, suffix, got)
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

func newTestEngine(t *testing.T, cw *fakeChatwoot, intent llm.Intent) *Engine {
	return newTestEngineWith(t, cw, fakeClassifier{intent: intent})
}

func newTestEngineWith(t *testing.T, cw *fakeChatwoot, classifier llm.Classifier) *Engine {
	t.Helper()
	cfg := &config.Config{
		ChatwootBaseURL:    cw.srv.URL,
		ChatwootAccountID:  "1",
		ChatwootAPIToken:   "token",
		LabelBot:           "fila-bot",
		LabelCSAT:          "fila-csat",
		DefaultSectorLabel: "fila-humano",
		Sectors:            map[string]string{"financeiro": "fila-financeiro"},
		CSATTimeout:        5 * time.Minute,
		CSATAttribute:      "csat_rating",
	}
	st, err := store.NewSQLite(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	client := chatwoot.NewClient(cfg.ChatwootBaseURL, cfg.ChatwootAccountID, cfg.ChatwootAPIToken)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	eng, err := New(cfg, st, client, classifier, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// countingClassifier conta as chamadas e atrasa a resposta, alargando a janela de
// corrida entre eventos concorrentes para tornar o teste determinístico.
type countingClassifier struct {
	intent llm.Intent
	delay  time.Duration
	calls  atomic.Int32
}

func (c *countingClassifier) Classify(_ context.Context, _ string, _ []llm.Turn) (llm.Intent, error) {
	c.calls.Add(1)
	time.Sleep(c.delay)
	return c.intent, nil
}

// --- tests ---

func TestParseRating(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"5", 5, true},
		{"  3 ", 3, true},
		{"nota 4 por favor", 4, true},
		{"5 - Excelente", 5, true},
		{"0", 0, false},
		{"6", 0, false},
		{"10", 0, false},
		{"obrigado", 0, false},
	}
	for _, c := range cases {
		got, ok := parseRating(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("parseRating(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestTriage_RoutesToSector(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	eng := newTestEngine(t, cw, llm.Intent{Action: "route", Sector: "financeiro"})

	msg := &chatwoot.MessageCreated{
		Event:       "message_created",
		MessageType: chatwoot.MessageIncoming,
		Content:     "quero pagar meu boleto",
		Sender:      chatwoot.Sender{Type: "contact"},
		Conversation: chatwoot.Conversation{
			ID: 567, AccountID: 1, Labels: []string{"fila-bot"},
		},
	}
	eng.HandleMessageCreated(context.Background(), msg)

	req := cw.find(t, http.MethodPost, "/conversations/567/labels")
	labels := toStrings(req.body["labels"])
	if chatwoot.HasLabel(labels, "fila-bot") {
		t.Errorf("fila-bot deveria ter sido removida: %v", labels)
	}
	if !chatwoot.HasLabel(labels, "fila-financeiro") {
		t.Errorf("fila-financeiro deveria ter sido adicionada: %v", labels)
	}
}

func TestTriage_UnknownSectorFallsBackToDefault(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	eng := newTestEngine(t, cw, llm.Intent{Action: "route", Sector: "setor-inexistente"})

	msg := &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "preciso de um setor que não existe",
		Conversation: chatwoot.Conversation{ID: 1, AccountID: 1, Labels: []string{"fila-bot"}},
	}
	eng.HandleMessageCreated(context.Background(), msg)

	req := cw.find(t, http.MethodPost, "/conversations/1/labels")
	if !chatwoot.HasLabel(toStrings(req.body["labels"]), "fila-humano") {
		t.Errorf("esperava fallback para fila-humano: %v", req.body["labels"])
	}
}

// TestTriage_ProactiveGreetingOnLabel cobre a entrada proativa: ao aplicar a
// etiqueta do bot (conversation_updated) sem mensagem prévia, o modelo cumprimenta —
// a saudação é apenas o primeiro `reply` emitido, sem caso especial no código.
func TestTriage_ProactiveGreetingOnLabel(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	const greet = "Olá! Sou o atendente virtual da Promosystem. Em que posso ajudar?"
	eng := newTestEngine(t, cw, llm.Intent{Action: "reply", Reply: greet})

	eng.HandleConversationUpdated(context.Background(), &chatwoot.ConversationUpdated{
		Conversation: chatwoot.Conversation{ID: 9, AccountID: 1, Labels: []string{"fila-bot"}},
	})

	msgs := cw.findAll(http.MethodPost, "/conversations/9/messages")
	if len(msgs) != 1 || msgs[0].body["content"] != greet {
		t.Fatalf("esperava exatamente 1 saudação proativa, got %+v", msgs)
	}
	cw.assertNo(t, http.MethodPost, "/conversations/9/labels")
}

// labelChange monta um changed_attributes com a mudança da lista de etiquetas,
// como o Chatwoot envia em conversation_updated.
func labelChange(previous, current []string) []map[string]chatwoot.ChangeValue {
	mustJSON := func(v []string) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	return []map[string]chatwoot.ChangeValue{
		{"labels": {CurrentValue: mustJSON(current), PreviousValue: mustJSON(previous)}},
	}
}

// TestTriage_ProactiveGreetingOnLabelAddition cobre o gatilho correto: quando a
// etiqueta do bot é RECÉM-ADICIONADA (changed_attributes mostra a transição), o bot
// saúda uma vez.
func TestTriage_ProactiveGreetingOnLabelAddition(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	const greet = "Olá! Sou o atendente virtual da Promosystem. Em que posso ajudar?"
	eng := newTestEngine(t, cw, llm.Intent{Action: "reply", Reply: greet})

	eng.HandleConversationUpdated(context.Background(), &chatwoot.ConversationUpdated{
		ChangedAttributes: labelChange(nil, []string{"fila-bot"}),
		Conversation:      chatwoot.Conversation{ID: 10, AccountID: 1, Labels: []string{"fila-bot"}},
	})

	if msgs := cw.findAll(http.MethodPost, "/conversations/10/messages"); len(msgs) != 1 || msgs[0].body["content"] != greet {
		t.Fatalf("esperava exatamente 1 saudação ao adicionar a etiqueta, got %+v", msgs)
	}
}

// TestTriage_NoGreetingWhenBotLabelAlreadyPresent cobre o bug relatado: mexer em algo
// na conversa (ex.: adicionar OUTRA etiqueta) enquanto a etiqueta do bot já estava
// aplicada gera conversation_updated, mas NÃO deve disparar uma nova saudação.
func TestTriage_NoGreetingWhenBotLabelAlreadyPresent(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	eng := newTestEngine(t, cw, llm.Intent{Action: "reply", Reply: "não deveria ser enviado"})

	// fila-bot já estava presente; o que mudou foi a adição de "vip".
	eng.HandleConversationUpdated(context.Background(), &chatwoot.ConversationUpdated{
		ChangedAttributes: labelChange([]string{"fila-bot"}, []string{"fila-bot", "vip"}),
		Conversation:      chatwoot.Conversation{ID: 11, AccountID: 1, Labels: []string{"fila-bot", "vip"}},
	})

	cw.assertNo(t, http.MethodPost, "/conversations/11/messages")
}

// TestTriage_FirstMessageGreetsWhenNoRoute cobre o bug original: a primeira mensagem
// do cliente chega antes do conversation_updated. O modelo responde (saudação) e,
// como ainda não há setor claro, a conversa NÃO é roteada.
func TestTriage_FirstMessageGreetsWhenNoRoute(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	const reply = "Olá! Em que posso ajudar você hoje?"
	eng := newTestEngine(t, cw, llm.Intent{Action: "reply", Reply: reply})

	eng.HandleMessageCreated(context.Background(), &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "oi tudo bem?",
		Conversation: chatwoot.Conversation{ID: 42, AccountID: 1, Labels: []string{"fila-bot"}},
	})

	msgs := cw.findAll(http.MethodPost, "/conversations/42/messages")
	if len(msgs) != 1 {
		t.Fatalf("esperava exatamente 1 mensagem (a resposta do modelo), got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].body["content"] != reply {
		t.Errorf("esperava a resposta do modelo, got %v", msgs[0].body["content"])
	}
	cw.assertNo(t, http.MethodPost, "/conversations/42/labels")
}

// TestTriage_FirstMessageWithNeedRoutes garante que uma primeira mensagem que já traz
// a necessidade roteia de imediato (apenas a frase de encaminhamento + troca de fila).
func TestTriage_FirstMessageWithNeedRoutes(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	const handoff = "Vou te transferir para o financeiro."
	eng := newTestEngine(t, cw, llm.Intent{Action: "route", Sector: "financeiro", Reply: handoff})

	eng.HandleMessageCreated(context.Background(), &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "quero pagar meu boleto",
		Conversation: chatwoot.Conversation{ID: 77, AccountID: 1, Labels: []string{"fila-bot"}},
	})

	if msgs := cw.findAll(http.MethodPost, "/conversations/77/messages"); len(msgs) != 1 || msgs[0].body["content"] != handoff {
		t.Errorf("esperava a frase de encaminhamento como única mensagem, got %+v", msgs)
	}
	req := cw.find(t, http.MethodPost, "/conversations/77/labels")
	labels := toStrings(req.body["labels"])
	if chatwoot.HasLabel(labels, "fila-bot") || !chatwoot.HasLabel(labels, "fila-financeiro") {
		t.Errorf("esperava troca de fila-bot por fila-financeiro: %v", labels)
	}
}

func TestCSAT_AnswerWithinWindow(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	eng := newTestEngine(t, cw, llm.Intent{})
	ctx := context.Background()

	conv := chatwoot.Conversation{ID: 800, AccountID: 1, Labels: []string{"fila-csat"}}

	// 1) Etiqueta fila-csat aplicada -> envia pedido de nota (input_select).
	eng.HandleConversationUpdated(ctx, &chatwoot.ConversationUpdated{Conversation: conv})
	sel := cw.find(t, http.MethodPost, "/conversations/800/messages")
	if sel.body["content_type"] != "input_select" {
		t.Errorf("esperava input_select, got %v", sel.body["content_type"])
	}

	// 2) Cliente responde "5".
	eng.HandleMessageCreated(ctx, &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "5",
		Conversation: conv,
	})

	attrs := cw.find(t, http.MethodPost, "/conversations/800/custom_attributes")
	ca, _ := attrs.body["custom_attributes"].(map[string]any)
	if ca == nil || ca["csat_rating"] == nil {
		t.Errorf("csat_rating não registrado: %+v", attrs.body)
	}
	status := cw.find(t, http.MethodPost, "/conversations/800/toggle_status")
	if status.body["status"] != "resolved" {
		t.Errorf("conversa deveria ser resolvida: %v", status.body["status"])
	}
	labels := cw.find(t, http.MethodPost, "/conversations/800/labels")
	if chatwoot.HasLabel(toStrings(labels.body["labels"]), "fila-csat") {
		t.Errorf("fila-csat deveria ter sido removida: %v", labels.body["labels"])
	}
}

// TestCSAT_LateLabelEventDoesNotRePrompt cobre o bug do CSAT reenviado: depois que a
// nota é registrada (estado csat_done), um conversation_updated atrasado que ainda
// carrega a etiqueta fila-csat — como o disparado pela própria gravação do csat_rating,
// processado por outro worker após a conclusão — NÃO deve reabrir o pedido de avaliação.
func TestCSAT_LateLabelEventDoesNotRePrompt(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	eng := newTestEngine(t, cw, llm.Intent{})
	ctx := context.Background()

	conv := chatwoot.Conversation{ID: 801, AccountID: 1, Labels: []string{"fila-csat"}}

	// Pedido de nota + resposta do cliente -> estado csat_done.
	eng.HandleConversationUpdated(ctx, &chatwoot.ConversationUpdated{Conversation: conv})
	eng.HandleMessageCreated(ctx, &chatwoot.MessageCreated{
		MessageType:  chatwoot.MessageIncoming,
		Sender:       chatwoot.Sender{Type: "contact"},
		Content:      "4",
		Conversation: conv,
	})
	before := len(cw.findAll(http.MethodPost, "/conversations/801/messages"))

	// Webhook atrasado AINDA com a etiqueta fila-csat (race da gravação do atributo).
	eng.HandleConversationUpdated(ctx, &chatwoot.ConversationUpdated{Conversation: conv})

	if after := len(cw.findAll(http.MethodPost, "/conversations/801/messages")); after != before {
		t.Errorf("não deveria reenviar o pedido de CSAT após concluído: %d mensagens antes, %d depois", before, after)
	}
}

// TestTriage_ConcurrentProactiveSendsSingleGreeting cobre o bug da saudação duplicada:
// o Chatwoot emite múltiplos conversation_updated quase simultâneos (todos com
// X-Chatwoot-Delivery vazio, então o dedup não os pega). Sem serialização por
// conversa, dois workers leem estado nil, ambos chamam o modelo e ambos enviam a
// saudação. O lock por conversa garante que o segundo veja "triagem já iniciada".
func TestTriage_ConcurrentProactiveSendsSingleGreeting(t *testing.T) {
	cw := newFakeChatwoot()
	defer cw.srv.Close()
	const greet = "Olá! Eu sou o Claudio, como posso ajudar você hoje?"
	clf := &countingClassifier{intent: llm.Intent{Action: "reply", Reply: greet}, delay: 40 * time.Millisecond}
	eng := newTestEngineWith(t, cw, clf)

	ev := &chatwoot.ConversationUpdated{
		Conversation: chatwoot.Conversation{ID: 86, AccountID: 1, Labels: []string{"fila-bot"}},
	}

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			eng.HandleConversationUpdated(context.Background(), ev)
		}()
	}
	wg.Wait()

	if msgs := cw.findAll(http.MethodPost, "/conversations/86/messages"); len(msgs) != 1 {
		t.Errorf("esperava exatamente 1 saudação para eventos concorrentes, got %d: %+v", len(msgs), msgs)
	}
	if n := clf.calls.Load(); n != 1 {
		t.Errorf("esperava 1 chamada ao modelo, got %d", n)
	}
}

func toStrings(v any) []string {
	arr, _ := v.([]any)
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
