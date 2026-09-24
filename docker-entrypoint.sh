#!/bin/sh
set -e

# O corpus documental, o índice e os logs viven en DATA_DIR (por defecto /data, volume montado).
# Exemplo: docker run -v "$(pwd)/data:/data" ...
DATA_DIR="${DATA_DIR:-/data}"

# Diríxese a Ollama segundo a variable de contorno (OLLAMA_URL)
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