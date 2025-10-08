# ---- Builder ----
FROM golang:1.25-bullseye AS builder

# Install build dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    git \
    curl \
    libvips-dev \
    libheif-dev \
    ffmpeg \
    bash \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY . .

# Enable CGO for Go packages using C libraries (libvips, sqlite-vec)
ENV CGO_ENABLED=1

# Use Go module and build cache mounts
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download && \
    go build -o /app/timelinize



# ---- Runtime ----
FROM debian:bullseye-slim AS final

WORKDIR /app

# Install only runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    libvips-dev \
    libheif-dev \
    ffmpeg \
    bash \
    && rm -rf /var/lib/apt/lists/*

# Create non-root user and directories
RUN useradd -u 1000 -m -s /bin/bash -d /app timelinize
RUN mkdir -p /app/.config/timelinize /repo
RUN chown -R timelinize /app /repo

# Copy built binary
COPY --from=builder /app/timelinize /app/timelinize

# Runtime config
ENV TLZ_ADMIN_ADDR="0.0.0.0:12002"
EXPOSE 12002

VOLUME /app/.config/timelinize
VOLUME /repo

USER timelinize
CMD ["/app/timelinize", "serve"]