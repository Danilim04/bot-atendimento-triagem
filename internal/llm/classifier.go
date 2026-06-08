// Package llm define a interface do condutor de triagem e suas implementações. O
// LLM orquestra a conversa de triagem: a cada turno ele ou responde ao cliente
// (saudação/pergunta curta), ou decide o setor de roteamento.
package llm

import "context"

// Turn é um turno do histórico de triagem.
type Turn struct {
	Role    string // "customer" | "bot"
	Content string
}

// Intent é a saída estruturada do condutor de triagem.
type Intent struct {
	Action string `json:"action"` // "reply" | "route"
	Reply  string `json:"reply"`  // mensagem ao cliente: saudação, pergunta ou aviso de encaminhamento
	Sector string `json:"sector"` // setor escolhido (action=route)
	Reason string `json:"reason"` // justificativa interna (logs)
}

// Classifier conduz a triagem a partir do histórico, decidindo a próxima ação.
type Classifier interface {
	Classify(ctx context.Context, systemPrompt string, history []Turn) (Intent, error)
}
