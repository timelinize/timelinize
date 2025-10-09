# ---- Builder ----
FROM golang:1.25-bookworm AS builder

# Install build dependencies for libvips and Go modules
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential \
    git \
    curl \
    bash \
    meson \
    ninja-build \
    pkg-config \
    libglib2.0-dev \
    libexpat1-dev \
    libjpeg-dev \
    libpng-dev \
    libtiff-dev \
    libgif-dev \
    libwebp-dev \
    liborc-0.4-dev \
    libgsf-1-dev \
    libheif-dev \
    libsqlite3-dev \
    ffmpeg \
    ca-certificates
RUN rm -rf /var/lib/apt/lists/*

# Build latest libvips from source with caching
RUN --mount=type=cache,target=/tmp/libvips-cache \
    git clone --depth 1 https://github.com/libvips/libvips.git /tmp/libvips && \
    cd /tmp/libvips && \
    meson setup build --buildtype=release --wrap-mode=forcefallback --backend=ninja -Dprefix=/usr -Dlibdir=/usr/lib && \
    ninja -C build && \
    ninja -C build install && \
    rm -rf /tmp/libvips && \
    ldconfig

# Set working directory and copy app
WORKDIR /app
COPY . .

ENV CGO_ENABLED=1

# Use Go module and build cache mounts
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download && \
    go build -o /app/timelinize

# ---- Runtime ----
FROM debian:bookworm-slim AS final

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    libglib2.0-0 \
    libexpat1 \
    libjpeg62-turbo \
    libpng16-16 \
    libtiff6 \
    libgif7 \
    libwebp7 \
    liborc-0.4-0 \
    libgsf-1-114 \
    libheif1 \
    ffmpeg \
    bash \
    ca-certificates
RUN rm -rf /var/lib/apt/lists/*

# Copy libvips libraries from builder stage
COPY --from=builder /usr/lib/ /usr/lib/
COPY --from=builder /usr/include/ /usr/include/
COPY --from=builder /usr/lib/pkgconfig/ /usr/lib/pkgconfig/
RUN ldconfig

# Set working directory
WORKDIR /app

# Create non-root user and directories
RUN useradd -u 1000 -m -s /bin/bash -d /app timelinize && \
    mkdir -p /app/.config/timelinize /repo && \
    chown -R timelinize /app /repo

# Copy built binary
COPY --from=builder /app/timelinize /app/timelinize

# Runtime config
ENV TLZ_ADMIN_ADDR="0.0.0.0:12002"
EXPOSE 12002

VOLUME /app/.config/timelinize
VOLUME /repo

USER timelinize
CMD ["/app/timelinize", "serve"]