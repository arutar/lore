FROM node:24-alpine AS web-build
WORKDIR /build
COPY platform/web/package.json platform/web/package-lock.json ./
RUN npm ci
COPY platform/web/ ./
RUN npm run build

FROM golang:1.27.3-alpine AS go-build
WORKDIR /src
COPY platform/control-plane/go.mod platform/control-plane/go.sum ./platform/control-plane/
RUN cd platform/control-plane && go mod download
COPY platform/control-plane/ ./platform/control-plane/
RUN cd platform/control-plane && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/control-plane ./cmd/control-plane

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S lore && adduser -S -G lore lore
COPY --from=go-build /out/control-plane /usr/local/bin/control-plane
COPY --from=web-build /build/dist/ /web/
ENV STATIC_DIR=/web
EXPOSE 8080 9090
USER lore
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/control-plane"]
