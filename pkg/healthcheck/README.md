# Проверка работоспособности

Зарегистрируйте транспорт, который использует сервис. Пакет сам не запускает сервер.

Для HTTP с `gorilla/mux`:

```go
router := mux.NewRouter()
healthcheck.RegisterHTTP(router)
// Обслуживайте router существующим HTTP-сервером.
```

`GET /health` возвращает `200 OK` и `ok`. Это проверка работоспособности процесса; она не проверяет PostgreSQL, Redis и другие зависимости.

Для gRPC:

```go
server := grpc.NewServer()
health := healthcheck.RegisterGRPC(server)
// Обслуживайте server существующим gRPC-слушателем.

// Когда процесс больше не может обрабатывать запросы:
health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
// При корректном завершении работы:
health.Shutdown()
```

Маршрут gRPC реализует стандартные методы `Check` и `Watch` сервиса `grpc.health.v1.Health`. Пустое имя сервиса показывает общее состояние и изначально имеет значение `SERVING`. При необходимости задавайте состояния именованных сервисов через возвращённый сервер проверки здоровья. Для опроса используйте стандартный gRPC health probe.
