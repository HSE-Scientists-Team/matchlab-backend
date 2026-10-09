FROM golang:1.27.1-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /media-bin ./cmd/media

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /etc/app
COPY --from=build /media-bin /app/media
ENTRYPOINT ["/app/media"]
