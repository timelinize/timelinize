#!/bin/sh

export DEBIAN_FRONTEND=noninteractive
# install system dependencies
echo "deb http://deb.debian.org/debian bookworm-backports main" | sudo tee -a /etc/apt/sources.list
sudo apt-get update -t bookworm-backports && \
    sudo apt-get install -t bookworm-backports -y --no-install-recommends \
        git \
        curl \
        build-essential \
        libvips-dev \
        software-properties-common \
        libsqlite3-dev \
        ffmpeg \
        ca-certificates \
        libheif-dev \
        libheif1 \
        cmake pkg-config libgoogle-perftools-dev \
	libsqlite3-dev && \
    sudo rm -rf /var/lib/apt/lists/*

# install uv
curl -LsSf https://astral.sh/uv/install.sh | sh

# cache Go deps
go install github.com/air-verse/air@latest
go mod tidy

# install sentencepiece, Debian bookworm version is < 0.2
cd /tmp
git clone https://github.com/google/sentencepiece.git
cd sentencepiece
mkdir build
cd build
cmake .. -DSPM_ENABLE_SHARED=OFF -DCMAKE_INSTALL_PREFIX=./root
make install