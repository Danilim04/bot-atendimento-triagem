package engine

import (
	"context"
	"path"
	"strings"

	"bot-atendimento-promosystem/internal/chatwoot"
)

// audioFallbackMsg é enviado quando o cliente manda um áudio que o bot não
// consegue transcrever (STT desabilitado, erro ou formato não suportado).
const audioFallbackMsg = "Não consegui ouvir o seu áudio. Pode escrever a sua mensagem, por favor? 🙂"

// resolveIncomingContent devolve o texto a usar na triagem para a mensagem do
// cliente: o próprio Content, quando houver; ou a transcrição de um anexo de áudio.
// O segundo retorno é false quando não há nada a processar (anexo não-áudio, áudio
// não transcrito etc.) — nesses casos a função já tratou o cliente quando aplicável.
func (e *Engine) resolveIncomingContent(ctx context.Context, msg *chatwoot.MessageCreated) (string, bool) {
	if s := strings.TrimSpace(msg.Content); s != "" {
		return msg.Content, true
	}

	att, ok := msg.FirstAudioAttachment()
	if !ok {
		e.log.Info("mensagem sem texto e sem áudio reconhecível; ignorada",
			"conversation_id", msg.Conversation.ID, "anexos", len(msg.Attachments))
		return "", false
	}

	if e.transcriber == nil {
		e.log.Warn("áudio recebido mas transcrição desabilitada (STT não configurado)",
			"conversation_id", msg.Conversation.ID)
		_ = e.cw.SendMessage(ctx, msg.Conversation.ID, audioFallbackMsg, false)
		return "", false
	}

	text, err := e.transcribeAudio(ctx, &att)
	if err != nil {
		e.log.Error("falha ao transcrever áudio", "conversation_id", msg.Conversation.ID, "err", err)
		_ = e.cw.SendMessage(ctx, msg.Conversation.ID, audioFallbackMsg, false)
		return "", false
	}
	if strings.TrimSpace(text) == "" {
		e.log.Warn("transcrição vazia", "conversation_id", msg.Conversation.ID)
		_ = e.cw.SendMessage(ctx, msg.Conversation.ID, audioFallbackMsg, false)
		return "", false
	}

	e.log.Info("áudio transcrito", "conversation_id", msg.Conversation.ID, "texto", text)
	return text, true
}

// transcribeAudio baixa o anexo do Chatwoot e o transcreve via o provedor STT.
func (e *Engine) transcribeAudio(ctx context.Context, att *chatwoot.Attachment) (string, error) {
	data, _, err := e.cw.DownloadAttachment(ctx, att.DataURL)
	if err != nil {
		return "", err
	}
	return e.transcriber.Transcribe(ctx, data, audioFilename(att), e.cfg.STTLanguage)
}

// audioFilename infere um nome de arquivo com extensão a partir do anexo, para o
// provedor STT reconhecer o formato. Default audio.ogg (voice notes opus/.oga).
func audioFilename(att *chatwoot.Attachment) string {
	if ext := strings.TrimPrefix(strings.TrimSpace(att.Extension), "."); ext != "" {
		return "audio." + ext
	}
	if base := path.Base(att.DataURL); base != "" && base != "." && base != "/" {
		if i := strings.IndexAny(base, "?#"); i >= 0 {
			base = base[:i]
		}
		if path.Ext(base) != "" {
			return base
		}
	}
	return "audio.ogg"
}
