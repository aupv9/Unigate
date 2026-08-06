FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/unigate-server ./cmd/server

# alpine (not distroless) so `wget` is available for the container
# HEALTHCHECK used by docker-compose's `condition: service_healthy`.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/unigate-server /usr/local/bin/unigate-server
COPY deploy/config/config.yaml /etc/unigate/config.yaml
EXPOSE 8080 8081 9090 9100
ENTRYPOINT ["/usr/local/bin/unigate-server", "-config=/etc/unigate/config.yaml"]
