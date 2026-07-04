# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build

WORKDIR /src

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
