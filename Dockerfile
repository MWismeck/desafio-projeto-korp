# syntax=docker/dockerfile:1.7
# Dockerfile — http-server-projeto-korp.
# Multi-stage: build estático em golang:1.25 → runtime alpine:3 non-root (< 20 MB, COM shell).
# Base Alpine em vez de distroless para permitir diagnóstico em campo
# (docker exec -it ... sh; apk add --no-cache curl tcpdump) sem rebuild. Mitigações obrigatórias:
# usuário não-root, base pinada por digest, apk --no-cache e `trivy image` no CI.

ARG GO_VERSION=1.25
# digest de 2026-09-07 (docker buildx imagetools inspect golang:1.25); a tag fica antes do @ para leitura
FROM golang:${GO_VERSION}@sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80 AS build

WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOFLAGS=-mod=readonly

# dependências primeiro (cache de camada)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
    -o /out/http-server-projeto-korp ./cmd/http-server-projeto-korp

# runtime: alpine (BusyBox sh + apk para diagnóstico em campo), usuário nonroot (UID 65532)
FROM alpine:3.20@sha256:d9e853e87e55526f6b2917df91a2115c36dd7c696a35be12163d44e6e2a4b6bc

# ca-certificates e tzdata não vêm na base alpine (vinham prontos no distroless static)
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 65532 nonroot

ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="http-server-projeto-korp" \
      org.opencontainers.image.description="Serviço HTTP do Desafio DevOps Projeto Korp (GET /projeto-korp)" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="https://github.com/MWismeck/desafio-projeto-korp" \
      org.opencontainers.image.licenses="MIT"

COPY --from=build /out/http-server-projeto-korp /http-server-projeto-korp

# UID numerico (DL3066): resolvivel pelo host e compativel com runAsNonRoot do Kubernetes
USER 65532:65532
ENV APP_PORT=8080
EXPOSE 8080
# A sonda é o próprio binário: `-healthcheck` lê o mesmo APP_PORT que o servidor escuta, então mudar
# a porta não deixa a sonda apontando para a errada. Forma exec, igual à do compose, sem depender de shell.
HEALTHCHECK --interval=10s --timeout=2s --start-period=3s --retries=3 \
  CMD ["/http-server-projeto-korp", "-healthcheck"]
ENTRYPOINT ["/http-server-projeto-korp"]
