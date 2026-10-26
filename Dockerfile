# syntax=docker/dockerfile:1
# Local:  docker build -t opennow .
# CI:     docker buildx build --platform linux/amd64 -t ghcr.io/zx06/opennow:latest --push .
# GOPROXY is not pinned; use the Go default or set build-arg/ENV yourself if needed.

FROM golang:1.26-alpine AS build
WORKDIR /src
ARG TARGETOS=linux
ARG TARGETARCH=amd64
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /opennow ./cmd/opennow

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /opennow /usr/local/bin/opennow
ENV OPENNOW_ADDR=:8080
ENV OPENNOW_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["opennow"]
CMD ["serve"]
