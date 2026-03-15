FROM golang:1.25rc1 AS builder
ENV GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o /nsh ./cmd/nsh
RUN go install github.com/homeport/termshot/cmd/termshot@latest

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    tmux bash coreutils git curl ca-certificates fonts-dejavu-core && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /nsh /usr/local/bin/nsh
COPY --from=builder /go/bin/termshot /usr/local/bin/termshot
COPY test/e2e/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

ENV TERM=xterm-256color
ENTRYPOINT ["/entrypoint.sh"]
