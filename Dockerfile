FROM golang:1.27-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Central version — source of truth is version.json, which the release job
# commits and the Makefile reads. VERSION= can be passed in to override.
ARG VERSION
RUN VERSION=${VERSION:-$(cat version.json | sed -n 's/.*"latestVersion": *"\([^"]*\)".*/\1/p')} && \
    echo "Building version $VERSION" && \
    CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X '9router/proxy/internal/updater.CurrentVersion=${VERSION}'" -o 9router-go ./cmd/9router-go/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /app/9router-go /usr/local/bin/9router-go
EXPOSE 20128
ENTRYPOINT ["9router-go"]
