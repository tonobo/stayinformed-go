# syntax=docker/dockerfile:1

FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/webcal ./cmd/webcal \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/message2mail ./cmd/message2mail \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/stayinformed-token ./cmd/stayinformed-token

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 65532 -S app \
    && adduser -u 65532 -S -D -H -G app app
COPY --from=build /out/ /usr/local/bin/
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/webcal"]
