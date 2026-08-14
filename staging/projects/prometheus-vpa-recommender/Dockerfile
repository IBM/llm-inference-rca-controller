# Build stage
FROM golang:1.26.2-alpine AS builder

WORKDIR /workspace

# Copy go mod files
COPY go.mod ./
COPY go.sum* ./

# Download dependencies
RUN go mod download

# Copy source code
COPY main.go ./
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o prometheus-vpa-recommender main.go

# Runtime stage
FROM gcr.io/distroless/static:nonroot

WORKDIR /

# Copy binary from builder
COPY --from=builder /workspace/prometheus-vpa-recommender .

USER 65532:65532

ENTRYPOINT ["/prometheus-vpa-recommender"]
