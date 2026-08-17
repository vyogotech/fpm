# Container image for the fpm registry service (`fpm-registry serve`).
#
# Only the registry is packaged here. The CLI is distributed as a binary to
# developers; an operator deploying the registry has no use for it, and baking
# both into one image would invite running publish commands inside the server.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Dependencies resolve in their own layer so source edits do not re-download
# the module cache on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 keeps the binary static, which is what lets the runtime stage
# stay minimal — no libc to match, no dynamic loader to go missing.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
        -o /out/fpm-registry ./cmd/fpm-registry

FROM alpine:3.21

# Runs unprivileged. The registry writes only to its document root and its
# publishers file, both of which arrive as mounted volumes.
RUN adduser -S -u 10001 -H -D fpm

COPY --from=build /out/fpm-registry /usr/local/bin/fpm-registry

# Declared for documentation and for `docker run` without a mount; in Kubernetes
# both paths are replaced by volumes.
RUN mkdir -p /var/fpm-repo /etc/fpm && chown -R 10001 /var/fpm-repo /etc/fpm

USER 10001

EXPOSE 8080

ENV FPM_REGISTRY_ROOT=/var/fpm-repo \
    FPM_REGISTRY_ADDR=:8080

ENTRYPOINT ["/usr/local/bin/fpm-registry"]
CMD ["serve"]
