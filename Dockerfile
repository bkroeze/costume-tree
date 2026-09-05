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
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
	CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/costume-tree"]
