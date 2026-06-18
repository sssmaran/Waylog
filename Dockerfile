# ---- shared build stage ----
FROM golang:1.24-alpine AS builder
WORKDIR /src
COPY go.mod go.sum go.work ./
COPY pkg/go.mod ./pkg/
COPY pkg/transport/kafka/go.mod pkg/transport/kafka/go.sum ./pkg/transport/kafka/
RUN go mod download
COPY . .

# ---- per-binary targets ----
FROM builder AS build-ingest
RUN CGO_ENABLED=0 go build -o /bin/ingest ./cmd/ingest

FROM builder AS build-api-gateway
RUN CGO_ENABLED=0 go build -o /bin/api-gateway ./examples/cmd/api-gateway

FROM builder AS build-checkout-demo
RUN CGO_ENABLED=0 go build -o /bin/checkout-demo ./examples/cmd/checkout-demo

FROM builder AS build-payment-demo
RUN CGO_ENABLED=0 go build -o /bin/payment-demo ./examples/cmd/payment-demo

FROM builder AS build-db-demo
RUN CGO_ENABLED=0 go build -o /bin/db-demo ./examples/cmd/db-demo

# ---- minimal runtime images ----
FROM alpine:3.21 AS ingest
RUN apk add --no-cache ca-certificates
COPY --from=build-ingest /bin/ingest /bin/ingest
EXPOSE 8080
ENTRYPOINT ["/bin/ingest"]

FROM alpine:3.21 AS api-gateway
RUN apk add --no-cache ca-certificates
COPY --from=build-api-gateway /bin/api-gateway /bin/api-gateway
EXPOSE 9081
ENTRYPOINT ["/bin/api-gateway"]

FROM alpine:3.21 AS checkout-demo
RUN apk add --no-cache ca-certificates
COPY --from=build-checkout-demo /bin/checkout-demo /bin/checkout-demo
EXPOSE 9082
ENTRYPOINT ["/bin/checkout-demo"]

FROM alpine:3.21 AS payment-demo
RUN apk add --no-cache ca-certificates
COPY --from=build-payment-demo /bin/payment-demo /bin/payment-demo
EXPOSE 9083
ENTRYPOINT ["/bin/payment-demo"]

FROM alpine:3.21 AS db-demo
RUN apk add --no-cache ca-certificates
COPY --from=build-db-demo /bin/db-demo /bin/db-demo
EXPOSE 9084
ENTRYPOINT ["/bin/db-demo"]
