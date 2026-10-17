FROM golang:1.27.2-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/reverse-proxy .

FROM alpine:3.24
RUN addgroup -S proxy && adduser -S -G proxy proxy
WORKDIR /app
COPY --from=build /out/reverse-proxy /usr/local/bin/reverse-proxy
COPY config.yaml /app/config.yaml
USER proxy
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/reverse-proxy"]
