package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

var (
	ctx       = context.Background()
	redisCli  *redis.Client
	amqpConn  *amqp.Connection
	amqpCh    *amqp.Channel
	statusQ   amqp.Queue
	ordersQ   amqp.Queue
	adminUser = "admin"
	adminPass = "adminpass"
)

type Mission struct {
	ID        string    `json:"id"`
	Payload   any       `json:"payload"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type StatusMessage struct {
	MissionID string `json:"mission_id"`
	Status    string `json:"status"`
	SoldierID string `json:"soldier_id"`
	Token     string `json:"token"`
	Detail    string `json:"detail,omitempty"`
	Ts        int64  `json:"ts"`
}

func main() {
	// Env
	// Service knows where Redis, RabbitMQ are and which port to listen on.
	rabbitURL := getenv("RABBITMQ_URL", "amqp://guest:guest@rabbitmq:5672/")
	redisAddr := getenv("REDIS_ADDR", "redis:6379")
	port := getenv("COMMANDER_PORT", "8080")

	// Redis
	redisCli = redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})
	if err := redisCli.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping failed: %v", err)
	}
	log.Println("Connected to Redis")

	// RabbitMQ
	var err error
	amqpConn, err = amqp.Dial(rabbitURL)
	if err != nil {
		log.Fatalf("failed to connect to rabbitmq: %v", err)
	}
	amqpCh, err = amqpConn.Channel()
	if err != nil {
		log.Fatalf("failed to open amqp channel: %v", err)
	}
	ordersQ, err = amqpCh.QueueDeclare("orders_queue", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("declare orders_queue: %v", err)
	}
	statusQ, err = amqpCh.QueueDeclare("status_queue", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("declare status_queue: %v", err)
	}
	log.Println("Connected to RabbitMQ and declared queues")

	// Start consumer
	go consumeStatusQueue()

	router := gin.Default()
	router.Use(cors.Default())

	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "Commander API is running",
		})
	})

	router.POST("/missions", createMissionHandler)
	router.GET("/missions/:id", getMissionHandler)
	router.GET("/missions", listMissionsHandler)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":  "ok",
			"redis":   redisCli.Ping(ctx).Err() == nil,
			"rabbit":  amqpConn != nil,
		})
	})

	// Token issue endpoint
	router.POST("/token/issue", issueTokenHandler)

	// Admin-only token list
	admin := router.Group("/admin", gin.BasicAuth(gin.Accounts{adminUser: adminPass}))
	admin.GET("/tokens", listTokensHandler)

	log.Printf("Commander listening on :%s", port)
	router.Run(":" + port)
}

// validateToken checks if token exists and belongs to given soldier ID.
func validateToken(token string, soldierID string) bool {
	if token == "" || soldierID == "" {
		return false
	}

	key := fmt.Sprintf("token:%s", token)
	owner, err := redisCli.Get(ctx, key).Result()
	if err != nil {
		log.Printf("token validation failed: %v", err)
		return false
	}

	if owner != soldierID {
		log.Printf("token mismatch: owner=%s, soldier=%s", owner, soldierID)
		return false
	}

	return true
}

func consumeStatusQueue() {
	msgs, err := amqpCh.Consume(statusQ.Name, "", true, false, false, false, nil)
	if err != nil {
		log.Fatalf("consume status queue: %v", err)
	}
	log.Println("Started consuming status_queue")
	for d := range msgs {
		var s StatusMessage

		if err := json.Unmarshal(d.Body, &s); err != nil {
			log.Printf("invalid status message: %v", err)
			continue
		}

		// Use the new validateToken function
		if !validateToken(s.Token, s.SoldierID) {
			log.Printf("invalid token from soldier %s", s.SoldierID)
			continue
		}

		if err := updateMissionStatus(s.MissionID, s.Status); err != nil {
			log.Printf("failed update mission status: %v", err)
		} else {
			log.Printf("Mission %s updated to %s by %s", s.MissionID, s.Status, s.SoldierID)
		}
	}
}

func createMissionHandler(c *gin.Context) {
	var payload any
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	id := uuid.New().String()
	m := &Mission{
		ID:        id,
		Payload:   payload,
		Status:    "QUEUED", 
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	bs, _ := json.Marshal(m)
	if err := redisCli.Set(ctx, fmt.Sprintf("mission:%s", id), bs, 0).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed store mission"})
		return
	}

	orderMsg := map[string]any{"mission_id": id, "payload": payload, "ts": time.Now().Unix()}
	body, _ := json.Marshal(orderMsg)
	if err := amqpCh.Publish("", ordersQ.Name, false, false, amqp.Publishing{
		ContentType: "application/json",
		Body:        body,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed publish order"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"mission_id": id, "status": "QUEUED"})
}

func getMissionHandler(c *gin.Context) {
	id := c.Param("id")
	key := fmt.Sprintf("mission:%s", id)
	val, err := redisCli.Get(ctx, key).Result()
	if err == redis.Nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "mission not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}
	var m Mission
	if err := json.Unmarshal([]byte(val), &m); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "unmarshal error"})
		return
	}
	c.JSON(http.StatusOK, m)
}

func listMissionsHandler(c *gin.Context) {
	iter := redisCli.Scan(ctx, 0, "mission:*", 100).Iterator()
	missions := []Mission{}

	for iter.Next(ctx) {
		val, _ := redisCli.Get(ctx, iter.Val()).Result()
		var m Mission
		if err := json.Unmarshal([]byte(val), &m); err == nil {
			missions = append(missions, m)
		}
	}

	sort.Slice(missions, func(i, j int) bool {
		return missions[i].UpdatedAt.After(missions[j].UpdatedAt)
	})

	c.JSON(http.StatusOK, missions)
}

func updateMissionStatus(id, status string) error {
	key := fmt.Sprintf("mission:%s", id)
	val, err := redisCli.Get(ctx, key).Result()
	if err != nil {
		return err
	}
	var m Mission
	if err := json.Unmarshal([]byte(val), &m); err != nil {
		return err
	}
	m.Status = status
	m.UpdatedAt = time.Now()
	bs, _ := json.Marshal(m)
	return redisCli.Set(ctx, key, bs, 0).Err()
}

func issueTokenHandler(c *gin.Context) {
	var req struct {
		SoldierID string `json:"soldier_id"`
		Secret    string `json:"secret"`
	}

	if err := c.ShouldBindJSON(&req); err != nil || req.SoldierID == "" || req.Secret == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad request"})
		return
	}

	expected := getenv("WORKER_BOOTSTRAP_SECRET", "bootstrapsecret")
	if req.Secret != expected {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid secret"})
		return
	}

	token := uuid.New().String()
	ttlSecs := getenvInt("TOKEN_TTL_SECS", 30)
	key := fmt.Sprintf("token:%s", token)

	if err := redisCli.Set(ctx, key, req.SoldierID, time.Duration(ttlSecs)*time.Second).Err(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed store token"})
		return
	}

	meta := map[string]any{
		"token":      token,
		"soldier_id": req.SoldierID,
		"issued_at":  time.Now().Unix(),
		"ttl":        ttlSecs,
	}

	_ = redisCli.Set(ctx, "tokenmeta:"+token, toJson(meta), time.Duration(ttlSecs)*time.Second).Err()

	c.JSON(http.StatusOK, gin.H{"token": token, "ttl_secs": ttlSecs})
}

func listTokensHandler(c *gin.Context) {
	iter := redisCli.Scan(ctx, 0, "tokenmeta:*", 100).Iterator()
	list := []map[string]any{}
	for iter.Next(ctx) {
		v, _ := redisCli.Get(ctx, iter.Val()).Result()
		var m map[string]any
		_ = json.Unmarshal([]byte(v), &m)
		list = append(list, m)
	}
	c.JSON(http.StatusOK, list)
}

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

func toJson(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
