FROM golang:1.26-alpine AS builder

WORKDIR /src

RUN apk add --no-cache build-base

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/autopilot ./cmd/autopilot

FROM alpine:latest AS runner

RUN apk add --no-cache ca-certificates libgcc

COPY --from=builder /out/autopilot /autopilot

ENTRYPOINT ["/autopilot"]