package main

import (
	"context"
	"encoding/json"
	"log"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"
)

type PostEvent struct {
	ID      uuid.UUID `json:"id"`
	Title   string    `json:"title"`
	Content string    `json:"content"`
}

func main() {
	// Load environment variables from .env file for local development
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	// --- Database connection & Schema setup ---
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	dbpool, err := pgxpool.Connect(context.Background(), databaseURL)

	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer dbpool.Close()

	setupSchema(dbpool)

	// --- NATS connection ---
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222" // Default fallback
	}

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("Unable to connect to NATS: %v\n", err)
	}
	defer nc.Close()

	nc.Subscribe("post.updated", func(msg *nats.Msg) {
		var post PostEvent
		if err := json.Unmarshal(msg.Data, &post); err != nil {
			log.Printf("Invalid message format: %v", err)
			return
		}
		upsertPost(dbpool, post)
	})

	// --- Gin setup ---
	r := gin.Default()
	r.GET("/search", func(c *gin.Context) {
		query := c.Query("q")

		if query == "" {
			c.JSON(400, gin.H{"error": "Query parameter 'q' is required"})
			return
		}

		rows, err := dbpool.Query(context.Background(),
			`SELECT post_id FROM posts_search_index WHERE content_tsv @@ to_tsquery('english', $1)`, query)
		if err != nil {
			c.JSON(500, gin.H{"error": "search failed"})
			return
		}
		defer rows.Close()

		var postIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				c.JSON(500, gin.H{"error": "failed to scan result"})
				continue
			}
			postIDs = append(postIDs, id)
		}
		c.JSON(200, gin.H{"post_ids": postIDs})
	})

	r.Run(":8083")
}

func setupSchema(dbpool *pgxpool.Pool) {
	// Create the table
	_, err := dbpool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS posts_search_index (
			post_id UUID PRIMARY KEY,
			content TEXT,
			content_tsv TSVECTOR
		);
	`)
	if err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	// Create the GIN index on the tsvector column
	_, err = dbpool.Exec(context.Background(), `
		CREATE INDEX IF NOT EXISTS content_tsv_idx ON posts_search_index USING GIN(content_tsv);
	`)
	if err != nil {
		log.Fatalf("Failed to create GIN index: %v", err)
	}

	// Create a trigger to automatically update the tsvector column
	_, err = dbpool.Exec(context.Background(), `
		CREATE OR REPLACE FUNCTION update_tsv() RETURNS trigger AS $$
		BEGIN
			NEW.content_tsv := to_tsvector('english', NEW.content);
			RETURN NEW;
		END
		$$ LANGUAGE plpgsql;

		DROP TRIGGER IF EXISTS tsvector_update ON posts_search_index;
		CREATE TRIGGER tsvector_update BEFORE INSERT OR UPDATE
		ON posts_search_index FOR EACH ROW EXECUTE PROCEDURE update_tsv();
	`)
	if err != nil {
		log.Fatalf("Failed to create trigger: %v", err)
	}

	log.Println("Schema and GIN index are ready.")
}

// You would call this function from your NATS subscriber
func upsertPost(dbpool *pgxpool.Pool, post PostEvent) {
	// The trigger handles the 'content_tsv' column automatically!
	_, err := dbpool.Exec(context.Background(), `
		INSERT INTO posts_search_index (post_id, content)
		VALUES ($1, $2)
		ON CONFLICT (post_id)
		DO UPDATE SET content = EXCLUDED.content;
	`, post.ID, post.Content)

	if err != nil {
		log.Printf("Failed to upsert post %s: %v", post.ID, err)
	} else {
		log.Printf("Successfully indexed post %s", post.ID)
	}
}
