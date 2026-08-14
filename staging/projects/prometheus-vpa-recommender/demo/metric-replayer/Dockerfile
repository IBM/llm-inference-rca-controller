# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /workspace

COPY go.mod ./
RUN go mod download

COPY main.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -a -o metric-replayer main.go

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

COPY --from=builder /workspace/metric-replayer .

USER 65532:65532

ENTRYPOINT ["/metric-replayer"]
