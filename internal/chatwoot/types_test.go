package chatwoot

import (
	"encoding/json"
	"testing"
)

func TestMessageTypeUnmarshal_Int(t *testing.T) {
	cases := map[string]MessageType{
		`0`: MessageIncoming,
		`1`: MessageOutgoing,
		`2`: MessageActivity,
		`3`: MessageTemplate,
	}
	for in, want := range cases {
		var mt MessageType
		if err := json.Unmarshal([]byte(in), &mt); err != nil {
			t.Fatalf("unmarshal %q: %v", in, err)
		}
		if mt != want {
			t.Errorf("input %q: got %q want %q", in, mt, want)
		}
	}
}

func TestMessageTypeUnmarshal_String(t *testing.T) {
	var mt MessageType
	if err := json.Unmarshal([]byte(`"incoming"`), &mt); err != nil {
		t.Fatal(err)
	}
	if !mt.IsIncoming() {
		t.Errorf("esperava incoming, got %q", mt)
	}
}

func TestMessageCreated_ParseRealPayload(t *testing.T) {
	// message_type como inteiro + sender contato + labels na conversa.
	payload := `{
		"event": "message_created",
		"id": 1234,
		"content": "preciso de ajuda com meu boleto",
		"message_type": 0,
		"private": false,
		"sender": {"id": 89, "name": "Jane", "type": "contact"},
		"conversation": {
			"id": 567,
			"account_id": 1,
			"status": "open",
			"labels": ["fila-bot"]
		},
		"account": {"id": 1},
		"inbox": {"id": 7}
	}`
	var msg MessageCreated
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		t.Fatal(err)
	}
	if !msg.MessageType.IsIncoming() {
		t.Errorf("message_type incoming esperado")
	}
	if !msg.Sender.IsContact() {
		t.Errorf("sender deveria ser contato")
	}
	if msg.Conversation.ID != 567 {
		t.Errorf("conversation id errado: %d", msg.Conversation.ID)
	}
	if !HasLabel(msg.Conversation.Labels, "fila-bot") {
		t.Errorf("label fila-bot esperada")
	}
}

func TestConversationUpdated_FlatConversationFields(t *testing.T) {
	// No conversation_updated os campos da conversa vêm no nível raiz.
	payload := `{
		"event": "conversation_updated",
		"id": 999,
		"account_id": 1,
		"status": "open",
		"labels": ["fila-csat"],
		"changed_attributes": [{"labels": {"current_value": ["fila-csat"], "previous_value": []}}]
	}`
	var ev ConversationUpdated
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Conversation.ID != 999 {
		t.Errorf("id achatado não lido: %d", ev.Conversation.ID)
	}
	if !HasLabel(ev.Conversation.Labels, "fila-csat") {
		t.Errorf("label fila-csat esperada")
	}
	if len(ev.ChangedAttributes) != 1 {
		t.Errorf("changed_attributes não parseado")
	}
}

func TestConversationUpdated_LabelTransition(t *testing.T) {
	parse := func(t *testing.T, payload string) *ConversationUpdated {
		t.Helper()
		var ev ConversationUpdated
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			t.Fatal(err)
		}
		return &ev
	}

	// Etiqueta recém-adicionada: ausente em previous, presente em current.
	added := parse(t, `{"event":"conversation_updated","labels":["fila-bot"],
		"changed_attributes":[{"labels":{"current_value":["fila-bot"],"previous_value":[]}}]}`)
	if !added.LabelsChanged() {
		t.Errorf("LabelsChanged() deveria ser true quando há mudança de labels")
	}
	if !added.LabelJustAdded("fila-bot") {
		t.Errorf("LabelJustAdded(fila-bot) deveria ser true na aplicação da etiqueta")
	}

	// Etiqueta já presente; o que mudou foi a adição de outra.
	other := parse(t, `{"event":"conversation_updated","labels":["fila-bot","vip"],
		"changed_attributes":[{"labels":{"current_value":["fila-bot","vip"],"previous_value":["fila-bot"]}}]}`)
	if !other.LabelsChanged() {
		t.Errorf("LabelsChanged() deveria ser true")
	}
	if other.LabelJustAdded("fila-bot") {
		t.Errorf("LabelJustAdded(fila-bot) deveria ser false quando já estava presente")
	}

	// Update sem mudança de etiquetas (ex.: designação): sem atributo "labels".
	noLabel := parse(t, `{"event":"conversation_updated","labels":["fila-bot"],
		"changed_attributes":[{"assignee_id":{"current_value":7,"previous_value":null}}]}`)
	if noLabel.LabelsChanged() {
		t.Errorf("LabelsChanged() deveria ser false sem atributo labels")
	}
	if noLabel.LabelJustAdded("fila-bot") {
		t.Errorf("LabelJustAdded(fila-bot) deveria ser false sem mudança de labels")
	}
}

func TestReplaceLabel(t *testing.T) {
	got := ReplaceLabel([]string{"fila-bot", "vip"}, "fila-bot", "fila-financeiro")
	if HasLabel(got, "fila-bot") {
		t.Errorf("fila-bot deveria ter sido removida: %v", got)
	}
	if !HasLabel(got, "fila-financeiro") || !HasLabel(got, "vip") {
		t.Errorf("conjunto final incorreto: %v", got)
	}

	// Remoção pura (add vazio).
	got = ReplaceLabel([]string{"fila-csat", "vip"}, "fila-csat", "")
	if HasLabel(got, "fila-csat") || !HasLabel(got, "vip") {
		t.Errorf("remoção pura incorreta: %v", got)
	}

	// Não duplica label já presente.
	got = ReplaceLabel([]string{"fila-financeiro"}, "fila-bot", "fila-financeiro")
	if len(got) != 1 {
		t.Errorf("não deveria duplicar: %v", got)
	}
}
