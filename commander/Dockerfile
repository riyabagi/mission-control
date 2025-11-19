FROM golang:1.25-alpine as build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o commander main.go

FROM alpine:3.18
RUN apk add --no-cache ca-certificates
WORKDIR /root/
COPY --from=build /app/commander .
ENV RABBITMQ_URL=amqp://guest:guest@rabbitmq:5672/
ENV REDIS_ADDR=redis:6379
ENV WORKER_BOOTSTRAP_SECRET=bootstrapsecret
EXPOSE 8080
CMD ["./commander"]
