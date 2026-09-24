#!/bin/sh
set -e

# O corpus documental, o índice e os logs viven en /data (volume montado).
# Exemplo: docker run -v "$(pwd)/data:/data" ...
cd /data

# Diríxese a Ollama segundo a variable de contorno (OLLAMA_URL)
echo "🔎 Ollama en: ${OLLAMA_URL}"

# Se non hai índice xerado, créao a partir dos PDFs.
# Requírese que bge-m3:latest estea dispoñible en Ollama.
if [ ! -f db_vectores.gob ]; then
  echo "📦 Índice non atopado. Xerando a partir dos PDFs do directorio..."
  pdfbot -index
fi

# Lanza o servidor web co porto e a interface configurados.
echo "🚀 Arrancando servidor web en http://${HOST}:${PORT}"
exec pdfbot -web -host "${HOST}" -port "${PORT}"