FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/go-ingest .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/go-ingest /go-ingest
EXPOSE 8100
USER nonroot:nonroot
ENTRYPOINT ["/go-ingest"]
