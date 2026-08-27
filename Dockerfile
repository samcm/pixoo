FROM golang:1.24-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /pixoo ./cmd/pixoo

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /pixoo /pixoo

EXPOSE 6464

ENTRYPOINT ["/pixoo"]
CMD ["--config", "/config.yaml"]
