FROM golang:alpine AS builder

ENV GOTOOLCHAIN=auto

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/server /app/server
COPY web/ /app/web/
COPY knowledge_base/ /app/knowledge_base/
COPY schema.sql /app/schema.sql

EXPOSE 8080

CMD ["/app/server"]
