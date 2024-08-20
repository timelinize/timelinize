# Use Arch Linux as the base image
FROM archlinux:latest AS builder

# Install necessary dependencies
RUN pacman -Syu --noconfirm \
    base-devel \
    git \
    go \
    libvips \
    ffmpeg \
    libheif

# Set the working directory inside the container
WORKDIR /app
COPY . .

ENV CGO_ENABLED=1
RUN go env -w GOCACHE=/go/cache
RUN go env -w GOMODCACHE=/go/modcache
RUN --mount=type=cache,target=/go/modcache go mod download
RUN --mount=type=cache,target=/go/modcache --mount=type=cache,target=/go/cache go build -o /app/timelinize

FROM archlinux:latest AS final

WORKDIR /app

RUN pacman -Syu --noconfirm \
    libvips \
    ffmpeg \
    libheif

RUN useradd -u 1000 -m -s /bin/bash -d /app timelinize
RUN mkdir -p /app/.config/timelinize /repo
RUN chown -R timelinize /app
RUN chown -R timelinize /repo

COPY --from=builder /app/timelinize /app/timelinize

ENV TLZ_ADMIN_ADDR="0.0.0.0:12002"
EXPOSE 12002

VOLUME /app/.config/timelinize
VOLUME /repo
USER timelinize

CMD ["/app/timelinize", "serve"]
