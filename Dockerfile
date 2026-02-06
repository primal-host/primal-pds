# Build stage: compile the Go binary.
FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o primal-pds ./cmd/primal-pds

# Runtime stage: minimal image with just the binary.
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/primal-pds .

EXPOSE 3000

CMD ["./primal-pds"]
