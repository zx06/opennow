FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /opennow ./cmd/opennow

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
