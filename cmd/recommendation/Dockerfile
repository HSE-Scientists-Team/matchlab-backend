FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Share the expensive common compilation layer across service images.
RUN CGO_ENABLED=0 go build -trimpath ./pkg/... ./internal/gen/...
ARG VCS_REF=unknown
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags "-X github.com/HSE-Scientists-Team/matchlab-backend/pkg/observability.Revision=${VCS_REF} -X github.com/HSE-Scientists-Team/matchlab-backend/pkg/observability.Version=${VERSION}" -trimpath -o /recommendation ./cmd/recommendation

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /etc/app
COPY --from=build /recommendation /recommendation
USER 65532:65532
ENTRYPOINT ["/recommendation"]
