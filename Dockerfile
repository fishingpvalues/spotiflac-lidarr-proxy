# Stage 1: Build proxy server
FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /build
RUN apk add --no-cache git ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/server ./cmd/server

# Stage 2: Build spotiflac-cli from fork (requires Go 1.26)
FROM golang:1.26-alpine AS cli-builder
ARG SPOTIFLAC_COMMIT=289920c9755f9426175ba88ab2ac0ae45ab8f7d0
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
RUN mkdir -p /downloads /data && chown -R spotiflac:spotiflac /downloads /data
USER spotiflac
EXPOSE 8484
ENTRYPOINT ["server", "serve"]
