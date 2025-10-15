# ---- Builder ----
FROM golang:1.25-trixie AS builder

RUN sh -c "echo 'deb http://deb.debian.org/debian trixie-backports main contrib non-free non-free-firmware' | tee -a /etc/apt/sources.list.d/backports.list"

# Install build dependencies for libvips and Go modules
RUN apt-get update && apt-get install -y --no-install-recommends -t trixie-backports \
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
    libheif-plugins-all \
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
FROM debian:trixie-slim AS final

COPY --from=builder /etc/apt/sources.list.d/backports.list /etc/apt/sources.list.d/backports.list

# Install runtime dependencies
RUN apt-get update && apt-get install -y --no-install-recommends -t trixie-backports \
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
    libheif-plugins-all \
    ffmpeg \
    bash \
    curl \
    ca-certificates
RUN rm -rf /var/lib/apt/lists/*

# Pin uv version, so the runtime environment is more predictable
ENV UV_VERSION="0.9.2"
RUN sh -c 'curl -LsSf https://astral.sh/uv/$UV_VERSION/install.sh | env UV_INSTALL_DIR="/usr/local/bin" sh'

# Copy libvips libraries from builder stage
COPY --from=builder /usr/lib/ /usr/lib/
COPY --from=builder /usr/include/ /usr/include/
COPY --from=builder /usr/lib/pkgconfig/ /usr/lib/pkgconfig/
RUN ldconfig

# Set working directory
WORKDIR /app

# Create non-root user and directories
RUN useradd -u 1000 -m -s /bin/bash -d /app timelinize && \
    mkdir -p /app/.config/timelinize /repo /app/.cache /app/.local && \
    chown -R timelinize /app /repo /app/.cache /app/.local

# Copy built binary
COPY --from=builder /app/timelinize /app/timelinize

# Runtime config
ENV TLZ_ADMIN_ADDR="0.0.0.0:12002"
EXPOSE 12002

# Timelinize configuration
VOLUME /app/.config/timelinize
VOLUME /repo
# Expose the user cache directories, where python (uv) dependencies, models
# and virtual envs are cached.
#
# Some dependencies and embedding models are quite big (several GB), so this prevents
# dependencies to be downloaded every time we start a new container,
# as long as we re-use the cache volumes.
#
# Example:
#
# docker run -v timelinize-cache:/app/.cache \
#            -v timelinize-local:/app/.local \
#            -v timelinize-repo:/repo \
#            -v timelinize-config:/app/.config/timelinize \
#            -p 12001:12002 timelinize
VOLUME /app/.cache
VOLUME /app/.local

USER timelinize
CMD ["/app/timelinize", "serve"]
