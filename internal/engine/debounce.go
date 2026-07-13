package engine

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"bot-atendimento-promosystem/internal/chatwoot"
)

// debouncer agrupa as mensagens de entrada de uma mesma conversa dentro de uma
// janela de silêncio: cada nova mensagem reinicia o timer; quando ele expira sem
// novas mensagens, todas as acumuladas são entregues de uma vez ao flush como um
// único conteúdo (separadas por \n). Resolve o caso em que o cliente envia várias
// mensagens em sequência e o bot responderia só a primeira / cada uma isolada.
//
// É um buffer em memória: mensagens pendentes na janela são perdidas se o processo
// reiniciar — aceitável para o baixo volume do bot e consistente com o lock
// striping (também em memória) usado na engine.
type debouncer struct {
	mu      sync.Mutex
	pending map[int64]*pendingConv
	window  time.Duration
	flush   func(conv *chatwoot.Conversation, combined string, proactive bool)
	log     *slog.Logger
}

// pendingConv é o estado acumulado de uma conversa aguardando o fim da janela.
type pendingConv struct {
	timer    *time.Timer
	contents []string
	conv     *chatwoot.Conversation
	gen      int // geração: invalida disparos de timer superados por mensagens novas
}

// newDebouncer cria o debouncer. flush é chamado (em uma goroutine própria) quando
// a janela de uma conversa expira. O parâmetro proactive é true quando a janela
// encerrou sem nenhuma mensagem do cliente — ou seja, só houve o gatilho de
// saudação proativa (ver addProactive); nesse caso o flush deve saudar sem tratar
// conteúdo. Havendo mensagem do cliente, proactive é false e combined traz o texto.
func newDebouncer(window time.Duration, log *slog.Logger, flush func(conv *chatwoot.Conversation, combined string, proactive bool)) *debouncer {
	if log == nil {
		log = slog.Default()
	}
	return &debouncer{
		pending: make(map[int64]*pendingConv),
		window:  window,
		flush:   flush,
		log:     log,
	}
}

// add acumula uma mensagem da conversa e (re)arma o timer da janela.
func (d *debouncer) add(conv *chatwoot.Conversation, content string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	p, ok := d.pending[conv.ID]
	if !ok {
		p = &pendingConv{}
		d.pending[conv.ID] = p
	}
	p.contents = append(p.contents, content)
	p.conv = conv
	p.gen++
	gen := p.gen
	id := conv.ID

	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(d.window, func() { d.fire(id, gen) })

	d.log.Info("debounce: mensagem acumulada",
		"conversation_id", id, "acumuladas", len(p.contents), "janela", d.window)
}

// addProactive registra o gatilho de saudação proativa (etiqueta do bot aplicada)
// na MESMA janela das mensagens do cliente, sem acrescentar conteúdo. Assim os dois
// gatilhos de início de conversa se fundem: se a primeira mensagem do cliente chegar
// dentro da janela, o flush a trata como triagem (que já cumprimenta) e não há
// saudação separada; se a janela expirar sem mensagem, o flush faz a saudação
// proativa (combined vazio, proactive=true). Fecha a corrida entre conversation_updated
// e message_created que gerava saudação dupla numa conversa recém-aberta pelo cliente.
func (d *debouncer) addProactive(conv *chatwoot.Conversation) {
	d.mu.Lock()
	defer d.mu.Unlock()

	p, ok := d.pending[conv.ID]
	if !ok {
		p = &pendingConv{}
		d.pending[conv.ID] = p
	}
	p.conv = conv
	p.gen++
	gen := p.gen
	id := conv.ID

	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(d.window, func() { d.fire(id, gen) })

	d.log.Info("debounce: saudação proativa agendada na janela",
		"conversation_id", id, "acumuladas", len(p.contents), "janela", d.window)
}

// fire é chamado pelo timer ao fim da janela; entrega o conteúdo combinado ao flush
// caso não tenha sido superado por uma mensagem mais nova (gen).
func (d *debouncer) fire(id int64, gen int) {
	d.mu.Lock()
	p, ok := d.pending[id]
	if !ok || p.gen != gen {
		// Superado por uma mensagem mais recente (que rearmou o timer) ou já entregue.
		d.mu.Unlock()
		return
	}
	combined := strings.Join(p.contents, "\n")
	proactive := len(p.contents) == 0
	conv := p.conv
	delete(d.pending, id)
	d.mu.Unlock()

	d.log.Info("debounce: janela encerrada, processando",
		"conversation_id", id, "mensagens", len(p.contents), "proativa", proactive)
	d.flush(conv, combined, proactive)
}
