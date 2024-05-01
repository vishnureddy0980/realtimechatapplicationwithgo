package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

const (
	DB_USER     = "postgres"
	DB_PASSWORD = "postgres"
	DB_NAME     = "chatdb"
)

var (
	db       *sql.DB
	redisCli *redis.Client
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}
	clients = make(map[string]*websocket.Conn)
	lock    = sync.RWMutex{}
)

type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"-"`
}

type Message struct {
	SenderID    int    `json:"sender_id"`
	RecipientID int    `json:"recipient_id"`
	Text        string `json:"text"`
}

func main() {
	// Initialize database connection
	dbinfo := fmt.Sprintf("user=%s password=%s dbname=%s sslmode=disable",
		DB_USER, DB_PASSWORD, DB_NAME)
	var err error
	db, err = sql.Open("postgres", dbinfo)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		log.Fatal(err)
	}

	// Initialize Redis client
	redisCli = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // Redis server address
		Password: "",                // No password
		DB:       0,                 // Default DB
	})

	_, err = redisCli.Ping(context.Background()).Result()
	if err != nil {
		log.Fatal("Redis connection failed:", err)
	}

	// Initialize router
	r := mux.NewRouter()

	// API routes
	r.HandleFunc("/users", CreateUser).Methods("POST")
	r.HandleFunc("/users/{id}", getUser).Methods("GET")
	r.HandleFunc("/messages", sendMessage).Methods("POST")

	// WebSocket route
	r.HandleFunc("/ws/{userID}", handleWebSocket)

	// Start the server
	fmt.Println("Server started on port 8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

// Create a new user
func CreateUser(w http.ResponseWriter, r *http.Request) {
	var user User
	err := json.NewDecoder(r.Body).Decode(&user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Hash the password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	// Insert user into database
	err = db.QueryRow("INSERT INTO users (username, email, password_hash) VALUES ($1, $2, $3) RETURNING user_id", user.Username, user.Email, string(hashedPassword)).Scan(&user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Cache user session in Redis
	err = setUserSession(user.ID)
	if err != nil {
		http.Error(w, "Failed to cache user session", http.StatusInternalServerError)
		return
	}

	// Return the created user
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// Get user by ID
func getUser(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	id := params["id"]

	// Check if user session is cached in Redis
	userSession, err := getUserSession(id)
	if err != nil {
		http.Error(w, "Failed to get user session", http.StatusInternalServerError)
		return
	}
	if userSession != nil {
		// User session found in cache, return user
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(*userSession)
		return
	}

	// Fetch user from database
	var user User
	err = db.QueryRow("SELECT user_id, username, email FROM users WHERE user_id = $1", id).Scan(&user.ID, &user.Username, &user.Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Cache user session in Redis
	err = setUserSession(user.ID)
	if err != nil {
		log.Println("Failed to cache user session:", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

// Send a new message
func sendMessage(w http.ResponseWriter, r *http.Request) {
	var message Message
	err := json.NewDecoder(r.Body).Decode(&message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Insert message into database
	_, err = db.Exec("INSERT INTO messages (sender_id, receiver_id, text) VALUES ($1, $2, $3)",
		message.SenderID, message.RecipientID, message.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Cache recent message in Redis
	if err := cacheRecentMessage(message); err != nil {
		log.Println("Failed to cache recent message:", err)
	}

	w.WriteHeader(http.StatusCreated)
}

// Handle WebSocket connections
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userID"]

	// Upgrade HTTP connection to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	defer conn.Close()

	lock.Lock()
	clients[userID] = conn
	lock.Unlock()

	for {
		var msg Message
		err := conn.ReadJSON(&msg)
		if err != nil {
			log.Printf("error reading JSON message: %v", err)
			break
		}

		// Cache recent message in Redis
		if err := cacheRecentMessage(msg); err != nil {
			log.Println("Failed to cache recent message:", err)
		}

		recipientID := fmt.Sprintf("%d", msg.RecipientID)
		lock.RLock()
		recipientConn, ok := clients[recipientID]
		lock.RUnlock()

		if ok {
			err := recipientConn.WriteJSON(msg)
			if err != nil {
				log.Printf("error writing JSON message: %v", err)
				break
			}
		} else {
			log.Printf("recipient client not found: %s", recipientID)
		}
	}

	lock.Lock()
	delete(clients, userID)
	lock.Unlock()
}

// Cache user session in Redis
func setUserSession(userID int) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:session", userID)
	return redisCli.Set(ctx, key, "active", time.Hour).Err()
}

// Get user session from Redis
func getUserSession(userID string) (*User, error) {
	ctx := context.Background()
	key := fmt.Sprintf("user:%s:session", userID)
	_, err := redisCli.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil // User session not found
	} else if err != nil {
		return nil, err
	}

	// Fetch user from database
	var user User
	err = db.QueryRow("SELECT user_id, username, email FROM users WHERE user_id = $1", userID).Scan(&user.ID, &user.Username, &user.Email)
	if err != nil {
		return nil, err
	}

	return &user, nil
}

// Cache recent message in Redis
func cacheRecentMessage(msg Message) error {
	ctx := context.Background()
	key := "recent_messages"
	if err := redisCli.LPush(ctx, key, fmt.Sprintf("%v", msg)).Err(); err != nil {
		return err
	}
	return nil
}
