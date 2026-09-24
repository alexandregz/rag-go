# pdfbot — Asistente RAG do Regulamento Municipal de Ames

Aplicación en **Go** que fai *Retrieval-Augmented Generation* (RAG) sobre os ficheiros **PDF** que se atopen no mesmo directorio. Permite facer preguntas en linguaxe natural sobre a normativa municipal, tanto desde a **liña de comandos** (consola) como desde un **entorno web** sinxelo de autoservizo.

Usa **Ollama** en local como motor de modelos:

- **Embeddings**: `bge-m3:latest` (indexa e busca por similitude coseno).
- **Chat**: varios LLM predefinidos (locais e en nube), ou calquera modelo que se queira escribir manualmente.

---

## Características

- 📄 **Indexación** de todos os `.pdf` do directorio actual en pedazos (`chunk`) solapados, con busca semántica por similitude coseno.
- 💬 **Consola**: facer preguntas directamente por terminal.
- 🌐 **Servidor web**: formulario sinxelo con panel lateral que lista os PDFs e respostas formateadas (Markdown) coas fontes consultadas.
- 🧠 **Modelo configurábel**:
  - Por liña de comandos co flag `-model`.
  - Desde o formulario web, seleccionando un predefinido ou escribindo calquera outro modelo de Ollama na última opción.
- 🐳 **Dockerizado**: porto e interface de rede configurábeis para lanzar en contedores.
- 🧠 **Xestión de modelos desde o panel lateral**: ao arrincar, `bge-m3` (embeddings) cárgase automaticamente na RAM de Ollama se non está xa (`keep_alive: -1`). O modelo de chat local seleccionado deixa o input de pregunta **bloqueado ata que se carga en RAM** mediante o botón **Forzar Carga en RAM** (desactivado para modelos `-cloud`, que non precisan carga local). Ao rematar, móstrase a lista de modelos activos en RAM. Se falta algún modelo local descargado, aparece outro botón para descargalo (equivalente a `ollama pull`).
- 📊 **Detalles técnicos da consulta**: na web, baixo a resposta, un bloque colapsable (pechado por defecto) co modelo empregado, tempo total/avaliación, tokens enviados e recibidos, os caracteres/fontes do contexto e o **texto íntegro enviado ao LLM**; na consola, unha liña resumo ao final.
- 🗒️ **Rexistro de consultas**: cada consulta (web e consola) engade unha entrada en `logs/consultas_AAAA-MM-DD.log` (JSON por liña) coas métricas técnicas e a pregunta — **sen** o texto do contexto.

---

## Requisitos

- [Go](https://go.dev/dl/) ≥ 1.27 (versión declarada en `go.mod`).
- [Ollama](https://ollama.com/) funcionando en local (ou noutra máquina, véxase `OLLAMA_URL`).
- O modelo de embeddings `bge-m3:latest` descargado en Ollama:

  ```bash
  ollama pull bge-m3:latest
  ```

- Descargar os modelos de chat que se queiran usar, por exemplo:

  ```bash
  ollama pull qwen3.5:4b-mlx
  ollama pull llama3.2:3b
  ```

> ⚠️ Os PDFs que se queren consultar teñen que estar no **mesmo directorio** que o binario.

---

## Instalación

```bash
git clone <URL_DO_REPO>
cd rag-go
go build -o pdfbot .
```

---

## Uso

### 1. Indexar os PDFs (obrigatorio a primeira vez)

Crea o índice vectorial `db_vectores.gob` a partir dos `.pdf` do directorio.

```bash
./pdfbot -index
```

> Necesita que Ollama teña o modelo `bge-m3:latest` para xerar os embeddings.

### 2. Consultar desde consola

```bash
./pdfbot -q "En que artigo se regula a participación cidadá?"
```

Escollendo un modelo distinto do predeterminado:

```bash
./pdfbot -q "Que obrigas ten Ames Radio?" -model "qwen3.5:4b-mlx"
```

### 3. Servidor web

```bash
./pdfbot -web
```

Ábrese en <http://localhost:8987>. Para cambiar porto e interface:

```bash
# Bindear só no loopback e no porto 9000
./pdfbot -web -host 127.0.0.1 -port 9000

# Bindear en todas as interfaces (ex: 0.0.0.0), porto 8987
./pdfbot -web -host 0.0.0.0 -port 8987
```

---

## Configuración por variables de contorno

As opcións da liña de comandos pódense sobrescribir/establecer tamén coas seguintes variables de contorno (útil en Docker):

| Variable   | Descrición                                                        | Por defecto              |
|------------|-------------------------------------------------------------------|--------------------------|
| `PORT`     | Porto do servidor web                                             | `8987`                   |
| `HOST`     | Interface de rede do servidor web (baleiro = todas)               | *(baleiro)*              |
| `OLLAMA_URL` | Endpoint da API de Ollama                                        | `http://localhost:11434` |

---

## Docker

### Construír a imaxe

```bash
docker build -t pdfbot .
```

### Lanzar o servidor web (porto e interface axustábeis)

Con `HOST` a `0.0.0.0`, porto `8987` (ou calquera outro coa variable `PORT`):

```bash
docker run --rm \
  -e HOST=0.0.0.0 \
  -p 8987:8987 \
  -e PORT=8987 \
  -e OLLAMA_URL=http://host.docker.internal:11434 \
  pdfbot
```

Se Ollama corre na máquina host, `host.docker.internal` resolvelo en macOS/Windows; en **Linux** usa `--network host` ou a IP do host:

```bash
docker run --rm --network host \
  -e HOST=0.0.0.0 \
  -e PORT=8987 \
  pdfbot
```

> O contedor arranca en modo web automaticamente. Se o índice `db_vectores.gob` non está incluído, o contedor executa a indexación (necesita `bge-m3:latest` accesible) antes de servir.

### Corpus persistente: montar `/data` (recomendado)

O contedor traballa sobre `/data`, que concentra **corpus documental (PDFs) + índice (`db_vectores.gob`) + `logs/`**. Montar un **bind mount** a un directorio local permite actualizar PDFs, rexenerar o índice e revisar logs **sen reconstruír a imaxe nin entrar no contedor**:

```bash
# Preparar o directorio de datos no host (só a primeira vez)
mkdir -p data
cp *.pdf data/

# Arrancar co volume montado
docker run --rm \
  -v "$(pwd)/data:/data" \
  -e HOST=0.0.0.0 \
  -p 8987:8987 \
  -e PORT=8987 \
  -e OLLAMA_URL=http://host.docker.internal:11434 \
  pdfbot
```

Actualizar a normativa a partir de agora resúmese en:

```bash
cp novo_regulamento.pdf data/   # e reiniciar o contedor se o índice quedou obsoleto
```

Para forzar a reindexación, borra o índice e reinicia (volverase a xerar ao arrincar):

```bash
rm data/db_vectores.gob
# docker restart <contedor>  ou  docker run de novo (se lanzaches con --rm)
```

Se prefires que Docker xestione o almacenamento (sen directorio no host), usa un volume nomeado; na primeira creación cópiase o estado inicial da imaxe:

```bash
docker run --rm -v pdfbot_data:/data \
  -e HOST=0.0.0.0 -p 8987:8987 \
  -e PORT=8987 \
  -e OLLAMA_URL=http://host.docker.internal:11434 \
  pdfbot
```

> 📦 Desde fóra do contedor tócase só o que hai no volume: non fai falta `docker cp` nin entrar nel.

---

## Estrutura do proxecto

```
rag-go/
├── main.go             # Código principal (indexación, consola, servidor web)
├── go.mod / go.sum     # Módulo e dependencias Go
├── Dockerfile          # Imaxe do contedor
├── docker-entrypoint.sh
├── README.md
├── *.pdf               # Corpus documental (normativa municipal)
├── data/               # Recomendado: corpus + índice + logs persistidos (montado en /data no contedor)
└── logs/               # Xerado: rexistro diario das consultas (JSON por liña)
```

> `logs/` xérase automaticamente; recoméndase excluílo no control de versións.

O ficheiro `db_vectores.gob` é o **índice xerado** por `-index`; recréase tantas veces como sexa necesario.

---

## Notas técnicas

- A busca usa **similitude coseno** entre o embedding da pregunta e os vectores de cada pedazo, e toma os 3 pedazos máis similares como contexto.
- O `system prompt` pídelle ao modelo responder en **galego** e só co contexto proporcionado.
- Os modelos etiquetados `-cloud` (ex: `gemma4:31b-cloud`) consomen un servizo remoto e poden quedar sen acceso por consumo excesivo.

---

## Licenza

*Por determinar.*