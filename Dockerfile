# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/costume-tree ./cmd/costume-tree

FROM alpine:3.22.1
RUN addgroup -S -g 65532 app && adduser -S -D -H -u 65532 -G app app \
	&& mkdir /data \
	&& chown 65532:65532 /data
COPY --from=build /out/costume-tree /costume-tree
USER 65532:65532
# Runtime commands intentionally run as the same non-root user as the server:
#   docker exec <container> /costume-tree backup /data/backups/costume-tree-$(date +%Y%m%d).db
# Restore only while stopped, into a fresh path:
#   docker run --rm -u 65532:65532 -v costume-tree-data:/data costume-tree:dev \
#     restore /data/backups/backup.db /data/costume-tree-restored.db
#
# The restore command validates ownership, schema, and integrity and refuses
# to overwrite an existing path. Rename a validated restored file into place
# only after stopping the application.
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/costume-tree"]
