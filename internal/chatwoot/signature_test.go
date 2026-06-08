package chatwoot

import "testing"

func TestVerifySignature(t *testing.T) {
	secret := "s3cr3t"
	ts := "1700000000"
	body := []byte(`{"event":"message_created"}`)

	sig := ComputeSignature(secret, ts, body)
	if !VerifySignature(secret, ts, body, sig) {
		t.Errorf("assinatura válida foi rejeitada")
	}
	if VerifySignature(secret, ts, body, "sha256=deadbeef") {
		t.Errorf("assinatura inválida foi aceita")
	}
	if VerifySignature(secret, "1700000001", body, sig) {
		t.Errorf("timestamp diferente deveria invalidar")
	}
}

func TestVerifySignature_EmptySecretSkips(t *testing.T) {
	if !VerifySignature("", "ts", []byte("x"), "") {
		t.Errorf("segredo vazio deveria pular a verificação")
	}
}
