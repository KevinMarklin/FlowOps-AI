# FlowOps AI

FlowOps AI - сервис для обработки обращений сотрудников и клиентов. Он принимает тикеты через HTTP API и omnichannel ingest, определяет отдел и приоритет, ищет ответ в базе знаний и при необходимости передает обращение оператору.

## Возможности

- классификация обращений по отделам и приоритету;
- RAG-ответы на основе Markdown-документов в `knowledge_base/`;
- автоматическое решение тикета после подтверждения пользователя;
- прием обращений из web, email и bot-каналов через единый ingest API;
- обучение базы знаний на ответах операторов;
- простая web-панель в каталоге `web/`.

## Требования

- Go 1.22 или новее;
- PostgreSQL;
- API-ключ Gemini/совместимого AI-провайдера для LLM-режима.

## Быстрый старт

1. Создайте базу PostgreSQL и скопируйте `.env.example` в `.env`.
2. Заполните `DATABASE_URL` и, при необходимости, `GEMINI_API_KEY` или `AI_API_KEY`.
3. Установите зависимости:

   ```bash
   go mod download
   ```

4. Запустите сервер:

   ```bash
   go run ./cmd/server
   ```

По умолчанию API доступен на `http://localhost:8080`. Таблица `tickets` создается автоматически при запуске; SQL для ручной инициализации находится в `schema.sql`.

## API

Создать тикет:

```bash
curl -X POST http://localhost:8080/api/tickets \
  -H "Content-Type: application/json" \
  -d '{"user_email":"user@example.com","raw_text":"Не работает VPN"}'
```

Получить список тикетов:

```bash
curl http://localhost:8080/api/tickets
```

Принять обращение из канала:

```bash
curl -X POST http://localhost:8080/api/v1/ingest \
  -H "Content-Type: application/json" \
  -d '{"channel":"web","sender":"user@example.com","message":"Не работает VPN"}'
```

Дополнительные endpoints для статистики, обратной связи и управления базой знаний зарегистрированы в `cmd/server/main.go`.

## Тестирование

Запустить модульные тесты:

```bash
go test ./...
```

Для API-проверок после запуска сервера доступны PowerShell-скрипты `test_api.ps1` и `test_all.ps1`.

## Конфигурация

Основные переменные окружения описаны в `.env.example`:

- `PORT` - порт HTTP-сервера;
- `DATABASE_URL` - строка подключения к PostgreSQL;
- `AI_BASE_URL` - OpenAI-compatible endpoint AI-провайдера;
- `AI_API_KEY` / `GEMINI_API_KEY` - секретный API-ключ;
- `AI_MODEL` - используемая модель.
