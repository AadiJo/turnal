FROM golang:1.26.4-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/turnal-collector ./cmd/turnal-collector
RUN mkdir -p /out/data && chmod 0700 /out/data

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/turnal-collector /app/turnal-collector
COPY --from=build --chown=65532:65532 /out/data /app/data
VOLUME ["/app/data"]
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/turnal-collector"]
