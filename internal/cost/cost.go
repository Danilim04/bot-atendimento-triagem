// Package cost agrega o custo (em USD) das chamadas a provedores externos (LLM e
// STT) durante a execução e registra o total acumulado no log. O total é mantido
// em memória e zera a cada reinício do processo; o valor oficial de cobrança é o do
// painel do provedor (ex.: OpenRouter). Serve para acompanhar o gasto em tempo real.
package cost

import (
	"log/slog"
	"sync"
)

// Tracker soma o custo das chamadas por categoria e loga o acumulado a cada adição.
// É seguro para uso concorrente. Um *Tracker nil é um no-op, o que permite desligar
// a métrica sem espalhar checagens nos chamadores.
type Tracker struct {
	log    *slog.Logger
	mu     sync.Mutex
	total  float64
	byKind map[string]float64
	calls  int
}

// New cria um Tracker que loga no logger fornecido (ou no slog padrão se nil).
func New(log *slog.Logger) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{log: log, byKind: map[string]float64{}}
}

// Add soma usd ao acumulado da categoria kind (ex.: "llm", "stt") e loga o total da
// sessão. É seguro chamar em um *Tracker nil (no-op) e com usd<=0 (apenas conta a
// chamada, sem alterar o total — útil quando o provedor não devolve o custo).
func (t *Tracker) Add(kind string, usd float64) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.calls++
	if usd > 0 {
		t.total += usd
		t.byKind[kind] += usd
	}
	total, llm, stt, calls := t.total, t.byKind["llm"], t.byKind["stt"], t.calls
	t.mu.Unlock()

	t.log.Info("custo acumulado (sessão)",
		"kind", kind,
		"ultimo_usd", usd,
		"llm_usd", llm,
		"stt_usd", stt,
		"total_usd", total,
		"chamadas", calls,
	)
}

// Total devolve o custo total (USD) acumulado na sessão. Seguro em um *Tracker nil.
func (t *Tracker) Total() float64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}
