package chatwoot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

// ComputeSignature reproduz a assinatura enviada pelo Chatwoot:
// sha256=HMAC-SHA256(secret, "{timestamp}.{raw_body}")
func ComputeSignature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature compara, em tempo constante, a assinatura recebida no header
// X-Chatwoot-Signature com a esperada.
func VerifySignature(secret, timestamp string, body []byte, provided string) bool {
	if secret == "" {
		return true // verificação desabilitada (modo dev)
	}
	expected := ComputeSignature(secret, timestamp, body)
	return hmac.Equal([]byte(expected), []byte(provided))
}
