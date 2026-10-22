# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/pggo .

FROM alpine:latest
# postgresql-client 提供 pg_dump / pg_restore / psql,供备份页使用
RUN apk add --no-cache ca-certificates tzdata postgresql-client \
    && addgroup -S app && adduser -S -G app app
ENV TZ=Asia/Shanghai
COPY --from=build /out/pggo /usr/local/bin/pggo
USER app
WORKDIR /app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/login >/dev/null || exit 1
ENTRYPOINT ["pggo"]
# 容器内需要监听所有网卡;若只想容器内可访问,改为 127.0.0.1:8080。
CMD ["-addr", ":8080"]
