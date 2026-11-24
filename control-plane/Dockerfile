FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /infera-server .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H infera && mkdir /data && chown infera /data
USER infera
COPY --from=build /infera-server /usr/local/bin/infera-server
ENV INFERA_DB=/data/infera.db INFERA_ADDR=:8080
EXPOSE 8080
HEALTHCHECK CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["infera-server"]
