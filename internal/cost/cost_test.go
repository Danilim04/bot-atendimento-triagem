package cost

import (
	"io"
	"log/slog"
	"testing"
)

func discardTracker() *Tracker {
	return New(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestTrackerAccumulates verifica a soma do total e que chamadas com custo 0
// (provedor sem cost) não alteram o total. Usa valores exatos em float64.
func TestTrackerAccumulates(t *testing.T) {
	tr := discardTracker()
	tr.Add("llm", 1.5)
	tr.Add("stt", 0.25)
	tr.Add("llm", 0) // chamada sem custo: conta mas não soma
	if got := tr.Total(); got != 1.75 {
		t.Fatalf("Total() = %v, esperado 1.75", got)
	}
}

// TestNilTrackerIsNoOp garante que um *Tracker nil é um no-op seguro.
func TestNilTrackerIsNoOp(t *testing.T) {
	var tr *Tracker
	tr.Add("llm", 1) // não deve entrar em panic
	if got := tr.Total(); got != 0 {
		t.Fatalf("nil Total() = %v, esperado 0", got)
	}
}
