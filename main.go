package main

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

const (
	EmbeddingModel = "bge-m3:latest"
	ChunkSize      = 1000
	ChunkOverlap   = 100
	IndexFile      = "db_vectores.gob"
)

// OllamaURL é configurable (pódese sobrescribir coa variable de contorno OLLAMA_URL)
var OllamaURL = envOrDefault("OLLAMA_URL", "http://localhost:11434")

// ChatModel é unha variable global cun modelo por defecto
var ChatModel = "gemma4:31b-cloud"

// Modelos de chat locais que se consideran "optimais" para o RAG por defecto
var ModelosLocais = []string{"qwen3.5:4b-mlx", "qwen3.5:9b-mlx", "llama3.2:3b", "gemma4:12b-mlx"}

// EstadoModelos describe que modelos hai cargados en Ollama (local)
type EstadoModelos struct {
	BGE3            bool
	LocaisPresentes []string
	LocaisFaltan    []string
}

var estadoModelos EstadoModelos

// ramModelos contén os modelos que están cargados na RAM de Ollama (vía /api/ps)
var ramModelos = map[string]bool{}

// envOrDefault devolve o valor da variable de contorno key ou, se está baleira, o fallback.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type Chunk struct {
	Text   string
	Source string
	Page   int
	Vector []float64
}

var dbGlobal []Chunk

type QueryRequest struct {
	Question string `json:"question"`
	Model    string `json:"model"` // Novo campo para o modelo
}

type QueryResponse struct {
	Answer  string   `json:"answer"`
	Sources []Chunk  `json:"sources"`
	Meta    ChatMeta `json:"meta"`
}

// ChatMeta contén os datos técnicos da chamada ao LLM (modelo, tempos, tokens e contexto enviado).
type ChatMeta struct {
	Model      string `json:"model"`           // modelo empregado (só LLM)
	TotalMs    int64  `json:"totalMs"`         // tempo total da chamada
	EvalMs     int64  `json:"evalMs"`          // tempo só de avaliación
	PromptEval int    `json:"promptEvalCount"` // tokens enviados (prompt)
	EvalCount  int    `json:"evalCount"`       // tokens recibidos (resposta)
	Contexto   int    `json:"contextoChars"`   // caracteres do contexto enviado
	Fontes     int    `json:"fontes"`          // nº de fontes (chunks) usadas

	// ContextoTexto contén o texto íntegro enviado ao LLM (sistema + contexto + pregunta)
	ContextoTexto string `json:"contextoTexto,omitempty"`
}

func main() {
	indexMode := flag.Bool("index", false, "Indexar os PDFs do directorio actual")
	query := flag.String("q", "", "Pregunta a facer ao bot por liña de comandos")
	selectedModel := flag.String("model", "", "Modelo de Ollama a usar (ex: qwen3.5:4b-mlx, llama3.2:3b)")
	webMode := flag.Bool("web", false, "Iniciar servidor web")
	host := flag.String("host", envOrDefault("HOST", ""), "Interface de rede do servidor web (baleiro = todas, ex: 127.0.0.1)")
	port := flag.String("port", envOrDefault("PORT", "8080"), "Porto do servidor web")
	flag.Parse()

	// Se se especifica un modelo por CLI, sobrescríbese o por defecto
	if *selectedModel != "" {
		ChatModel = *selectedModel
	}

	if *indexMode {
		fmt.Println("🚀 Iniciando a fase de indexación...")
		indexarPDFs()
		return
	}

	// Comprobación de modelos dispoñibles en Ollama ao arrincar (shell e web)
	reportarModelos()
	resumoModelos(estadoModelos)

	// Carga automática de bge-m3 na RAM se non está xa (necesario para responder)
	cargarBGE3AoArrincar()

	if *query != "" {
		cargarDB()
		fmt.Printf("🤖 Usando modelo: %s\n", ChatModel)
		resp, fuentes, meta := procesarPregunta(*query, ChatModel)
		fmt.Printf("\n--- RESPOSTA ---\n%s\n", resp)
		fmt.Println("\n--- FONTES CONSULTADAS ---")
		for _, f := range fuentes {
			fmt.Printf("- %s (Páx %d)\n", f.Source, f.Page)
		}
		fmt.Printf("\n[Detalles técnicos] Modelo: %s | Tempo total: %d ms (avaliación: %d ms) | Tokens enviados: %d | Tokens recibidos: %d | Contexto enviado: %d caracteres (%d fontes)\n",
			meta.Model, meta.TotalMs, meta.EvalMs, meta.PromptEval, meta.EvalCount, meta.Contexto, meta.Fontes)
		rexistrarConsulta(*query, meta)
		return
	}

	if *webMode {
		cargarDB()
		iniciarServidorWeb(*host, *port)
		return
	}

	fmt.Println("Uso:\n  pdfbot -index (indexar PDFs)\n  pdfbot -q \"pregunta\" -model \"qwen3.5:4b-mlx\" (consola)\n  pdfbot -web (servidor web)\n  pdfbot -web -host 0.0.0.0 -port 9000 (servidor web con bind/porto personalizados)")
}

func cargarDB() {
	file, err := os.Open(IndexFile)
	if err != nil {
		log.Fatal("Non se atopou o índice. Executa primeiro con -index")
	}
	defer file.Close()
	gob.NewDecoder(file).Decode(&dbGlobal)
	fmt.Printf("📦 Base de datos cargada na RAM (%d chunks)\n", len(dbGlobal))
}

func procesarPregunta(pregunta string, modelName string) (string, []Chunk, ChatMeta) {
	vectorPregunta := pedirEmbeddingOllama(pregunta)

	type Puntuacion struct {
		Chunk Chunk
		Score float64
	}
	var resultados []Puntuacion

	for _, c := range dbGlobal {
		score := similitudeCoseno(vectorPregunta, c.Vector)
		resultados = append(resultados, Puntuacion{Chunk: c, Score: score})
	}

	sort.Slice(resultados, func(i, j int) bool {
		return resultados[i].Score > resultados[j].Score
	})

	contexto := ""
	var chunksUsados []Chunk

	for i := 0; i < 3 && i < len(resultados); i++ {
		c := resultados[i].Chunk
		contexto += fmt.Sprintf("---\nDocumento: %s (Páx: %d)\nTexto: %s\n", c.Source, c.Page, c.Text)
		chunksUsados = append(chunksUsados, c)
	}

	systemPrompt := "Es un asistente que responde de forma precisa e en galego baseándote SÓ no contexto proporcionado."
	userPrompt := fmt.Sprintf("Contexto:\n%s\n\nPregunta: %s", contexto, pregunta)

	// Pasamos o modelo escollido
	resposta, meta := pedirChatOllama(systemPrompt, userPrompt, modelName)
	meta.Fontes = len(chunksUsados)
	return resposta, chunksUsados, meta
}

// reportarModelos consulta a API de Ollama e actualiza o estado global de modelos.
func reportarModelos() EstadoModelos {
	est := EstadoModelos{}
	dispoñibles := map[string]bool{}

	resp, err := http.Get(OllamaURL + "/api/tags")
	if err != nil {
		fmt.Printf("⚠️ Non se puido contactar con Ollama (%s): %v\n", OllamaURL, err)
	} else {
		defer resp.Body.Close()
		var result struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if derr := json.NewDecoder(resp.Body).Decode(&result); derr != nil {
			fmt.Printf("⚠️ Resposta de Ollama inválida: %v\n", derr)
		} else {
			for _, m := range result.Models {
				dispoñibles[m.Name] = true
			}
		}
	}

	est.BGE3 = dispoñibles[EmbeddingModel]
	for _, m := range ModelosLocais {
		if dispoñibles[m] {
			est.LocaisPresentes = append(est.LocaisPresentes, m)
		} else {
			est.LocaisFaltan = append(est.LocaisFaltan, m)
		}
	}

	estadoModelos = est
	return est
}

func resumoModelos(e EstadoModelos) {
	fmt.Println("🗂️ Modelos en Ollama local:")
	if e.BGE3 {
		fmt.Println("  ✅ Embeddamento bge-m3:latest presente")
	} else {
		fmt.Println("  ❌ FALTA o modelo de embeddamento bge-m3:latest (necesario para responder)")
	}
	if len(e.LocaisPresentes) > 0 {
		fmt.Println("  🧠 Razoamento local dispoñible:", strings.Join(e.LocaisPresentes, ", "))
	} else {
		fmt.Println("  🧠 Ningún modelo local de razoamento dispoñible")
	}
	if len(e.LocaisFaltan) > 0 {
		fmt.Println("  ⚠️ Faltan modelos locais:", strings.Join(e.LocaisFaltan, ", "))
	}
}

// modelosEnRAM consulta /api/ps e devolve que modelos están cargados na RAM de Ollama agora.
func modelosEnRAM() map[string]bool {
	enRAM := map[string]bool{}
	resp, err := http.Get(OllamaURL + "/api/ps")
	if err != nil {
		return enRAM
	}
	defer resp.Body.Close()
	var result struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&result); derr != nil {
		return enRAM
	}
	for _, m := range result.Models {
		enRAM[m.Name] = true
	}
	return enRAM
}

// cargarBGE3AoArrincar carga o modelo de embeddings na RAM se non está xa.
func cargarBGE3AoArrincar() {
	ramModelos = modelosEnRAM()
	if !estadoModelos.BGE3 {
		fmt.Println("  ❌ bge-m3 non está descargado; descárgao co botón 'Precargar modelos que faltan' (ou: ollama pull bge-m3:latest)")
		return
	}
	if ramModelos[EmbeddingModel] {
		fmt.Println("  ✅ bge-m3 en RAM (Embeddings)")
		return
	}
	fmt.Println("  🔄 Cargando bge-m3 na RAM...")
	if err := cargarEmbeddingRAM(); err != nil {
		fmt.Printf("  ❌ Erro cargando bge-m3 na RAM: %v\n", err)
		return
	}
	ramModelos[EmbeddingModel] = true
	fmt.Println("  ✅ bge-m3 en RAM (Embeddings)")
}

func htmlEstadoModelos() string {
	var b strings.Builder
	if ramModelos[EmbeddingModel] {
		b.WriteString("<p class='ok'>✅ <code>bge-m3:latest</code> en RAM (Embeddings)</p>")
	} else if !estadoModelos.BGE3 {
		b.WriteString("<p class='bad'>❌ <code>bge-m3:latest</code> non está descargado</p>")
	} else {
		b.WriteString("<p class='warn'>⚠️ <code>bge-m3:latest</code> non está en RAM</p>")
	}
	if len(estadoModelos.LocaisFaltan) > 0 {
		b.WriteString("<p class='warn'>⚠️ Faltan: <code>" + strings.Join(estadoModelos.LocaisFaltan, "</code>, <code>") + "</code></p>")
	}
	if len(estadoModelos.LocaisFaltan) > 0 || !estadoModelos.BGE3 {
		b.WriteString("<button type='button' id='preloadBtn' class='preload-btn' onclick='precargarModelos()'>⬇️ Precargar modelos que faltan</button>")
	}
	b.WriteString("<div id='llmStatus'></div>")
	b.WriteString("<button type='button' id='ramBtn' class='preload-btn' onclick='forzarCargaRAM()'>🧠 Forzar Carga en RAM</button>")
	b.WriteString("<div id='preloadStatus' style='display:none; font-size:0.85rem; margin-top:8px;'></div>")
	return b.String()
}

func iniciarServidorWeb(host, port string) {
	http.HandleFunc("/", servirmainPage)
	http.HandleFunc("/api/query", handleQuery)
	http.HandleFunc("/api/preload", handlePreload)
	http.HandleFunc("/api/load-ram", handleLoadRAM)

	// O propio FileServer de Go xa lista os ficheiros ao acceder a /docs/
	http.Handle("/docs/", http.StripPrefix("/docs/", http.FileServer(http.Dir("."))))

	// host baleiro => lánzase como ":port" e bindea todas as interfaces
	addr := fmt.Sprintf("%s:%s", host, port)
	dir := "localhost"
	if host != "" {
		dir = host
	}
	fmt.Printf("🌐 Servidor web listo en: http://%s:%s (bind: %q)\n", dir, port, host)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método non permitido", http.StatusMethodNotAllowed)
		return
	}

	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Question == "" {
		http.Error(w, "Petición inválida", http.StatusBadRequest)
		return
	}

	// Se o cliente web non manda modelo, usamos o global por defecto
	modeloAUsar := req.Model
	if modeloAUsar == "" {
		modeloAUsar = ChatModel
	}

	answer, sources, meta := procesarPregunta(req.Question, modeloAUsar)

	rexistrarConsulta(req.Question, meta)

	resp := QueryResponse{
		Answer:  answer,
		Sources: sources,
		Meta:    meta,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// rexistrarConsulta engade unha entrada de log (sen o texto do contexto, só as métricas)
// ao ficheiro diario logs/consultas_AAAA-MM-DD.log en formato JSON por liña.
func rexistrarConsulta(pregunta string, meta ChatMeta) {
	if err := os.MkdirAll("logs", 0o755); err != nil {
		log.Printf("⚠️ Non se puido crear o directorio logs/: %v", err)
		return
	}
	ficheiro := filepath.Join("logs", fmt.Sprintf("consultas_%s.log", time.Now().Format("2006-01-02")))
	f, err := os.OpenFile(ficheiro, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Printf("⚠️ Non se puido abrir o log %s: %v", ficheiro, err)
		return
	}
	defer f.Close()

	entrada := map[string]interface{}{
		"time":            time.Now().Format(time.RFC3339),
		"model":           meta.Model,
		"totalMs":         meta.TotalMs,
		"evalMs":          meta.EvalMs,
		"promptEvalCount": meta.PromptEval,
		"evalCount":       meta.EvalCount,
		"contextoChars":   meta.Contexto,
		"fontes":          meta.Fontes,
		"question":        pregunta,
	}
	linea, err := json.Marshal(entrada)
	if err != nil {
		log.Printf("⚠️ Erro serializando o log: %v", err)
		return
	}
	f.Write(append(linea, '\n'))
}

type PreloadResponse struct {
	Ok      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handlePreload precarga (ollama pull) os modelos locais que faltan: bge-m3 e/ou os razoadores locais.
func handlePreload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método non permitido", http.StatusMethodNotAllowed)
		return
	}

	// Recalcula o estado e recolle os modelos locais que faltan
	est := reportarModelos()
	faltan := make([]string, 0, len(est.LocaisFaltan)+1)
	if !est.BGE3 {
		faltan = append(faltan, EmbeddingModel)
	}
	faltan = append(faltan, est.LocaisFaltan...)

	resp := PreloadResponse{}
	w.Header().Set("Content-Type", "application/json")
	if len(faltan) == 0 {
		resp.Ok = true
		resp.Message = "Todos os modelos locais xa están dispoñibles"
		json.NewEncoder(w).Encode(resp)
		return
	}

	for _, m := range faltan {
		if err := precargarModelo(m); err != nil {
			resp.Ok = false
			resp.Error = fmt.Sprintf("Erro descargando %s: %v", m, err)
			json.NewEncoder(w).Encode(resp)
			return
		}
	}

	resp.Ok = true
	resp.Message = fmt.Sprintf("Modelos precargados correctamente (%d): %s", len(faltan), strings.Join(faltan, ", "))
	json.NewEncoder(w).Encode(resp)
}

// ollamaPost fai un POST xenérico á API de Ollama e devolve erro se o status non é 200.
func ollamaPost(endpoint string, payload map[string]interface{}) error {
	reqBody, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	resp, err := http.Post(OllamaURL+endpoint, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		Error string `json:"error"`
	}
	if derr := json.NewDecoder(resp.Body).Decode(&result); derr != nil {
		return derr
	}
	if result.Error != "" {
		return fmt.Errorf("%s", result.Error)
	}
	return nil
}

// precargarModelo pídelle a Ollama que descargue un modelo (equivalente a `ollama pull`).
func precargarModelo(m string) error {
	return ollamaPost("/api/pull", map[string]interface{}{
		"model":  m,
		"stream": false,
	})
}

// cargarEmbeddingRAM forxa a carga en RAM do modelo de embeddings bge-m3 (queda na RAM).
func cargarEmbeddingRAM() error {
	return ollamaPost("/api/embeddings", map[string]interface{}{
		"model":      EmbeddingModel,
		"prompt":     "carga",
		"keep_alive": -1,
	})
}

// cargarChatRAM forxa a carga en RAM do modelo de chat con keep_alive = -1 (queda na RAM).
func cargarChatRAM(modelo string) error {
	return ollamaPost("/api/generate", map[string]interface{}{
		"model":      modelo,
		"prompt":     "",
		"stream":     false,
		"keep_alive": -1,
	})
}

// LoadRAMResponse describe o resultado da carga forzada en RAM.
type LoadRAMResponse struct {
	Ok          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
	EmbeddingMs int64  `json:"embeddingMs"`
	ChatMs      int64  `json:"chatMs"`
}

// handleLoadRAM forza a carga en RAM do modelo de embeddings e do modelo de chat escollido.
func handleLoadRAM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Método non permitido", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	modelo := req.Model
	if modelo == "" {
		modelo = ChatModel
	}

	resp := LoadRAMResponse{Ok: true}
	w.Header().Set("Content-Type", "application/json")

	// Os modelos cloud non se cargan na RAM local: o cliente desactiva o botón, pero protexemos o endpoint.
	if strings.Contains(modelo, "-cloud") {
		resp.Ok = false
		resp.Error = "Os modelos cloud non se cargan na RAM local"
		json.NewEncoder(w).Encode(resp)
		return
	}

	t0 := time.Now()
	if err := cargarEmbeddingRAM(); err != nil {
		resp.Ok = false
		resp.Error = fmt.Sprintf("Erro cargando bge-m3 na RAM: %v", err)
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp.EmbeddingMs = time.Since(t0).Milliseconds()

	t1 := time.Now()
	if err := cargarChatRAM(modelo); err != nil {
		resp.Ok = false
		resp.Error = fmt.Sprintf("Erro cargando %s na RAM: %v", modelo, err)
		json.NewEncoder(w).Encode(resp)
		return
	}
	resp.ChatMs = time.Since(t1).Milliseconds()

	// Actualiza o estado de RAM coñecido pola aplicación
	ramModelos[EmbeddingModel] = true
	ramModelos[modelo] = true

	json.NewEncoder(w).Encode(resp)
}

func servirmainPage(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html lang="gl">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Consulta Regulamento Municipal - Ames</title>
    <script src="https://cdn.jsdelivr.net/npm/marked/marked.min.js"></script>
    <style>
        * { box-sizing: border-box; }
        body { font-family: system-ui, -apple-system, sans-serif; margin: 0; padding: 0; background: #f4f6f8; color: #222; display: flex; height: 100vh; overflow: hidden; }
        
        /* Panel Lateral (Sidebar) para ver o corpus de PDFs */
        .sidebar { width: 280px; background: #1e293b; color: #f8fafc; padding: 20px; display: flex; flex-direction: column; border-right: 1px solid #334155; }
        .sidebar h2 { font-size: 1.1rem; color: #94a3b8; margin-top: 0; text-transform: uppercase; letter-spacing: 0.05em; border-bottom: 1px solid #334155; padding-bottom: 10px; }
        .pdf-list { list-style: none; padding: 0; margin: 0; overflow-y: auto; flex-grow: 1; }
        .pdf-list li { margin-bottom: 8px; }
        .pdf-list a { color: #38bdf8; text-decoration: none; font-size: 0.9rem; word-break: break-all; display: block; padding: 6px 8px; border-radius: 4px; transition: background 0.2s; }
        .pdf-list a:hover { background: #334155; text-decoration: underline; }

        /* Panel Lateral Dereito: estado dos modelos */
        .sidebar-right { border-left: 1px solid #334155; border-right: none; }
        #modelStatus p { font-size: 0.85rem; line-height: 1.5; padding: 8px 10px; border-radius: 6px; margin: 0 0 8px; }
        .sidebar-right #modelStatus .preload-btn { margin-top: 4px; font-size: 0.9rem; }
        .sidebar-right #modelStatus #preloadStatus { color: #e2e8f0; }
        #modelStatus p.ok { background: rgba(34,197,94,0.15); color: #86efac; }
        #modelStatus p.bad { background: rgba(239,68,68,0.15); color: #fca5a5; }
        #modelStatus p.warn { background: rgba(245,158,11,0.15); color: #fcd34d; }
        #modelStatus code { background: #0f172a; padding: 1px 5px; border-radius: 4px; color: #e2e8f0; }

        /* Contido Principal */
        .main-content { flex-grow: 1; padding: 30px; overflow-y: auto; max-width: 900px; margin: 0 auto; }
        h1 { color: #0f172a; margin-top: 0; }
        .box { background: white; padding: 24px; border-radius: 8px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); margin-bottom: 24px; border: 1px solid #e2e8f0; }
        
        label { display: block; margin-bottom: 6px; font-weight: 600; color: #475569; }
        input[type="text"], select { width: 100%; padding: 12px; font-size: 15px; border: 1px solid #cbd5e1; border-radius: 6px; margin-bottom: 16px; }
        button { background: #0284c7; color: white; border: none; padding: 12px 20px; font-size: 16px; font-weight: 600; border-radius: 6px; cursor: pointer; width: 100%; transition: background 0.2s; }
        button:hover { background: #0369a1; }
        
        .resposta { line-height: 1.6; color: #1e293b; }
        .resposta ul, .resposta ol { padding-left: 20px; }
        .resposta code { background: #f1f5f9; padding: 2px 6px; border-radius: 4px; font-family: monospace; }
        
        .fuente { font-size: 0.9em; background: #f8fafc; padding: 12px; border-left: 4px solid #0284c7; margin-top: 10px; border-radius: 4px; border: 1px solid #e2e8f0; border-left-width: 4px; }
        .fuente-link { color: #0284c7; text-decoration: none; font-weight: 600; }
        .fuente-link:hover { text-decoration: underline; }
        
        /* Detalles técnicos da consulta (colapsable) */
        #detallesTecnicos { margin-top: 14px; }
        #detallesTecnicos details { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px; padding: 10px 12px; font-size: 0.85em; }
        #detallesTecnicos summary { cursor: pointer; color: #0284c7; font-weight: 600; }
        .tec-list { list-style: none; padding: 0; margin: 8px 0 0; }
        .tec-list li { padding: 3px 0; color: #475569; }
        .tec-list code { background: #f1f5f9; padding: 1px 5px; border-radius: 4px; }
        #detallesTecnicos .ctx-label { margin: 10px 0 0; font-weight: 600; color: #475569; }
        #detallesTecnicos .ctx-pre { background: #0f172a; color: #e2e8f0; padding: 10px; border-radius: 6px; overflow: auto; max-height: 260px; white-space: pre-wrap; word-break: break-word; font-size: 0.8em; margin: 6px 0 0; }
        
        .spinner { display: none; color: #64748b; font-style: italic; margin-top: 12px; text-align: center; }
    </style>
</head>
<body>
    <!-- Estrutura do Panel Lateral -->
    <div class="sidebar">
        <h2>📚 Base Documental</h2>
        <ul id="pdfList" class="pdf-list">
            <li><em>Cargando ficheiros...</em></li>
        </ul>
    </div>

    <!-- Estrutura do Contido Principal -->
    <div class="main-content">
        <h1>🏛️ Asistente de Regulamento Municipal</h1>
        
        <div class="box">
            <label for="modelSelect">Modelo LLM:</label>
			<select id="modelSelect" onchange="onModelChange()">
				<optgroup label="Cloud">
					<option value="gemma4:31b-cloud">Gemma4 31B-cloud (podemos quedar sen acceso por consumo excesivo)</option>
				</optgroup>
				<optgroup label="Local">
					<option value="qwen3.5:4b-mlx">Qwen 3.5 4B (Rápido e bo en galego)</option>
					<option value="qwen3.5:9b-mlx">Qwen 3.5 9B (Máis precisión)</option>
					<option value="llama3.2:3b">Llama 3.2 3B (Moi lixeiro)</option>
					<option value="gemma4:12b-mlx">Gemma 4 12B</option>
				</optgroup>
				<option value="__custom__">✏️ Outro modelo (escribilo manualmente)</option>
			</select>
			<input type="text" id="modelCustom" placeholder="Nome do modelo de Ollama (ex: qwen3.5:4b-mlx)" style="display:none;" onkeyup="actualizarEstadoRAM()">

            <label for="pregunta">Pregunta:</label>
            <input type="text" id="pregunta" placeholder="Fai unha pregunta sobre a normativa..." onkeypress="if(event.key==='Enter') consultar()">
            <button id="btnConsultar" onclick="consultar()">Consultar</button>
            <div id="spinner" class="spinner">🤖 Analizando documentos...</div>
        </div>

        <div id="resultado" class="box" style="display:none;">
            <h2>Resposta:</h2>
            <div id="respostaTexto" class="resposta"></div>
            
            <h3>Fontes consultadas:</h3>
            <div id="fontesLista"></div>

            <div id="detallesTecnicos"></div>
        </div>
    </div>

    <!-- Panel Lateral Dereito: estado dos modelos en Ollama -->
    <div class="sidebar sidebar-right">
        <h2>🤖 Modelos en Ollama</h2>
        <div id="modelStatus">__ESTADO_MODEOS__</div>
    </div>

    <script>
        const modelosRAM = __RAM_JSON__;

        async function cargarListaPDFs() {
            try {
                const res = await fetch('/docs/');
                const htmlText = await res.text();
                
                const parser = new DOMParser();
                const doc = parser.parseFromString(htmlText, 'text/html');
                const links = Array.from(doc.querySelectorAll('a'))
                    .map(a => a.getAttribute('href'))
                    .filter(href => href && href.toLowerCase().endsWith('.pdf'));

                const listEl = document.getElementById('pdfList');
                listEl.innerHTML = '';

                if (links.length === 0) {
                    listEl.innerHTML = '<li><em>Non hai PDFs no directorio</em></li>';
                    return;
                }

                links.forEach(pdf => {
                    const li = document.createElement('li');
                    const nomePropo = decodeURIComponent(pdf).replace(/^\//, ''); 
                    li.innerHTML = '<a href="/docs/' + pdf + '" target="_blank">📄 ' + nomePropo + '</a>';
                    listEl.appendChild(li);
                });
            } catch(e) {
                console.error("Erro cargando lista de PDFs", e);
                document.getElementById('pdfList').innerHTML = '<li><em>Erro cargando os documentos</em></li>';
            }
        }

        function onModelChange() {
            const sel = document.getElementById('modelSelect');
            const customInput = document.getElementById('modelCustom');
            if (sel.value === '__custom__') {
                customInput.style.display = 'block';
                customInput.focus();
            } else {
                customInput.style.display = 'none';
            }
            actualizarEstadoRAM();
        }

        function getModeloSeleccionado() {
            const sel = document.getElementById('modelSelect');
            return sel.value === '__custom__'
                ? document.getElementById('modelCustom').value.trim()
                : sel.value;
        }

        function setComboBlocked(on) {
            ['modelSelect', 'modelCustom'].forEach(id => {
                const el = document.getElementById(id);
                if (el) el.disabled = on;
            });
        }

        function actualizarEstadoRAM() {
            const modelo = getModeloSeleccionado();
            const st = document.getElementById('llmStatus');
            const btn = document.getElementById('ramBtn');
            const pregunta = document.getElementById('pregunta');
            const btnConsultar = document.getElementById('btnConsultar');
            if (!st || !btn || !pregunta || !btnConsultar) return;

            btn.innerHTML = '🧠 Forzar Carga en RAM (' + (modelo || '...') + ')';

            if (!modelo) {
                st.innerHTML = "<p class='warn'>✏️ Escribe o nome do modelo na opción 'Outro modelo'.</p>";
                btn.disabled = true;
                pregunta.disabled = true;
                btnConsultar.disabled = true;
                return;
            }

            // Cloud: por sufixo '-cloud' ou por estar no optgroup "Cloud" do select
            const selOpt = document.getElementById('modelSelect');
            const optEl = selOpt.querySelector('option[value="' + selOpt.value + '"]');
            const isCloud = modelo.includes('-cloud') || (optEl && optEl.parentElement && optEl.parentElement.label === 'Cloud');

            if (isCloud) {
                btn.innerHTML = '☁️ Modelo Cloud, sen carga en RAM';
                st.innerHTML = "<p class='ok'>Modelo Cloud: sen carga en RAM local.</p>";
                btn.disabled = true;
                pregunta.disabled = false;
                btnConsultar.disabled = false;
                return;
            }

            if (modelosRAM.includes(modelo)) {
                st.innerHTML = "<p class='ok'>✅ <code>" + modelo + "</code> en RAM (LLM)</p>"
                    + "<p class='ok'><b>Modelos activos en RAM</b><br>• bge-m3 (Embeddings)<br>• <code>" + modelo + "</code> (LLM)</p>"
                    + "<p class='ok'>✔️ Formula a túa pregunta sobre a biblioteca documental.</p>";
                btn.disabled = false;
                pregunta.disabled = false;
                btnConsultar.disabled = false;
            } else {
                st.innerHTML = "<p class='warn'>bge-m3 está en RAM. Falta cargar o LLM.</p>";
                btn.disabled = false;
                pregunta.disabled = true;
                btnConsultar.disabled = true;
            }
        }

        function escapeHtml(s) {
            return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
        }

        async function consultar() {
            const pregunta = document.getElementById('pregunta').value.trim();
            const sel = document.getElementById('modelSelect');
            const modelo = sel.value === '__custom__'
                ? document.getElementById('modelCustom').value.trim()
                : sel.value;
            if(!pregunta || !modelo) return;

            document.getElementById('spinner').style.display = 'block';
            document.getElementById('resultado').style.display = 'none';

            try {
                const res = await fetch('/api/query', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({
                        question: pregunta,
                        model: modelo
                    })
                });

                const data = await res.json();
                
                document.getElementById('respostaTexto').innerHTML = marked.parse(data.answer);
                
                const fontesDiv = document.getElementById('fontesLista');
                fontesDiv.innerHTML = '';
                
                data.sources.forEach((src, idx) => {
                    const d = document.createElement('div');
                    d.className = 'fuente';
                    
                    const pdfUrl = '/docs/' + encodeURIComponent(src.Source) + '#page=' + src.Page;
                    d.innerHTML = '<strong>[' + (idx + 1) + '] <a href="' + pdfUrl + '" target="_blank" class="fuente-link">📄 ' + src.Source + ' (Páxina ' + src.Page + ')</a></strong>';
                    fontesDiv.appendChild(d);
                });

                const meta = data.meta || {};
                let detHTML = '';
                if (meta.model) {
                    detHTML = '<details><summary>⚙️ Detalles técnicos</summary><ul class="tec-list">'
                        + '<li>🤖 Modelo empregado (LLM): <code>' + meta.model + '</code></li>'
                        + '<li>⏱️ Tempo total: <code>' + (meta.totalMs/1000).toFixed(2) + ' s</code> (avaliación: ' + (meta.evalMs/1000).toFixed(2) + ' s)</li>'
                        + '<li>📤 Tokens enviados: <code>' + (meta.promptEvalCount || 0) + '</code></li>'
                        + '<li>📥 Tokens recibidos: <code>' + (meta.evalCount || 0) + '</code></li>'
                        + '<li>📚 Contexto enviado: <code>' + (meta.contextoChars || 0) + ' caracteres</code> · ' + (meta.fontes || 0) + ' fontes</li>'
                        + '</ul>'
                        + '<p class="ctx-label">📄 Texto do contexto enviado:</p>'
                        + '<pre class="ctx-pre">' + escapeHtml(meta.contextoTexto || '') + '</pre>'
                        + '</details>';
                }
                document.getElementById('detallesTecnicos').innerHTML = detHTML;

                document.getElementById('resultado').style.display = 'block';
            } catch(e) {
                alert('Erro procesando a consulta');
            } finally {
                document.getElementById('spinner').style.display = 'none';
            }
        }

        async function precargarModelos() {
            const btn = document.getElementById('preloadBtn');
            const st = document.getElementById('preloadStatus');
            btn.disabled = true;
            st.style.display = 'block';
            st.textContent = '⬇️ Descargando modelos (isto pode tardar)...';
            try {
                const res = await fetch('/api/preload', { method: 'POST' });
                const data = await res.json();
                if (data.ok) {
                    st.textContent = '✅ ' + data.message;
                    setTimeout(() => location.reload(), 1500);
                } else {
                    st.textContent = '❌ ' + (data.error || 'Erro descoñecido');
                    btn.disabled = false;
                }
            } catch(e) {
                st.textContent = '❌ Erro conectando co servidor';
                btn.disabled = false;
            }
        }

        function setConsultable(on) {
            ['pregunta', 'modelSelect', 'modelCustom', 'btnConsultar', 'ramBtn', 'preloadBtn'].forEach(id => {
                const el = document.getElementById(id);
                if (el) el.disabled = !on;
            });
        }

        async function forzarCargaRAM() {
            const modelo = getModeloSeleccionado();
            if (!modelo) {
                alert('Escribe o nome do modelo na opción ✏️ Outro modelo');
                return;
            }

            const st = document.getElementById('preloadStatus');
            st.style.display = 'block';
            setComboBlocked(true);
            document.getElementById('ramBtn').disabled = true;
            document.getElementById('pregunta').disabled = true;
            document.getElementById('btnConsultar').disabled = true;
            st.innerHTML = '<b>Proceso de carga de modelos na RAM...</b><br>📌 Paso 1/2: Cargando modelo de embeddings <code>bge-m3</code> na RAM...<br>📌 Paso 2/2: Cargando modelo de linguaxe <code>' + modelo + '</code> na RAM...';

            try {
                const res = await fetch('/api/load-ram', {
                    method: 'POST',
                    headers: {'Content-Type': 'application/json'},
                    body: JSON.stringify({model: modelo})
                });
                const data = await res.json();
                if (data.ok) {
                    if (!modelosRAM.includes('bge-m3:latest')) modelosRAM.push('bge-m3:latest');
                    if (!modelosRAM.includes(modelo)) modelosRAM.push(modelo);
                    st.innerHTML = '<b>Modelos activos en RAM</b><br>• bge-m3 (Embeddings)<br>• <code>' + modelo + '</code> (LLM)';
                } else {
                    st.innerHTML = '❌ ' + (data.error || 'Erro descoñecido');
                }
            } catch(e) {
                st.innerHTML = '❌ Erro conectando co servidor: ' + e.message;
            } finally {
                setComboBlocked(false);
                actualizarEstadoRAM();
            }
        }

        // Carga inicial dos ficheiros no panel lateral
        cargarListaPDFs();
        actualizarEstadoRAM();
    </script>
</body>
</html>`
	html = strings.Replace(html, "__ESTADO_MODEOS__", htmlEstadoModelos(), 1)
	ramKeys := make([]string, 0, len(ramModelos))
	for m := range ramModelos {
		ramKeys = append(ramKeys, m)
	}
	ramJSON, _ := json.Marshal(ramKeys)
	html = strings.Replace(html, "__RAM_JSON__", string(ramJSON), 1)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func indexarPDFs() {
	var baseDeDatos []Chunk
	archivos, err := filepath.Glob("*.pdf")
	if err != nil || len(archivos) == 0 {
		log.Fatal("Non se atoparon PDFs no directorio actual")
	}

	for _, archivo := range archivos {
		fmt.Printf("Lendo: %s\n", archivo)
		paginas, err := extraerTextoPDF(archivo)
		if err != nil {
			log.Printf("Erro lendo %s: %v", archivo, err)
			continue
		}

		for numPag, texto := range paginas {
			textoLimpo := strings.TrimSpace(texto)
			if len(textoLimpo) < 50 {
				continue
			}

			pedazos := facerChunks(textoLimpo, ChunkSize, ChunkOverlap)
			for _, pedazo := range pedazos {
				vector := pedirEmbeddingOllama(pedazo)
				baseDeDatos = append(baseDeDatos, Chunk{
					Text:   pedazo,
					Source: archivo,
					Page:   numPag,
					Vector: vector,
				})
			}
		}
	}

	file, err := os.Create(IndexFile)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()
	gob.NewEncoder(file).Encode(baseDeDatos)
	fmt.Printf("✅ Indexación completada. Gardados %d chunks en %s\n", len(baseDeDatos), IndexFile)
}

func extraerTextoPDF(ruta string) (map[int]string, error) {
	f, r, err := pdf.Open(ruta)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	paginas := make(map[int]string)
	numPages := r.NumPage()

	for i := 1; i <= numPages; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		textoPagina, _ := p.GetPlainText(nil)
		paginas[i] = textoPagina
	}
	return paginas, nil
}

func facerChunks(texto string, size, overlap int) []string {
	runas := []rune(texto)
	var chunks []string
	for i := 0; i < len(runas); i += (size - overlap) {
		end := i + size
		if end > len(runas) {
			end = len(runas)
		}
		chunks = append(chunks, string(runas[i:end]))
		if end == len(runas) {
			break
		}
	}
	return chunks
}

func similitudeCoseno(a, b []float64) float64 {
	var dotProduct, normA, normB float64
	for i := 0; i < len(a); i++ {
		dotProduct += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

func pedirEmbeddingOllama(texto string) []float64 {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model":  EmbeddingModel,
		"prompt": texto,
	})

	resp, err := http.Post(OllamaURL+"/api/embeddings", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Fatal("Erro conectando con Ollama: ", err)
	}
	defer resp.Body.Close()

	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Embedding
}

func pedirChatOllama(systemPrompt, userPrompt, modelName string) (string, ChatMeta) {
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": modelName, // Usa o modelo pasado por parámetro
		"messages": []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		"think":  false,
		"stream": false,
	})

	resp, err := http.Post(OllamaURL+"/api/chat", "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		log.Fatal("Erro conectando con Ollama: ", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Erro na API de Ollama (Status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Model           string `json:"model"`
		TotalDuration   int64  `json:"total_duration"`
		EvalDuration    int64  `json:"eval_duration"`
		PromptEvalCount int    `json:"prompt_eval_count"`
		EvalCount       int    `json:"eval_count"`
		Message         struct {
			Content string `json:"content"`
		} `json:"message"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		log.Fatal("Erro descodificando a resposta de Ollama: ", err)
	}

	meta := ChatMeta{
		Model:         result.Model,
		TotalMs:       result.TotalDuration / int64(time.Millisecond),
		EvalMs:        result.EvalDuration / int64(time.Millisecond),
		PromptEval:    result.PromptEvalCount,
		EvalCount:     result.EvalCount,
		Contexto:      utf8.RuneCountInString(systemPrompt) + utf8.RuneCountInString(userPrompt),
		ContextoTexto: systemPrompt + "\n" + userPrompt,
	}
	if meta.Model == "" {
		meta.Model = modelName
	}

	return result.Message.Content, meta
}
