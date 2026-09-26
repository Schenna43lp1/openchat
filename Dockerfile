# syntax=docker/dockerfile:1
# Dockerfile for building and running the Open chat application.
FROM golang:1.27-alpine AS build

WORKDIR /src

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download
RUN go mod verify

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/openchat .

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

COPY --from=build /out/openchat /app/openchat
COPY --from=build /src/templates /app/templates
COPY --from=build /src/static /app/static

EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/app/openchat"]
