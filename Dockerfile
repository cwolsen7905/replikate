# syntax=docker/dockerfile:1

# ---- build stage ----
FROM golang:1.23 AS build
WORKDIR /src

# Copy the full source first so `go mod tidy` can resolve imports. Once you
# commit a go.sum, you can switch to copying go.mod/go.sum and running
# `go mod download` here for better layer caching.
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/replikate ./cmd

# ---- runtime stage ----
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=build /out/replikate /replikate
USER 65532:65532
ENTRYPOINT ["/replikate"]
