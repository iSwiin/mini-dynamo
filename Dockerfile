# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS build
WORKDIR /src

# Build dependencies (none external, but keep CA certs for module downloads if needed)
RUN apk add --no-cache ca-certificates

COPY go.mod ./
# If you add external deps later, this keeps Docker layer caching effective.
RUN go mod download || true

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mini-dynamo-node ./cmd/node

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app

# Default data dir (can be overridden via --data_dir)
RUN mkdir -p /data
COPY --from=build /out/mini-dynamo-node /usr/local/bin/mini-dynamo-node

EXPOSE 9001 9002 9003
ENTRYPOINT ["mini-dynamo-node"]
