FROM golang:1.25rc1
ENV GOTOOLCHAIN=auto
RUN apt-get update && apt-get install -y curl zstd
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
