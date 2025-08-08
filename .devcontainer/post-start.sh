#!/bin/sh

export TLZ_ADMIN_ADDR="0.0.0.0:12002"
export CGO_ENABLED=1

air \
    --build.cmd "go build -o /tmp/timelinize" \
    --build.bin "/tmp/timelinize" \
    --build.delay "100" \
    --build.include_ext "go" \
    --build.stop_on_error "false" \
    --misc.clean_on_exit true