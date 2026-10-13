# The spike's Go binaries: both gateway stand-ins and candidate D.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ ./cmd/...

FROM alpine:3.22
RUN adduser -D -h /home/agent agent
COPY --from=build /out/ /usr/local/bin/
USER agent
WORKDIR /work
