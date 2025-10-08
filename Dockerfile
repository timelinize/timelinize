# Use Alpine Linux as the base image
FROM alpine:3.20 AS builder

# Install necessary build dependencies
RUN apk add --no-cache \
    build-base \
    git \
    go \
    vips-dev \
    ffmpeg-dev \
    libheif-dev \
    bash \
    curl

# Set the working directory inside the container
WORKDIR /app
COPY . .

# Enable CGO for Go packages using C libraries (e.g. libvips)
ENV CGO_ENABLED=1

# Use Go module and build cache mounts for speed
RUN go env -w GOCACHE=/go/cache
RUN go env -w GOMODCACHE=/go/modcache
RUN --mount=type=cache,target=/go/modcache go mod download
RUN --mount=type=cache,target=/go/modcache --mount=type=cache,target=/go/cache go build -o /app/timelinize

# Final stage: minimal runtime image
FROM alpine:3.20 AS final

WORKDIR /app

# Install only runtime dependencies (no compilers)
RUN apk add --no-cache \
    vips \
    ffmpeg \
    libheif \
    bash \
    shadow

# Create non-root user and required directories
RUN useradd -u 1000 -m -s /bin/bash -d /app timelinize
RUN mkdir -p /app/.config/timelinize /repo
RUN chown -R timelinize /app /repo

# Copy built binary
COPY --from=builder /app/timelinize /app/timelinize

# Configure runtime environment
ENV TLZ_ADMIN_ADDR="0.0.0.0:12002"
EXPOSE 12002

VOLUME /app/.config/timelinize
VOLUME /repo

USER timelinize

CMD ["/app/timelinize", "serve"]