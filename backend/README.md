# Бэкенд Т-Путешествий

Для локального запуска нужен Go 1.27 или новее. Из корня репозитория выполните

```sh
cd backend
go run ./cmd/api
```

`GET http://127.0.0.1:8080/healthz` возвращает `200 OK` и `{"status":"ok"}`. Адрес сервера задаётся переменной окружения `HTTP_ADDR`.

## OpenAPI

Контракт хранится в [api/swagger.yaml](api/swagger.yaml) в формате OpenAPI 3.0.3. Запущенный сервер отдаёт его по адресу `http://127.0.0.1:8080/openapi.yaml`. Проверка `go test ./...` валидирует схему и сверяет с ней ответ `/healthz`.

## Локальная база данных

Из папки `backend` один раз скопируйте `.env.example` в `.env`. Запустите PostgreSQL через Docker Compose и примените миграции.

```sh
cp .env.example .env
docker compose up -d --wait postgres
docker compose run --rm migrate up
docker compose run --rm migrate version
```

С хоста база доступна на `127.0.0.1:5432`. Имя базы и пользователь — `t_travels`, пароль берётся из `.env`. Если порт занят, задайте другой `POSTGRES_PORT` в `.env`. Команда `docker compose down` останавливает локальное окружение и сохраняет данные в томе `postgres-data`.

Миграция `000001` создаёт таблицы `users` и `refresh_sessions`. Email уникален без учёта регистра. Таблицы предназначены для хранения хешей паролей и refresh-токенов. HTTP-сервер пока запускается отдельно от базы.

## Ветки и CI

Новые ветки `feature/*` и `fix/*` создаются от `develop`. Изменения попадают в `develop` через pull request. Готовую версию переносим из `develop` в `main`.

GitHub Actions проверяет форматирование, тесты, `go vet`, сборку и цикл применения миграций при изменениях бэкенда в `main` и `develop`, а также в pull request в эти ветки.
