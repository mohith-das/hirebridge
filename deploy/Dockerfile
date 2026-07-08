# syntax=docker/dockerfile:1

# ---- builder ----
FROM golang:1.23 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG BUILD_TAGS="sqlite_fts5 sqlite_load_extension"
RUN CGO_ENABLED=1 go build -tags "${BUILD_TAGS}" -ldflags="-s -w" -o /out/hirebridge ./cmd/hirebridge

# ---- vec0 extension ----
FROM alpine:3.20 AS vec0
RUN wget -qO- https://github.com/asg017/sqlite-vec/releases/download/v0.1.9/sqlite-vec-0.1.9-loadable-linux-x86_64.tar.gz \
  | tar xz -C /out

# ---- runtime ----
FROM gcr.io/distroless/base-debian12

COPY --from=builder /out/hirebridge /app/hirebridge
COPY --from=vec0 /out/vec0.so /app/ext/vec0.so

ENV HB_LISTEN=:8080
ENV HB_DB_PATH=/data/hirebridge.db
ENV HB_VEC0_PATH=/app/ext/vec0.so

EXPOSE 8080
VOLUME ["/data"]

ENTRYPOINT ["/app/hirebridge"]
