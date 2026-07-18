# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.23 AS build
WORKDIR /src

# Download dependencies first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/replikate ./cmd

# ---- runtime stage ----
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/replikate /replikate
USER 65532:65532
ENTRYPOINT ["/replikate"]
