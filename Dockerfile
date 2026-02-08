# Build stage: compile the Go binary.
FROM golang:1.25-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
ENV GOPROXY=http://host.docker.internal:3000,direct
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o primal-pds ./cmd/primal-pds

# Runtime stage: minimal image with just the binary.
FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata curl

WORKDIR /app
COPY --from=builder /app/primal-pds .
COPY smoke-test.sh .

EXPOSE 3000

CMD ["./primal-pds"]
