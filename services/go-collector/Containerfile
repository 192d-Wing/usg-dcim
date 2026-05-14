FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/go-collector .

# Alpine (not distroless) — gives a writable /var/lib for the SQLite
# buffer when the operator hasn't mounted a volume there yet.
FROM alpine:3.20
RUN adduser -D -u 65532 collector \
 && mkdir -p /var/lib/dcim-collector \
 && chown collector:collector /var/lib/dcim-collector
COPY --from=build /out/go-collector /go-collector
USER collector
ENTRYPOINT ["/go-collector"]
