FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/netreg ./cmd/netreg

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/netreg /usr/local/bin/netreg

VOLUME ["/data"]
EXPOSE 8080

ENTRYPOINT ["netreg"]
