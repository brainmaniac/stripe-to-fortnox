# Stage 1: Download Go dependencies
FROM golang:1.23-alpine AS deps
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

# Stage 2: Generate templ files
FROM ghcr.io/a-h/templ:latest AS templ-generate
COPY --chown=65532:65532 . /app
WORKDIR /app
RUN ["templ", "generate"]

# Stage 3: Build Go binary
FROM golang:1.23-alpine AS build
WORKDIR /app
COPY --from=deps /go/pkg/mod /go/pkg/mod
COPY --from=templ-generate /app /app
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /app/server ./cmd/server

# Stage 4: Minimal runtime image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -S app && adduser -S -G app app
RUN mkdir -p /app/data && chown -R app:app /app
WORKDIR /app
COPY --from=build --chown=app:app /app/server /app/server
COPY --chown=app:app static /app/static
USER app
EXPOSE 8080
ENTRYPOINT ["/app/server"]
