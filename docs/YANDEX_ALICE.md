# Алиса в Yandex AI Studio

Проект использует Alice AI LLM через OpenAI-совместимый Completions API.
Адрес сервиса фиксирован: `https://ai.api.cloud.yandex.net/v1`.

Добавьте в `.env`:

```dotenv
FLOWOPS_AI_PROVIDERS=yandex
FLOWOPS_AI_ALLOW_FALLBACK=false
FLOWOPS_AI_ALLOW_SENSITIVE=false
YANDEX_API_KEY=
YANDEX_FOLDER_ID=
YANDEX_MODEL=aliceai-llm
```

Заполните ключ и идентификатор каталога. Можно указать короткое имя модели
или полный URI `gpt://<folder-id>/aliceai-llm`; каталог в URI должен совпадать
с `YANDEX_FOLDER_ID`. Старые переменные `AI_*`, `GIGACHAT_*` и `GEMINI_API_KEY`
не используются при политике `yandex`.

Требуется активный платёжный аккаунт, роль сервисного аккаунта
`ai.languageModels.user` на каталоге и область ключа
`yc.ai.foundationModels.execute`.

После заполнения:

```powershell
docker compose up -d --build --force-recreate app
```

При запуске приложение показывает `Alice AI (Yandex AI Studio)` и URI модели.
После обращения проверяйте фактический источник анализа в логах:
`Local Heuristic (fallback)` означает, что модель не выполнила анализ.
Запрет резервных внешних провайдеров не отключает локальную обработку при
недоступности модели. Ошибка 403 означает отказ в доступе: проверьте биллинг,
роль, область ключа и каталог.

Модель получает текст для определения отдела, приоритета и краткого содержания.
JSON проверяется существующим валидатором. Ответы по регламентам берутся из
локальной базы знаний; веб-поиск не подключается. Проверка чувствительных данных
сохранена. Запросы передают `OpenAI-Project` и
`x-data-logging-enabled: false`; секреты не выводятся в логи.

Документация:
- https://aistudio.yandex.ru/ru/docs/ai-studio/concepts/generation/models
- https://aistudio.yandex.ru/ru/docs/ai-studio/operations/generation/completions-basic
