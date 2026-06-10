package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

	tr := NewOpenAITranscriber(srv.URL, "key", "whisper-large-v3", 5*time.Second, nil, nil)
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

// TestOpenRouterTranscriber_Transcribe valida que o áudio é enviado como JSON com o
// conteúdo em base64 (input_audio.data/format), que .oga vira "ogg" e que o texto
// da resposta é parseado e trimado.
func TestOpenRouterTranscriber_Transcribe(t *testing.T) {
	var (
		gotPath, gotCT, gotModel, gotLang, gotFormat, gotData string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		var body struct {
			Model      string `json:"model"`
			Language   string `json:"language"`
			InputAudio struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"input_audio"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		gotModel = body.Model
		gotLang = body.Language
		gotFormat = body.InputAudio.Format
		gotData = body.InputAudio.Data
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"  olá mundo  "}`))
	}))
	defer srv.Close()

	tr := NewOpenRouterTranscriber(srv.URL, "key", "openai/whisper-large-v3", 5*time.Second, nil, nil)
	text, err := tr.Transcribe(context.Background(), []byte("AUDIODATA"), "audio.oga", "pt")
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if text != "olá mundo" {
		t.Errorf("texto inesperado (esperava trim): %q", text)
	}
	if !strings.HasSuffix(gotPath, "/audio/transcriptions") {
		t.Errorf("endpoint inesperado: %q", gotPath)
	}
	if !strings.HasPrefix(gotCT, "application/json") {
		t.Errorf("content-type inesperado: %q", gotCT)
	}
	if gotModel != "openai/whisper-large-v3" {
		t.Errorf("model: %q", gotModel)
	}
	if gotLang != "pt" {
		t.Errorf("language: %q", gotLang)
	}
	if gotFormat != "ogg" {
		t.Errorf("format (.oga deve virar ogg): %q", gotFormat)
	}
	if decoded, _ := base64.StdEncoding.DecodeString(gotData); string(decoded) != "AUDIODATA" {
		t.Errorf("data base64 inesperada: %q (decodificado: %q)", gotData, decoded)
	}
}
