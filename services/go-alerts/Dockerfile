FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/go-alerts .

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/go-alerts /go-alerts
EXPOSE 8101
USER nonroot:nonroot
ENTRYPOINT ["/go-alerts"]
