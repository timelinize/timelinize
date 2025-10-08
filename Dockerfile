# ---- Builder ----
FROM golang:1.25-alpine AS builder

RUN apk add --no-cache \
    build-base \
    vips-dev \
    ffmpeg-dev \
    libheif-dev \
    bash \
    git \
    curl

WORKDIR /app
COPY . .

ENV CGO_ENABLED=1
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go mod download && \
    go build -o /app/timelinize



# ---- Runtime ----
FROM alpine:3.20 AS final

RUN apk add --no-cache vips ffmpeg libheif bash shadow
WORKDIR /app

RUN useradd -u 1000 -m -s /bin/bash -d /app timelinize
RUN mkdir -p /app/.config/timelinize /repo
RUN chown -R timelinize /app /repo

COPY --from=builder /app/timelinize /app/timelinize

ENV TLZ_ADMIN_ADDR="0.0.0.0:12002"
EXPOSE 12002

VOLUME /app/.config/timelinize
VOLUME /repo

USER timelinize

CMD ["/app/timelinize", "serve"]