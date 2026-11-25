# Бэкенд Т-Путешествий

Для локального запуска нужны Go 1.27 или новее и подготовленная база PostgreSQL. После запуска базы и применения миграций задайте `DATABASE_URL` и `JWT_SECRET`, затем запустите сервер из папки `backend`. Используйте случайный секрет длиной не менее 32 байт и сохраняйте его между перезапусками. Пример для PowerShell со значениями базы из `.env.example`

```powershell
$env:DATABASE_URL = 'postgres://t_travels:local_dev_password@127.0.0.1:5432/t_travels?sslmode=disable'
$env:JWT_SECRET = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
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

Миграция `000001` создаёт таблицы `users` и `refresh_sessions`. Email уникален без учёта регистра. Таблицы предназначены для хранения хешей паролей и refresh-токенов.

## Авторизация

`POST /api/v1/auth/register` принимает JSON с полями `email`, `password` и `name`. Пароль должен содержать от 15 до 128 символов. Ответ `201` содержит UUID, email и имя пользователя. При повторной регистрации с тем же email сервер возвращает `409`.

`POST /api/v1/auth/login` принимает `email` и `password` и выдаёт JWT на 15 минут и refresh-токен для сессии на 30 дней. `POST /api/v1/auth/refresh` принимает `refresh_token` и заменяет оба токена. Общий срок сессии отсчитывается от входа. Если ответ обновления потерян после успешной ротации, требуется повторный вход.

`POST /api/v1/auth/logout` принимает текущий `refresh_token` и отзывает сессию. Уже выданный JWT действует до истечения своего срока.

`GET /api/v1/users/me` возвращает `id`, `email` и `name` текущего пользователя. Передайте access-токен в заголовке `Authorization: Bearer <access_token>`.

## Поездки и приглашения

Организатор создаёт поездку через `POST /api/v1/trips`, редактирует её с текущей `version` через `PATCH /api/v1/trips/{tripId}` и выпускает семидневный код через `POST /api/v1/trips/{tripId}/invitations`. Бюджет указывается целым числом копеек в `RUB`. Пользователь вступает по коду через `POST /api/v1/trips/join`. Поездку и её участников видят только активные участники поездки.

Для этих изменяющих операций передавайте `Idempotency-Key` с UUID. При сетевом повторе сохраните прежние ключ и значения запроса. Для чтения списков используются `limit` и `offset`.

Действующие участники получают уведомление `member_joined` о новом участнике. `GET /api/v1/notifications` возвращает свои уведомления. Отметить их прочитанными можно по одному или все сразу. Для `POST /api/v1/notifications/read-all` также нужен `Idempotency-Key`.

## Ветки и CI

Новые ветки `feature/*` и `fix/*` создаются от `develop`. Изменения попадают в `develop` через pull request. Готовую версию переносим из `develop` в `main`.

GitHub Actions проверяет форматирование, тесты, `go vet`, сборку, миграции, регистрацию и сессии на тестовой PostgreSQL при изменениях бэкенда в `main` и `develop`, а также в pull request в эти ветки.
