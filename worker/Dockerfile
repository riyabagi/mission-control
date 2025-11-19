FROM golang:1.25-alpine as build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o worker main.go

FROM alpine:3.18
RUN apk add --no-cache ca-certificates
WORKDIR /root/
COPY --from=build /app/worker .
ENV RABBITMQ_URL=amqp://guest:guest@rabbitmq:5672/
ENV COMMANDER_URL=http://commander:8080
ENV WORKER_BOOTSTRAP_SECRET=bootstrapsecret
ENV WORKER_ID=soldier-demo
EXPOSE 0
CMD ["./worker"]
