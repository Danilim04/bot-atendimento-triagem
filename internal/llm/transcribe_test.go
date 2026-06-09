package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestOpenAITranscriber_Transcribe valida que o áudio é enviado como multipart com
// os campos esperados e que a resposta JSON é parseada.
func TestOpenAITranscriber_Transcribe(t *testing.T) {
	var (
		gotModel, gotLang, gotFilename string
		gotFile                        []byte
		gotPath                        string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
		}
		gotModel = r.FormValue("model")
		gotLang = r.FormValue("language")
		if f, hdr, err := r.FormFile("file"); err != nil {
			t.Errorf("form file ausente: %v", err)
		} else {
			gotFilename = hdr.Filename
			gotFile, _ = io.ReadAll(f)
			f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"  olá mundo  "}`))
	}))
	defer srv.Close()

	tr := NewOpenAITranscriber(srv.URL, "key", "whisper-large-v3", 5*time.Second, nil)
	text, err := tr.Transcribe(context.Background(), []byte("AUDIODATA"), "audio.ogg", "pt")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if text != "olá mundo" {
		t.Errorf("texto inesperado (esperava trim): %q", text)
	}
	if !strings.HasSuffix(gotPath, "/audio/transcriptions") {
		t.Errorf("endpoint inesperado: %q", gotPath)
	}
	if gotModel != "whisper-large-v3" {
		t.Errorf("model: %q", gotModel)
	}
	if gotLang != "pt" {
		t.Errorf("language: %q", gotLang)
	}
	if gotFilename != "audio.ogg" {
		t.Errorf("filename: %q", gotFilename)
	}
	if string(gotFile) != "AUDIODATA" {
		t.Errorf("bytes do arquivo: %q", gotFile)
	}
}
