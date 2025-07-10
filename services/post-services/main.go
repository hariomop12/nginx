package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"
)

var (
	dbPool *pgxpool.Pool
	nc     *nats.Conn
)

type Post struct {
	ID      uuid.UUID `json:"id"`
	UserID  uuid.UUID `json:"user_id"`
	Title   string    `json:"title"`
	Content string    `json:"content"`
}

type CreatePostRequest struct {
	Title   string `json:"title" binding:"required"`
	Content string `json:"content" binding:"required"`
}

type PostEvent struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

func main() {
	// Load environment variables from .env file for local development
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	var err error
	// --- DB Connection ---
	DATABASE_URL := os.Getenv("DATABASE_URL")
	if DATABASE_URL == "" {
		log.Fatal("DATABASE_URL environment variable is not set")
	}
	dbPool, err = pgxpool.Connect(context.Background(), os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer dbPool.Close()
	log.Println("Connected to database")

	// --- NATS Connection ---
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		log.Fatal("NATS_URL environment variable is not set")
	}
	nc, err = nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("Unable to connect to NATS: %v\n", err)
	}
	defer nc.Close()
	log.Println("Connected to NATS")

	// --- Gin Router ---
	r := gin.Default()
	r.POST("/posts", createPostHandler)
	// r.GET("/posts/:id", getPostByIdHandler)  // TODO: Implement this handler
	// r.GET("/posts", getAllPostsHandler)     // TODO: Implement this handler
	r.Run(":8082") // Run on port 8082

}

func createPostHandler(c *gin.Context) {
	var req CreatePostRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(
			http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// KrakenD will pass the JWT claims as headers, prefixed with 'X-Krakend-'

	userIDstr := c.GetHeader("X-Krakend-Sub")
	userID, err := uuid.Parse(userIDstr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user ID"})
		return
	}
	newPost := Post{
		ID:      uuid.New(),
		UserID:  userID,
		Title:   req.Title,
		Content: req.Content,
	}

	_, err = dbPool.Exec(context.Background(),
		"INSERT INTO posts (id, user_id, title, content) VALUES ($1, $2, $3, $4)",
		newPost.ID, newPost.UserID,
		newPost.Title, newPost.Content)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create post"})
		return
	}
	log.Printf("Post created: %v", newPost)

	// Publish event to NATS
	event := PostEvent{
		ID:      newPost.ID.String(),
		Title:   newPost.Title,
		Content: newPost.Content,
	}

	eventBytes, err := json.Marshal(event)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to marshal event"})
		return
	}

	// Publish to NATS
	err = nc.Publish("post.updated", eventBytes)
	if err != nil {
		log.Printf("Failed to publish event: %v", err)
	}
	log.Printf("Post created event published: %v", event)
	c.JSON(http.StatusCreated, newPost)
}
