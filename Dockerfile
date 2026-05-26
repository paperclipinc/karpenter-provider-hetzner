FROM golang:1.23-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /karpenter-provider-hetzner ./cmd/controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /karpenter-provider-hetzner /karpenter-provider-hetzner
ENTRYPOINT ["/karpenter-provider-hetzner"]
