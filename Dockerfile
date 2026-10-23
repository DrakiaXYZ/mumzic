# BUILD STAGE
FROM golang:1.25-alpine AS builder

WORKDIR /app

RUN apk update && apk upgrade --no-cache

RUN apk add --no-cache \
    opus-dev \
    opusfile-dev \
    pkgconfig \
    build-base

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o mumzic


# RUN STAGE

FROM alpine:latest

WORKDIR /app

# ffmpeg is used by gumbleffmpeg for playback, deno is the JS runtime yt-dlp needs for YouTube challenges
RUN apk add --no-cache \
    ffmpeg \
    deno \
    opusfile-dev

# Grab the latest yt-dlp release from GitHub (the apk package often lags behind). The standalone musl binary
# bundles Python and yt-dlp-ejs. The checksum file changes with every release, so ADD invalidates the
# build cache whenever a new version is out, and it's used to verify the download.
ADD https://github.com/yt-dlp/yt-dlp/releases/latest/download/SHA2-256SUMS /tmp/yt-dlp-SHA2-256SUMS
RUN case "$(uname -m)" in \
        x86_64) asset=yt-dlp_musllinux ;; \
        aarch64) asset=yt-dlp_musllinux_aarch64 ;; \
        *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;; \
    esac \
    && cd /tmp \
    && wget -q "https://github.com/yt-dlp/yt-dlp/releases/latest/download/${asset}" \
    && grep " ${asset}\$" yt-dlp-SHA2-256SUMS | sha256sum -c - \
    && install -m 755 "${asset}" /usr/local/bin/yt-dlp \
    && rm -f "${asset}" yt-dlp-SHA2-256SUMS \
    && yt-dlp --version

COPY --from=builder /app/mumzic .
# whitelist.txt is optional, built-in defaults are used without it (the pattern also matches whitelist-example.txt so it never fails)
COPY --from=builder /app/whitelist*.txt /app/

ENTRYPOINT ["./mumzic"]
