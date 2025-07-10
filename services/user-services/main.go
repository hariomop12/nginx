package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/joho/godotenv"
	"github.com/lestrrat-go/jwx/jwk"
	"golang.org/x/crypto/bcrypt"
)

var (
	dbPool    *pgxpool.Pool
	rsaKey    *rsa.PrivateKey
	rsaKeyJWK jwk.Key
	jwkKeySet jwk.Set
)

type User struct {
	ID       uuid.UUID `json:"id"`
	Username string    `json:"username"`
	Password string    `json:"-"`
}

type RegisterRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

type loginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required,min=8"`
}

func main() {
	// Load environment variables from .env file for local development
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using system environment variables")
	}

	var err error

	databaseUrl := os.Getenv("DATABASE_URL")
	dbPool, err = pgxpool.Connect(context.Background(), databaseUrl)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer dbPool.Close()

	rsaKey, err = rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("Error generating RSA key: %v\n", err)
	}

	rsaKeyJWK, err = jwk.New(rsaKey.PublicKey)
	if err != nil {
		log.Fatalf("Error creating JWK from RSA key: %v\n", err)
	}
	jwk.AssignKeyID(rsaKeyJWK)
	rsaKeyJWK.Set("alg", "RS256")

	jwkKeySet = jwk.NewSet()
	jwkKeySet.Add(rsaKeyJWK)

	log.Println("RSA key pair for JWT generated.")

	r := gin.Default()
	r.POST("/register", registerHandler)
	r.POST("/login", loginHandler)
	r.GET("/.jwk", jwkHandler)
	r.GET("/health", healthCheckHandler)

	r.Run(":8080")
}

func registerHandler(c *gin.Context) {
	var req RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to hash password"})
		return
	}

	newUser := User{
		ID:       uuid.New(),
		Username: req.Email,
		Password: string(hashedPassword),
	}

	_, err = dbPool.Exec(context.Background(),
		"INSERT INTO users (id, username, password) VALUES ($1, $2, $3)",
		newUser.ID, newUser.Username, newUser.Password)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register user"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"id": newUser.ID, "email": newUser.Username})
}

func loginHandler(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var user User
	err := dbPool.QueryRow(
		context.Background(),
		"SELECT id, username, password FROM users WHERE username = $1",
		req.Email,
	).Scan(&user.ID, &user.Username, &user.Password)

	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid email or password"})
		return
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub":   user.ID,
		"email": user.Username,
		"roles": []string{"user"},
		"exp":   time.Now().Add(time.Hour * 24).Unix(),
	})
	token.Header["kid"] = rsaKeyJWK.KeyID()

	tokenString, err := token.SignedString(rsaKey)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create token"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"token": tokenString})
}

func jwkHandler(c *gin.Context) {
	c.JSON(http.StatusOK, jwkKeySet)
}

func healthCheckHandler(c *gin.Context) {
	if err := dbPool.Ping(context.Background()); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "unhealthy", "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "healthy"})
}
