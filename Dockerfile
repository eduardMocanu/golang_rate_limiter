# ---- build stage ----
FROM golang:1.27-alpine AS build

WORKDIR /src

# Copy just the dependency files first. Docker caches each step, so as long as
# these two files are unchanged it will reuse the downloaded modules instead of
# fetching them again on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary with no C library dependencies, so it
# can run in an image that contains almost nothing.
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

# ---- run stage ----
FROM alpine:3.20

# Run as a non-root user.
RUN adduser -D -u 10001 app
USER app

COPY --from=build /out/server /server

EXPOSE 8080
ENTRYPOINT ["/server"]
