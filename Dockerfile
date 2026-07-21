# Stage 1: Build proxy server
FROM golang:1.25-alpine AS builder
# "develop" (not e.g. "dev"), unless overridden by --build-arg VERSION=vX.Y.Z
# (as release.yml/beta.yml do): Lidarr's SABnzbd client special-cases the
# literal string "develop" to assume SABnzbd 3.0.0+, and rejects anything
# else that isn't a strict semver-shaped X.Y.Z. See cmd/server/main.go.
ARG VERSION=develop
WORKDIR /build
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server

# Stage 2: Build spotiflac-cli from fork (headless, relay-capable)
FROM golang:1.26-alpine AS cli-builder
ARG SPOTIFLAC_COMMIT=326bbfaf03d9c49bfec9f3565136728d1fdd95fd
RUN apk add --no-cache git
RUN git clone https://github.com/fishingpvalues/SpotiFLAC.git /spotiflac && \
    cd /spotiflac && git checkout ${SPOTIFLAC_COMMIT}
WORKDIR /spotiflac
RUN CGO_ENABLED=0 go build -tags headless -ldflags="-s -w" -o /out/spotiflac-cli .

# Stage 3: Runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S spotiflac && adduser -S spotiflac -G spotiflac
COPY --from=builder /out/server /usr/local/bin/server
COPY --from=cli-builder /out/spotiflac-cli /usr/local/bin/spotiflac-cli
RUN mkdir -p /downloads /data /home/spotiflac/.spotiflac && \
    chown -R spotiflac:spotiflac /downloads /data /home/spotiflac
USER spotiflac
EXPOSE 8484
ENTRYPOINT ["server", "serve"]
