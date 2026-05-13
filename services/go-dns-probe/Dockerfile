FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/go-dns-probe .

# Use a small Alpine image (not distroless) so ICMP works without raw sockets
# via the unprivileged SOCK_DGRAM path, which still needs a real /proc.
FROM alpine:3.20
RUN adduser -D -u 65532 probe
COPY --from=build /out/go-dns-probe /go-dns-probe
EXPOSE 8102
USER probe
ENTRYPOINT ["/go-dns-probe"]
