FROM golang:1.25-alpine AS builder

ARG VERSION=dev

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -o /seaweedfs-provisioner ./cmd/seaweedfs-provisioner

FROM scratch
COPY --from=builder /seaweedfs-provisioner /seaweedfs-provisioner
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
ENTRYPOINT ["/seaweedfs-provisioner"]
