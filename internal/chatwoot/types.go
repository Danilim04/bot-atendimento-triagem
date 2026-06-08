// Package chatwoot contém os tipos dos payloads de webhook do Chatwoot v4.4.0 e
// o cliente REST de saída.
package chatwoot

import (
	"encoding/json"
	"strconv"
	"strings"
)

// MessageType normaliza o campo message_type do webhook, que pode chegar como
// inteiro (0=incoming, 1=outgoing, 2=activity, 3=template) ou como string,
// dependendo da versão do Chatwoot.
type MessageType string

const (
	MessageIncoming MessageType = "incoming"
	MessageOutgoing MessageType = "outgoing"
	MessageActivity MessageType = "activity"
	MessageTemplate MessageType = "template"
)

// UnmarshalJSON aceita tanto a forma inteira quanto a forma string.
func (m *MessageType) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*m = MessageType(str)
		return nil
	}
	var n int
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	switch n {
	case 0:
		*m = MessageIncoming
	case 1:
		*m = MessageOutgoing
	case 2:
		*m = MessageActivity
	case 3:
		*m = MessageTemplate
	default:
		*m = MessageType(strconv.Itoa(n))
	}
	return nil
}

func (m MessageType) IsIncoming() bool { return m == MessageIncoming }
func (m MessageType) IsOutgoing() bool { return m == MessageOutgoing }

// Envelope é usado para descobrir o tipo do evento antes do parse completo.
type Envelope struct {
	Event string `json:"event"`
}

// MessageCreated representa o evento message_created.
type MessageCreated struct {
	Event             string          `json:"event"`
	ID                int64           `json:"id"`
	Content           string          `json:"content"`
	MessageType       MessageType     `json:"message_type"`
	Private           bool            `json:"private"`
	ContentType       string          `json:"content_type"`
	ContentAttributes json.RawMessage `json:"content_attributes"`
	Sender            Sender          `json:"sender"`
	Conversation      Conversation    `json:"conversation"`
	Account           Account         `json:"account"`
	Inbox             Inbox           `json:"inbox"`
}

// ConversationUpdated representa o evento conversation_updated. No payload do
// Chatwoot os atributos da conversa vêm "achatados" no nível raiz, por isso a
// struct Conversation é embutida.
type ConversationUpdated struct {
	Event             string                   `json:"event"`
	ChangedAttributes []map[string]ChangeValue `json:"changed_attributes"`
	Conversation
}

// ChangeValue é o par current/previous de cada atributo alterado.
type ChangeValue struct {
	CurrentValue  json.RawMessage `json:"current_value"`
	PreviousValue json.RawMessage `json:"previous_value"`
}

// LabelsChanged informa se esta atualização inclui uma mudança na lista de
// etiquetas. O Chatwoot envia um atributo "labels" em changed_attributes quando,
// e somente quando, as etiquetas da conversa mudaram; updates de outra natureza
// (designação, status, atributos custom) não o trazem.
func (ev *ConversationUpdated) LabelsChanged() bool {
	for _, attr := range ev.ChangedAttributes {
		if _, ok := attr["labels"]; ok {
			return true
		}
	}
	return false
}

// LabelJustAdded informa se target passou a constar nas etiquetas NESTA
// atualização: presente em current_value e ausente em previous_value do atributo
// "labels". Permite distinguir a aplicação da etiqueta (o gatilho de entrada) de
// updates que apenas mexem em outra coisa enquanto a etiqueta já estava presente.
// Retorna false quando a atualização não traz mudança de etiquetas.
func (ev *ConversationUpdated) LabelJustAdded(target string) bool {
	for _, attr := range ev.ChangedAttributes {
		cv, ok := attr["labels"]
		if !ok {
			continue
		}
		var current, previous []string
		_ = json.Unmarshal(cv.CurrentValue, &current)
		_ = json.Unmarshal(cv.PreviousValue, &previous)
		return HasLabel(current, target) && !HasLabel(previous, target)
	}
	return false
}

// Conversation reúne os campos da conversa que o bot utiliza.
type Conversation struct {
	ID               int64          `json:"id"`
	AccountID        int64          `json:"account_id"`
	InboxID          int64          `json:"inbox_id"`
	Status           string         `json:"status"`
	Labels           []string       `json:"labels"`
	Meta             Meta           `json:"meta"`
	CustomAttributes map[string]any `json:"custom_attributes"`
}

// Meta carrega o contato (sender) e o agente designado da conversa.
type Meta struct {
	Sender   Contact `json:"sender"`
	Assignee *User   `json:"assignee"`
}

// Contact é o cliente final.
type Contact struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// User é um agente/atendente.
type User struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Sender é o autor de uma mensagem (contato ou agente).
type Sender struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Type  string `json:"type"` // "contact" | "user"
}

// IsContact informa se o remetente é o cliente final. Campo vazio é tratado
// como contato porque algumas versões omitem o type em mensagens de entrada.
func (s Sender) IsContact() bool {
	return s.Type == "contact" || s.Type == ""
}

// Account identifica a conta do Chatwoot.
type Account struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Inbox identifica a caixa de entrada (canal).
type Inbox struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// HasLabel informa se a etiqueta target está presente na lista.
func HasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// ReplaceLabel devolve uma nova lista de etiquetas removendo remove (se houver)
// e garantindo a presença de add. add vazio significa apenas remoção.
func ReplaceLabel(current []string, remove, add string) []string {
	out := make([]string, 0, len(current)+1)
	addPresent := false
	for _, l := range current {
		if l == remove {
			continue
		}
		if l == add {
			addPresent = true
		}
		out = append(out, l)
	}
	if add != "" && !addPresent {
		out = append(out, add)
	}
	return out
}
