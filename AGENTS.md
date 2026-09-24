# Instructions for coding agents

## Project context

This repository is a Go monorepo for a small educational project with several microservices. It runs on a self-hosted k3s cluster across VMs on one Proxmox host. PostgreSQL stores transactional data, Redis provides short-lived data or queues where needed, and a single-node ClickHouse instance serves analytical queries. Zabbix monitors the deployment. Availability across physical hosts is not a goal.

The application repository owns Go source code, service Dockerfiles, database migrations, local-development examples, and any explicit development seed scripts. A separate infrastructure repository owns k3s manifests, deployment settings, ConfigMaps, Secret references, storage, ingress, and GitOps configuration. Do not add cluster manifests here unless the repository's established layout explicitly calls for them.

Treat the existing code and documentation as authoritative for actual service names, APIs, modules, build commands, and directory structure. This file describes intended conventions; do not invent missing services or change established patterns just to match an example below.

## Service boundaries and communication

- Keep each service's entry point, configuration, API, migrations, and data ownership identifiable. Share Go packages only for genuinely common, stable code; avoid importing one service's internal implementation from another.
- For service-to-service calls, use configured URLs based on Kubernetes Service DNS, for example `http://users-api:8080`. For dependencies, use service names such as `postgres`, `redis`, and `clickhouse` with their configured ports. Never embed Pod IPs, VM IPs, or cluster-specific addresses in source code.
- Give outbound requests explicit timeouts, propagate cancellation with `context.Context`, and handle unavailable dependencies with clear errors. Add retries only where the operation is safe to repeat.
- The cluster's ingress exposes public HTTP(S) endpoints. Internal service and database endpoints remain internal unless a specific requirement says otherwise.

## Configuration and secrets

- Each service loads a mounted YAML file (default path `/etc/app/config.yaml`; make the path overridable for local development) for nonsecret settings, then loads secrets from environment variables. Validate required settings on startup and fail with an actionable error. Never log secret values.
- The infrastructure repository supplies environment-specific YAML via a Kubernetes ConfigMap and secret environment variables via native Kubernetes Secrets. Production secrets are created and updated through authorized cluster administration, not committed to either repository or passed through application CI.
- Keep a documented `config.example.yaml` or equivalent per service with safe values and the required environment variable names. Local overrides and credential files must be ignored by Git. Never commit real tokens, passwords, kubeconfigs, or rendered Secret manifests.
- Configuration examples may show names like `postgres` and `users-api`, but local development must permit host and port overrides.

## Data and migrations

- Give each service explicit ownership of its PostgreSQL database or schema and migrations; do not let unrelated services modify its tables. Use Goose SQL migrations where PostgreSQL schema changes are needed. Make each migration forward and rollback behavior explicit, and review destructive changes carefully.
- For the initial single-replica setup, a service may run its own migrations during startup before accepting traffic. If replicas or multiple concurrent starts are introduced, add migration coordination or move migrations into a dedicated deployment Job before scaling.
- Treat ClickHouse as the analytical database and keep its schema changes separate from PostgreSQL migrations. Use Redis only with an explicit purpose and document persistence expectations for each use.
- Keep test data generation separate from production startup. A small idempotent Python seed script is suitable for development; require an explicit target and make it hard to run against production accidentally. Do not assume a live cluster is available in CI.

## Development and delivery

- Work within the repository's actual Go module layout. Keep code formatted with `gofmt`, add focused tests for behavior that can regress, and run the relevant `go test` commands for changed modules or packages. Report any checks that could not run.
- Each deployable service gets its own container image. GitHub Actions builds and tests changed services and publishes versioned images to GHCR. Deployment changes go through the separate infrastructure repository and its GitOps workflow; application CI does not need cluster credentials or production secrets.
- Avoid introducing a new platform component, configuration framework, or shared library for a small problem without a concrete need. Document commands and assumptions when adding a service or a new dependency.

## Before changing code

Inspect the relevant service, its README, module files, and any more specific `AGENTS.md` instructions. State assumptions when the repository does not yet define an API or service contract. Keep changes scoped to the requested service and update examples or documentation when configuration keys or environment variables change.
