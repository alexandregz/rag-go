# ---------- Stage de construción ----------
FROM golang:1.27-alpine AS builder
WORKDIR /build

# Descarga dependencias primeiro para aproveitar a caché de layers
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/pdfbot .

# ---------- Stage de execución ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata

# /data = corpus documental + índice + logs (estado persistente).
# Montar aquí un volume ou bind mount para actualizar PDFs sen reconstruír a imaxe:
#   docker run -v "$(pwd)/data:/data" ...
WORKDIR /data

# Binario e corpus documental + índice xa xerado (se existe).
# Cópiase a /data como estado inicial; cun volume montado, o cwd do proceso é o volume.
COPY --from=builder /out/pdfbot /usr/local/bin/pdfbot
COPY *.pdf /data/
COPY db_vectores.gob /data/

# O corpus, o índice e os logs deben persistir fóra do ciclo de vida do contedor.
VOLUME /data

# Porto e interface de rede configurables (ver main.go: -host / -port)
ENV PORT=8080 \
    HOST=0.0.0.0 \
    OLLAMA_URL=http://host.docker.internal:11434

COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://localhost:${PORT}/ || exit 1

ENTRYPOINT ["/docker-entrypoint.sh"]