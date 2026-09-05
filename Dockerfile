# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/costume-tree ./cmd/costume-tree

FROM scratch
COPY --from=build /out/costume-tree /costume-tree
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/costume-tree"]
