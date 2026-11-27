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

RUN addgroup -S -g 10001 flowops && adduser -S -D -H -u 10001 -G flowops flowops \
    && chown 10001:10001 /app
COPY --from=builder --chown=10001:10001 /app/server /app/server
COPY --chown=10001:10001 web/ /app/web/
COPY --chown=10001:10001 knowledge_base/ /app/knowledge_base/
COPY --chown=10001:10001 schema.sql /app/schema.sql

USER 10001:10001

EXPOSE 8080

CMD ["/app/server"]
