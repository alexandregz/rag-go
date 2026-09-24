#!/bin/sh
set -e

# O corpus documental, o índice e os logs viven en DATA_DIR (por defecto /data, volume montado).
# Exemplo: docker run -v "$(pwd)/data:/data" ...
DATA_DIR="${DATA_DIR:-/data}"

# Se non se indicou OLLAMA_URL, autodetecta o host de Ollama: proba os alias
# de Docker Desktop e de Lima/Container/Colima e, en último caso, o gateway.
if [ -z "${OLLAMA_URL}" ]; then
  for host in host.docker.internal host.lima.internal 192.168.64.1; do
    if wget -q -T 2 -O /dev/null "http://${host}:11434/api/version" 2>/dev/null; then
      OLLAMA_URL="http://${host}:11434"
      break
    fi
  done
  OLLAMA_URL="${OLLAMA_URL:-http://localhost:11434}"
fi

echo "🔎 Ollama en: ${OLLAMA_URL}"
echo "📁 Datos en: ${DATA_DIR}"

# Se non hai índice xerado, créao a partir dos PDFs.
# Requírese que bge-m3:latest estea dispoñible en Ollama.
if [ ! -f "${DATA_DIR}/db_vectores.gob" ]; then
  echo "📦 Índice non atopado. Xerando a partir dos PDFs do directorio..."
  pdfbot -index -data "${DATA_DIR}"
fi

# Lanza o servidor web co porto e a interface configurados.
echo "🚀 Arrancando servidor web en http://${HOST}:${PORT}"
exec pdfbot -web -host "${HOST}" -port "${PORT}" -data "${DATA_DIR}"