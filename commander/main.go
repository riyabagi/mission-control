package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 1          // iterations
	argonMemory  = 64 * 1024  // 64 MB
	argonThreads = 4
	argonKeyLen  = 32
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
	ID           string     `json:"id"`
	Payload      any        `json:"payload"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	InProgressAt *time.Time `json:"in_progress_at,omitempty"`
	AssignedTo   string     `json:"assigned_to"`
	CommanderID  string     `json:"commander_id"`
}

type StatusMessage struct {
	MissionID string `json:"mission_id"`
	Status    string `json:"status"`
	SoldierID string `json:"soldier_id"`
	Token     string `json:"token"`
	Detail    string `json:"detail,omitempty"`
	Ts        int64  `json:"ts"`
}

type OrderMsg struct {
	MissionID string      `json:"mission_id"`
	Payload   interface{} `json:"payload"`
	Ts        int64       `json:"ts"`
}

type TokenIssueRequest struct {
	SoldierID string `json:"soldier_id"`
	Secret    string `json:"secret"`
}

type TokenIssueResponse struct {
	Token   string `json:"token"`
	TtlSecs int    `json:"ttl_secs"`
}

func main() {
	// Env
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

	// Direct exchange for targeted missions
	err = amqpCh.ExchangeDeclare(
		"mission_direct",
		"direct",
		true,
		false,
		false,
		false,
		nil,
	)

	if err != nil {
		log.Fatalf("failed to declare direct exchange: %v", err)
	}

	// Start consumer
	go consumeStatusQueue()

	router := gin.Default()
	router.Use(cors.Default())

	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "Commander API is running"})
	})

	router.POST("/missions", createMissionHandler)
	router.GET("/missions/:id", getMissionHandler)
	router.GET("/missions", listMissionsHandler)

	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
			"redis":  redisCli.Ping(ctx).Err() == nil,
			"rabbit": amqpConn != nil,
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

func hashSecret(secret string) string {
	salt := make([]byte, 16)

	if _, err := rand.Read(salt); err != nil {
		log.Fatalf("failed to generate salt: %v", err)
	}

	hash := argon2.IDKey([]byte(secret), salt, 1, 64*1024, 4, 32)
	final := append(salt, hash...)

	return base64.RawStdEncoding.EncodeToString(final)
}

func verifySecret(secret, encodedHash string) bool {
	data, err := base64.RawStdEncoding.DecodeString(encodedHash)
	if err != nil || len(data) < 48 {
		return false
	}

	salt := data[:16]
	storedHash := data[16:]
	newHash := argon2.IDKey([]byte(secret), salt, 1, 64*1024, 4, 32)

	return subtleCompare(newHash, storedHash)
}

func subtleCompare(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}

	var res byte
	for i := range a {
		res |= a[i] ^ b[i]
	}

	return res == 0
}

func verifyBootstrapSecret(given string) bool {
	envSecret := getenv("WORKER_BOOTSTRAP_SECRET", "bootstrapsecret")
	expectedHash := hashSecret(envSecret)
	return verifySecret(given, expectedHash)
}

func hashTokenSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func validateToken(token, soldierID string) bool {
	key := "token:" + soldierID

	storedHash, err := redisCli.Get(ctx, key).Result()
	if err != nil {
		return false
	}

	incomingHash := hashTokenSHA256(token)

	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(incomingHash)) == 1
}

func issueTokenHandler(c *gin.Context) {
	var req TokenIssueRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}

	if req.SoldierID == "" || req.Secret == "" {
		c.JSON(400, gin.H{"error": "missing fields"})
		return
	}

	if !verifyBootstrapSecret(req.Secret) {
		c.JSON(401, gin.H{"error": "invalid secret"})
		return
	}

	rawToken := uuid.New().String()
	hashed := hashTokenSHA256(rawToken)

	ttl := 30 * time.Second
	key := "token:" + req.SoldierID

	err := redisCli.Set(ctx, key, hashed, ttl).Err()
	if err != nil {
		c.JSON(500, gin.H{"error": "redis fail"})
		return
	}

	c.JSON(200, TokenIssueResponse{
		Token:   rawToken,
		TtlSecs: int(ttl.Seconds()),
	})
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

		if !validateToken(s.Token, s.SoldierID) {
			log.Printf("invalid token from soldier %s", s.SoldierID)
			continue
		}

		if err := updateMissionStatus(s.MissionID, s.Status, s.Ts); err != nil {
			log.Printf("failed update mission status: %v", err)
		} else {
			log.Printf("Mission %s updated to %s by %s", s.MissionID, s.Status, s.SoldierID)
		}
	}
}

func createMissionHandler(c *gin.Context) {
	var req struct {
		Target      string      `json:"target"`
		Payload     interface{} `json:"payload"`
		CommanderID string      `json:"commander_id"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON"})
		return
	}

	if req.Target == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "target is required"})
		return
	}

	if req.CommanderID == "" {
		req.CommanderID = "commander-1"
	}

	id := uuid.NewString()
	now := time.Now().UTC()

	m := Mission{
		ID:          id,
		Payload:     req.Payload,
		AssignedTo:  req.Target,
		Status:      "QUEUED",
		CreatedAt:   now,
		UpdatedAt:   now,
		CommanderID: req.CommanderID,
	}

	b, _ := json.Marshal(m)

	if err := redisCli.Set(ctx, "mission:"+id, b, 0).Err(); err != nil {
		log.Printf("redis set error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "redis error"})
		return
	}

	order := OrderMsg{
		MissionID: id,
		Payload:   req.Payload,
		Ts:        now.Unix(),
	}

	ob, _ := json.Marshal(order)

	err := amqpCh.Publish(
		"mission_direct",
		req.Target,
		false,
		false,
		amqp.Publishing{
			ContentType: "application/json",
			Body:        ob,
		},
	)

	if err != nil {
		log.Printf("publish order error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to publish mission"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"mission_id": id})
}

func getMissionHandler(c *gin.Context) {
	key := "mission:" + c.Param("id")

	val, err := redisCli.Get(ctx, key).Result()
	if err == redis.Nil {
		c.JSON(404, gin.H{"error": "mission not found"})
		return
	}

	var m Mission
	json.Unmarshal([]byte(val), &m)

	c.JSON(200, m)
}

func listMissionsHandler(c *gin.Context) {
	commanderFilter := c.Query("commander_id")

	iter := redisCli.Scan(ctx, 0, "mission:*", 100).Iterator()
	missions := []Mission{}

	for iter.Next(ctx) {
		val, err := redisCli.Get(ctx, iter.Val()).Result()
		if err != nil {
			log.Printf("redis get error: %v", err)
			continue
		}

		var m Mission
		if err := json.Unmarshal([]byte(val), &m); err != nil {
			log.Printf("unmarshal mission error: %v", err)
			continue
		}

		if commanderFilter != "" && m.CommanderID != commanderFilter {
			continue
		}

		missions = append(missions, m)
	}

	if err := iter.Err(); err != nil {
		log.Printf("redis scan error: %v", err)
	}

	sort.Slice(missions, func(i, j int) bool {
		return missions[i].CreatedAt.After(missions[j].CreatedAt)
	})

	c.JSON(http.StatusOK, missions)
}

func updateMissionStatus(id, status string, ts int64) error {
	key := "mission:" + id

	val, err := redisCli.Get(ctx, key).Result()
	if err != nil {
		return err
	}

	var m Mission
	if err := json.Unmarshal([]byte(val), &m); err != nil {
		return err
	}

	t := time.Now()
	if ts > 0 {
		t = time.Unix(ts, 0)
	}

	m.Status = status
	m.UpdatedAt = t

	if status == "IN_PROGRESS" && m.InProgressAt == nil {
		m.InProgressAt = &t
	}

	bs, _ := json.Marshal(m)
	return redisCli.Set(ctx, key, bs, 0).Err()
}

func listTokensHandler(c *gin.Context) {
	iter := redisCli.Scan(ctx, 0, "token:*", 100).Iterator()
	list := []map[string]any{}

	for iter.Next(ctx) {
		key := iter.Val()
		soldier := strings.TrimPrefix(key, "token:")

		hash, _ := redisCli.Get(ctx, key).Result()
		ttl, _ := redisCli.TTL(ctx, key).Result()

		list = append(list, map[string]any{
			"soldier_id": soldier,
			"token_hash": hash,
			"ttl_secs":   int(ttl.Seconds()),
		})
	}

	c.JSON(200, list)
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
