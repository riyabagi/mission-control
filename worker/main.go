package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	ctx = context.Background()
)

type OrderMsg struct {
	MissionID string      `json:"mission_id"`
	Payload   interface{} `json:"payload"`
	Ts        int64       `json:"ts"`
}

type StatusMessage struct {
	MissionID string `json:"mission_id"`
	Status    string `json:"status"`
	SoldierID string `json:"soldier_id"`
	Token     string `json:"token"`
	Detail    string `json:"detail,omitempty"`
	Ts        int64  `json:"ts"`
}

type TokenResponse struct {
	Token   string `json:"token"`
	TtlSecs int    `json:"ttl_secs"`
}

func main() {
	// env
	rabbitURL := getenv("RABBITMQ_URL", "amqp://guest:guest@rabbitmq:5672/")
	commanderURL := getenv("COMMANDER_URL", "http://commander:8080")
	redisAddr := getenv("REDIS_ADDR", "redis:6379")

	workerID := getenv("WORKER_ID", "soldier-"+uuid.New().String()[:8])
	bootstrapSecret := getenv("WORKER_BOOTSTRAP_SECRET", "bootstrapsecret")
	concurrency := getenvInt("WORKER_CONCURRENCY", 1)

	// Redis client (optional)
	_ = redis.NewClient(&redis.Options{Addr: redisAddr})

	// Connect RabbitMQ
	conn, err := amqp.Dial(rabbitURL)
	if err != nil {
		log.Fatalf("failed connect rabbit: %v", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("channel error: %v", err)
	}
	defer conn.Close()

	// Declare worker-specific queue
	queueName := "orders_" + workerID
	q, err := ch.QueueDeclare(queueName, true, false, false, false, nil)
	if err != nil {
		log.Fatalf("queue declare: %v", err)
	}

	// Bind queue to mission_direct exchange using routing key = workerID
	err = ch.QueueBind(q.Name, workerID, "mission_direct", false, nil)
	if err != nil {
		log.Fatalf("queue bind: %v", err)
	}

	statusQ, err := ch.QueueDeclare("status_queue", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("queue declare: %v", err)
	}

	// request initial token
	token, ttl := requestToken(commanderURL, workerID, bootstrapSecret)
	log.Printf("Obtained token=%s ttl=%d", token, ttl)

	// token auto-rotation
	var tokenMu sync.RWMutex
	tokenVal := token
	ttlDur := time.Duration(ttl) * time.Second

	go func() {
		for {
			time.Sleep(ttlDur - 3*time.Second) // renew a bit early
			newTok, newTtl := requestToken(commanderURL, workerID, bootstrapSecret)

			tokenMu.Lock()
			tokenVal = newTok
			ttlDur = time.Duration(newTtl) * time.Second
			tokenMu.Unlock()

			log.Printf("Rotated token -> %s (ttl=%d)", newTok, newTtl)
		}
	}()

	// concurrency control
	sem := make(chan struct{}, concurrency)

	msgs, err := ch.Consume(queueName, "", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("consume orders: %v", err)
	}

	log.Println("Worker listening for orders...")

	for d := range msgs {
		var order OrderMsg
		if err := json.Unmarshal(d.Body, &order); err != nil {
			log.Printf("bad order msg: %v", err)
			continue
		}

		// acquire worker slot
		sem <- struct{}{}

		go func(ord OrderMsg) {
			defer func() { <-sem }()

			// publish IN_PROGRESS
			tokenMu.RLock()
			curToken := tokenVal
			tokenMu.RUnlock()

			publishStatus(ch, statusQ.Name, StatusMessage{
				MissionID: ord.MissionID,
				Status:    "IN_PROGRESS",
				SoldierID: workerID,
				Token:     curToken,
				Ts:        time.Now().Unix(),
			})

			// simulate execution
			delay := 5 + randInt(0, 10) // 5–15s
			log.Printf("[%s] executing mission %s for %ds", workerID, ord.MissionID, delay)
			time.Sleep(time.Duration(delay) * time.Second)

			// 90% chance success
			outcome := "COMPLETED"
			if randInt(1, 100) > 90 {
				outcome = "FAILED"
			}

			// ensure token still valid
			tokenMu.RLock()
			curToken = tokenVal
			tokenMu.RUnlock()

			publishStatus(ch, statusQ.Name, StatusMessage{
				MissionID: ord.MissionID,
				Status:    outcome,
				SoldierID: workerID,
				Token:     curToken,
				Ts:        time.Now().Unix(),
			})

			log.Printf("[%s] mission %s -> %s", workerID, ord.MissionID, outcome)

		}(order)
	}
}

// publishStatus sends message to status_queue
func publishStatus(ch *amqp.Channel, qname string, s StatusMessage) {
	b, _ := json.Marshal(s)

	err := ch.Publish("", qname, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        b,
	})

	if err != nil {
		log.Printf("publish status err: %v", err)
	}
}

// requestToken calls commander /token/issue
func requestToken(commanderURL, soldierID, secret string) (string, int) {
	url := fmt.Sprintf("%s/token/issue", commanderURL)
	body := map[string]string{
		"soldier_id": soldierID,
		"secret":     secret,
	}

	bs, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(bs))
	if err != nil {
		log.Printf("token request failed: %v", err)
		time.Sleep(2 * time.Second)
		return requestToken(commanderURL, soldierID, secret)
	}
	defer resp.Body.Close()

	var tr TokenResponse
	if resp.StatusCode != 200 {
		log.Printf("token request status %d, retrying", resp.StatusCode)
		time.Sleep(2 * time.Second)
		return requestToken(commanderURL, soldierID, secret)
	}

	_ = json.NewDecoder(resp.Body).Decode(&tr)
	return tr.Token, tr.TtlSecs
}

// helpers

func getenv(k, d string) string {
	v := os.Getenv(k)
	if v == "" {
		return d
	}
	return v
}

func getenvInt(k string, d int) int {
	v := os.Getenv(k)
	if v == "" {
		return d
	}

	var i int
	fmt.Sscanf(v, "%d", &i)

	if i == 0 {
		return d
	}
	return i
}

func randInt(min, max int) int {
	return min + int(time.Now().UnixNano())%(max-min+1)
}
