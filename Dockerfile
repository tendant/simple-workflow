FROM public.ecr.aws/docker/library/golang:1.25-alpine AS build

RUN apk update && \
  apk upgrade && \
  apk add --no-cache alpine-sdk git make openssh

WORKDIR /app

# Cache go mod dependencies
COPY go.mod go.sum ./

RUN go mod download

COPY . .

# Install goose for migrations
RUN go install github.com/pressly/goose/v3/cmd/goose@latest

FROM public.ecr.aws/docker/library/golang:1.25-alpine

WORKDIR /app

# Create app user
RUN addgroup -g 1000 app && adduser -D -u 1000 -G app app

# Copy migrations directory
COPY --from=build /app/migrations /app/migrations

# Copy goose binary
COPY --from=build /go/bin/goose /usr/local/bin/goose

# Set proper permissions
RUN chmod -R 755 /app/migrations && \
    chown -R app:app /app/

# Switch to non-root user
USER app

# Default command (can be overridden)
CMD ["goose", "--help"]
