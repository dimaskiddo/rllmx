# Builder Image
# ---------------------------------------------------
FROM golang:1.26-alpine AS go-builder

ARG VERSION=dev
ARG COMMIT=none

# A dependency that needs a newer Go must fail the build, not fetch a toolchain.
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


# Final Image
# ---------------------------------------------------
FROM dimaskiddo/alpine:base
LABEL maintainer="Dimas Restu Hidayanto <drh.dimasrestu@gmail.com>"

ARG SERVICE_NAME="rllmx"

ENV PATH=$PATH:/usr/local/${SERVICE_NAME} \
    APP_ENV=production \
    APP_HOST=0.0.0.0 \
    APP_PORT=20128

WORKDIR /usr/local/${SERVICE_NAME}

RUN apk --no-cache --update upgrade \
    && addgroup -g 1000 ${SERVICE_NAME} \
    && adduser -D -u 1000 -G ${SERVICE_NAME} ${SERVICE_NAME} \
    && mkdir -p \
        storage/app \
        storage/logs \
    && chown -R ${SERVICE_NAME}:${SERVICE_NAME} .

COPY --from=go-builder /usr/src/app/main ./rllmx
COPY --from=go-builder /usr/src/app/.env.example ./.env

USER ${SERVICE_NAME}

EXPOSE 20128 1455

VOLUME ["/usr/local/rllmx/storage"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["rllmx", "artisan", "health"]

CMD ["rllmx"]
