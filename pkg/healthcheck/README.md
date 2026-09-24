# Health checks

Register the transport used by a service. The package does not start a server.

For HTTP with `gorilla/mux`:

```go
router := mux.NewRouter()
healthcheck.RegisterHTTP(router)
// Serve router with your existing HTTP server.
```

`GET /health` returns `200 OK` and `ok`. This is a liveness check: it does not
test PostgreSQL, Redis, or other dependencies.

For gRPC:

```go
server := grpc.NewServer()
health := healthcheck.RegisterGRPC(server)
// Serve server with your existing gRPC listener.

// When the process can no longer serve requests:
health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
// During graceful shutdown:
health.Shutdown()
```

The gRPC endpoint implements the standard `grpc.health.v1.Health` Check and
Watch RPCs. The empty service name reports overall health and starts as
`SERVING`. Set named service statuses through the returned health server if
needed. Use a standard gRPC health probe to query it.
