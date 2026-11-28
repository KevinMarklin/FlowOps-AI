# Справочник API

Все пути ниже относительны адресу установки FlowOps. JSON передаётся как `Content-Type: application/json`. Время API обычно содержит часовой пояс; пользовательские календари используют настройку организации.

## Авторизация и границы

Сайт использует cookie `flowops_session` и CSRF-cookie `flowops_csrf`. Для изменяющих запросов по cookie значение CSRF передаётся в `X-CSRF-Token`; проверяется origin. `X-FlowOps-Member` контролирует совпадение пользователя вкладки с текущим сеансом и не является самостоятельным паролем.

Для внешнего ввода используйте `Authorization: Bearer <ingest-key>` на `/api/v1/ingest`. Это не административный ключ. Публичный widget-ключ дополнительно связан с origin. Совместимый служебный Bearer-доступ не следует публиковать в HTML или использовать вместо персональных приглашений.

Компания выводится из авторизации. Не добавляйте `tenant_id` в надежде выбрать чужую компанию. Наличие маршрута не означает разрешение любому вошедшему пользователю: проверяются роль, разрешение, область и принадлежность ресурса.

## Вход

`POST /api/v1/auth/login` принимает `email`, `password`, `workspace`. При нескольких доступных компаниях требуется выбор. `POST /api/v1/auth/activate` используется для первого входа и восстановления по выданному коду. `GET /api/v1/auth/me` возвращает текущий профиль и доступную конфигурацию сеанса; не храните пароль после успешного входа.

## Создание тикета

```json
{
  "raw_text": "Не работает принтер в кабинете 12",
  "user_email": "employee@example.org",
  "form_id": "equipment",
  "form_revision": 3,
  "fields": {"room": "12"},
  "category": "equipment",
  "ticket_type": "incident",
  "tags": ["printer"]
}
```

Это пример структуры, а не гарантированно существующих кодов. Сначала получите `/api/v1/organization/ticket-form` и используйте актуальные форму, ревизию и поля вашей компании. Сотрудник не может подменить автора через `user_email`. Дополнительно поддерживаются `priority_id`, `asset_id`, `intake` и `attachments`.

Во вложении передаётся объект `filename`, `content`; байты `content` представлены Base64 по JSON-контракту Go. Карта вложений связывается с полями формы. Сервер ограничивает размер и проверяет имена, принадлежность и количество.

Для списка используйте `GET /api/tickets?q=принтер` либо ID в `q`. Состояния очереди фильтруются `queue_status`; не подменяйте их произвольным отображаемым названием. Для пагинации используйте курсоры, которые применяет актуальный интерфейс/ответ API.

## Внешнее сообщение

```json
{
  "channel": "API",
  "sender": "external-user-17",
  "subject": "Рабочее место",
  "message": "Не работает рабочий сервис",
  "idempotency_key": "external-case-2026-001",
  "require_confirmation": true
}
```

Поле сообщения называется `message`, а не `text`. Доступен `callback_url`, но только в рамках разрешённых сервером адресов. Отправитель внешнего канала не получает права участника по произвольной строке.

## Повторы

`Idempotency-Key` содержит 1–128 латинских букв, цифр, точек, подчёркиваний, двоеточий или дефисов. Для ingest допускается JSON-поле `idempotency_key`; при одновременном использовании значения должны совпадать.

Тот же ключ и то же тело после неопределённого сетевого результата возвращают существующее обращение; успешное создание/повтор может иметь HTTP 201. Изменённое тело под тем же ключом возвращает 409. Область включает компанию, источник и отправителя. Запрос без ключа не гарантирует дедупликацию одинакового текста.

## Переписка, решение и оценки

`POST /api/tickets/{id}/messages` принимает `body` и устойчивый `client_message_id`. Сохраняйте идентификатор при повторе того же сообщения. `POST .../decision` принимает `resolved`. Оценка `POST .../messages/{message}/rating` принимает `score` и `comment`; допустимость проверяется сервером и ролью сотрудника.

Для операторского ответа существует `/reply`. Изменение тикета выполняется PATCH; закрытие требует соответствующего разрешения и причины. Сначала загрузите текущую карточку: нельзя считать кнопку в старом DOM доказательством, что тикет всё ещё открыт.

## Версии и ошибки

Редактируемые определения, настройки и материалы имеют ревизии. Передавайте полученную ревизию в соответствующем контракте; после 409 перечитайте объект и разрешите конфликт.

| Код | Типичный смысл |
|---|---|
| 200 / 201 | Успех / создание или корректный повтор создания |
| 400 | Некорректные поля, параметры или ключ |
| 401 | Вход отсутствует или реквизиты не приняты |
| 403 | Недостаточно прав либо нарушена защита изменяющего запроса |
| 404 | Ресурс отсутствует или скрыт по доступу |
| 409 | Конфликт ревизии, ключа повтора или сеанса |
| 413 | Превышен размер |
| 429 | Ограничение частоты/ресурса там, где оно применяется |
| 502 / 503 | Внешний сервис или необходимая подсистема недоступны |

Ответы с персональными данными и настройками не следует кэшировать общедоступным прокси. Для HTTP-эффектов workflow получатель должен сам дедуплицировать запрос по `Idempotency-Key`.

## Каталог маршрутов

Ниже перечислены зарегистрированные методы бэкенда. Детали действий и ограничения находятся в соответствующих главах. Фигурные скобки обозначают параметр пути; OPTIONS обслуживает preflight внешнего виджета.

| Метод | Путь | Назначение |
|---|---|---|
| GET | `/api/v1/profile/bots` | Личные привязки ботов |
| POST | `/api/v1/profile/bots/{channel}` | Личные привязки ботов |
| DELETE | `/api/v1/profile/bots/{channel}` | Личные привязки ботов |
| POST | `/api/v1/auth/login` | Сеанс и активация |
| POST | `/api/v1/auth/activate` | Сеанс и активация |
| POST | `/api/v1/auth/logout` | Сеанс и активация |
| GET | `/api/v1/auth/me` | Сеанс и активация |
| GET | `/api/v1/members` | Команда и отзыв доступа |
| POST | `/api/v1/members` | Команда и отзыв доступа |
| PATCH | `/api/v1/members/{id}` | Команда и отзыв доступа |
| POST | `/api/v1/members/{id}/rotate` | Команда и отзыв доступа |
| POST | `/api/v1/workspaces` | Регистрация компании |
| GET | `/api/v1/organization/directory` | Безопасные данные селекторов |
| GET | `/api/v1/organization/ticket-form` | Схема формы |
| PUT | `/api/v1/organization/ticket-form` | Схема формы |
| GET | `/api/v1/organization/roles` | Определения и назначение ролей |
| POST | `/api/v1/organization/roles` | Определения и назначение ролей |
| POST | `/api/v1/organization/roles/assign` | Определения и назначение ролей |
| GET | `/api/v1/organization/units` | Иерархия и членство |
| POST | `/api/v1/organization/units` | Иерархия и членство |
| PUT | `/api/v1/organization/units/{id}` | Иерархия и членство |
| GET | `/api/v1/organization` | Данные организации |
| GET | `/api/v1/settings/organization` | Профиль, календарь и политики |
| PUT | `/api/v1/settings/organization` | Профиль, календарь и политики |
| GET | `/api/tickets` | Создание, список и карточка |
| GET | `/api/v1/notifications/sla` | Уведомления SLA |
| GET | `/api/tickets/{id}` | Создание, список и карточка |
| GET | `/api/tickets/{id}/fields` | Поля тикета |
| PUT | `/api/tickets/{id}/fields` | Поля тикета |
| GET | `/api/tickets/{id}/attachments` | Файлы тикета |
| POST | `/api/tickets/{id}/attachments` | Файлы тикета |
| GET | `/api/tickets/{id}/attachments/{attachment}` | Файлы тикета |
| POST | `/api/tickets` | Создание, список и карточка |
| POST | `/api/v1/intake/preview` |  |
| POST | `/api/v1/ingest` | Внешний ввод обращений |
| OPTIONS | `/api/v1/ingest` | Внешний ввод обращений |
| GET | `/api/v1/keys` | Интеграционные ключи |
| POST | `/api/v1/keys` | Интеграционные ключи |
| DELETE | `/api/v1/keys/{id}` | Интеграционные ключи |
| GET | `/api/v1/widget/config` | Настройки внешнего виджета |
| PUT | `/api/v1/widget/config` | Настройки внешнего виджета |
| OPTIONS | `/api/v1/widget/config` | Настройки внешнего виджета |
| POST | `/api/v1/kb/smart-import` | Подготовка черновиков |
| POST | `/api/v1/settings/preview` | Пробный ответ без создания тикета |
| GET | `/api/v1/settings/builder` | Отделы и тексты общения |
| PUT | `/api/v1/settings/builder` | Отделы и тексты общения |
| GET | `/api/v1/settings/channels` | Сохранение и проверка каналов |
| PUT | `/api/v1/settings/channels` | Сохранение и проверка каналов |
| DELETE | `/api/v1/settings/channels/{channel}` | Сохранение и проверка каналов |
| POST | `/api/v1/settings/channels/{channel}/check` | Сохранение и проверка каналов |
| GET | `/api/v1/integrations/1c` | Подключение и чтение 1С |
| PUT | `/api/v1/integrations/1c` | Подключение и чтение 1С |
| POST | `/api/v1/integrations/1c/check` | Подключение и чтение 1С |
| GET | `/api/v1/integrations/1c/data` | Подключение и чтение 1С |
| POST | `/api/tickets/{id}/feedback` | Совместимая обратная связь |
| POST | `/api/tickets/{id}/reply` | Ответ специалиста |
| GET | `/api/tickets/{id}/messages` | Диалог тикета |
| POST | `/api/tickets/{id}/messages` | Диалог тикета |
| POST | `/api/tickets/{id}/decision` | Подтверждение результата |
| POST | `/api/tickets/{id}/read` | Состояние прочтения |
| POST | `/api/tickets/{id}/messages/{message}/rating` | Оценка ответа сотрудником |
| POST | `/api/tickets/{id}/work` | Учёт активной работы |
| PATCH | `/api/tickets/{id}` | Создание, список и карточка |
| POST | `/api/tickets/queue/clear` | Персональное скрытие списка |
| GET | `/api/v1/assets` | Объекты, контекст и QR |
| POST | `/api/v1/assets` | Объекты, контекст и QR |
| PUT | `/api/v1/assets/{id}` | Объекты, контекст и QR |
| GET | `/api/v1/assets/{id}/context` | Объекты, контекст и QR |
| GET | `/api/v1/assets/{id}/qr` | Объекты, контекст и QR |
| GET | `/api/v1/workflows` | Определения/запуски процессов |
| POST | `/api/v1/workflows` | Определения/запуски процессов |
| PUT | `/api/v1/workflows/{id}` | Определения/запуски процессов |
| GET | `/api/tickets/{id}/workflows` | Определения/запуски процессов |
| POST | `/api/tickets/{id}/workflows` | Определения/запуски процессов |
| GET | `/api/v1/workflow-tasks` | Задачи и решения |
| POST | `/api/v1/workflow-tasks/{id}` | Задачи и решения |
| POST | `/api/v1/workflow-runs/{id}/cancel` | Отмена процесса |
| GET | `/api/analytics/reports` | Шаблоны и файлы отчётов |
| POST | `/api/analytics/reports` | Шаблоны и файлы отчётов |
| PUT | `/api/analytics/reports/{id}` | Шаблоны и файлы отчётов |
| POST | `/api/analytics/reports/{id}/run` | Шаблоны и файлы отчётов |
| GET | `/api/analytics/reports/artifacts/{artifact}` | Шаблоны и файлы отчётов |
| GET | `/api/analytics/dashboards` | Дашборды |
| POST | `/api/analytics/dashboards` | Дашборды |
| PUT | `/api/analytics/dashboards/{id}` | Дашборды |
| DELETE | `/api/analytics/dashboards/{id}` | Дашборды |
| POST | `/api/optimization/semantic` | Рекомендации улучшений |
| GET | `/api/optimization` | Рекомендации улучшений |
| PUT | `/api/optimization/{id}` | Рекомендации улучшений |
| GET | `/api/analytics/explore` | Фильтрованная аналитика и экспорт |
| GET | `/api/analytics/processes` | Наблюдаемые этапы и маршруты |
| GET | `/api/tickets/{id}/copilot` | Контекст и предложения Яндекса |
| POST | `/api/tickets/{id}/copilot` | Контекст и предложения Яндекса |
| GET | `/api/tickets/{id}/events` | История обработки |
| GET | `/api/stats` | Базовые агрегаты |
| GET | `/api/audit` | Журнал действий |
| GET | `/api/v1/kb/articles` | Согласование и версии знаний |
| POST | `/api/v1/kb/articles` | Согласование и версии знаний |
| PUT | `/api/v1/kb/articles/{id}` | Согласование и версии знаний |
| GET | `/api/v1/kb/articles/{id}/versions` | Согласование и версии знаний |
| POST | `/api/v1/kb/from-tickets` | Черновик из решённых обращений |
| GET | `/api/v1/kb/checks` | Проверки противоречий |
| POST | `/api/v1/kb/checks` | Проверки противоречий |
| GET | `/api/knowledge` | Совместимый каталог знаний |
| POST | `/api/knowledge` | Совместимый каталог знаний |
| PATCH | `/api/knowledge/{filename}` | Совместимый каталог знаний |
| DELETE | `/api/knowledge/{filename}` | Совместимый каталог знаний |
