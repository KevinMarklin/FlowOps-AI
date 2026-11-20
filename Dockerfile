FROM golang:alpine AS builder

ENV GOTOOLCHAIN=auto

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/server

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata openssl wget \
    && wget -q https://gu-st.ru/content/Other/doc/russian_trusted_root_ca.cer -O /tmp/russian_trusted_root_ca.cer \
    && wget -q https://gu-st.ru/content/Other/doc/russian_trusted_sub_ca.cer -O /tmp/russian_trusted_sub_ca.cer \
    && (openssl x509 -inform DER -in /tmp/russian_trusted_root_ca.cer -out /usr/local/share/ca-certificates/russian_trusted_root_ca.crt || openssl x509 -inform PEM -in /tmp/russian_trusted_root_ca.cer -out /usr/local/share/ca-certificates/russian_trusted_root_ca.crt) \
    && (openssl x509 -inform DER -in /tmp/russian_trusted_sub_ca.cer -out /usr/local/share/ca-certificates/russian_trusted_sub_ca.crt || openssl x509 -inform PEM -in /tmp/russian_trusted_sub_ca.cer -out /usr/local/share/ca-certificates/russian_trusted_sub_ca.crt) \
    && update-ca-certificates \
    && rm -f /tmp/russian_trusted_root_ca.cer /tmp/russian_trusted_sub_ca.cer

WORKDIR /app

COPY --from=builder /app/server /app/server
COPY web/ /app/web/
COPY knowledge_base/ /app/knowledge_base/
COPY schema.sql /app/schema.sql

EXPOSE 8080

CMD ["/app/server"]
