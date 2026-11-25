# Builder Image
# ---------------------------------------------------
FROM golang:1.26-alpine AS go-builder

ARG VERSION=dev
ARG COMMIT=none

ENV GOTOOLCHAIN=local

WORKDIR /usr/src/app

COPY . ./

RUN go mod download \
    && CGO_ENABLED=0 \
        GOOS=linux \
        go build \
          -tags nomsgpack \
          -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
          -trimpath -o main .

RUN mkdir -p \
      /out/storage/app \
      /out/storage/logs \
    && chown -R 65532:65532 /out


# Final Image
# ---------------------------------------------------
FROM cgr.dev/chainguard/static:latest
LABEL maintainer="Dimas Restu H <drh.dimasrestu@gmail.com>"

ARG SERVICE_NAME="rllmx"

ENV PATH=/usr/local/${SERVICE_NAME}:${PATH} \
    APP_ENV=production \
    APP_HOST=0.0.0.0 \
    APP_PORT=20128

WORKDIR /usr/local/${SERVICE_NAME}

COPY --from=go-builder --chown=65532:65532 /out/storage ./storage
COPY --from=go-builder --chown=65532:65532 /usr/src/app/main ./rllmx
COPY --from=go-builder --chown=65532:65532 /usr/src/app/.env.example ./.env

USER 65532:65532

EXPOSE 20128 1455

VOLUME ["/usr/local/rllmx/storage"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["/usr/local/rllmx/rllmx", "artisan", "health"]

CMD ["/usr/local/rllmx/rllmx"]
