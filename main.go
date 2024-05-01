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

	redisCli = redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "",
		DB:       0,
	})

	_, err = redisCli.Ping(context.Background()).Result()
	if err != nil {
		log.Fatal("Redis connection failed:", err)
	}

	r := mux.NewRouter()

	r.HandleFunc("/users", CreateUser).Methods("POST")
	r.HandleFunc("/users/{id}", getUser).Methods("GET")
	r.HandleFunc("/messages", sendMessage).Methods("POST")

	r.HandleFunc("/ws/{userID}", handleWebSocket)

	fmt.Println("Server started on port 8080")
	log.Fatal(http.ListenAndServe(":8080", r))
}

func CreateUser(w http.ResponseWriter, r *http.Request) {
	var user User
	err := json.NewDecoder(r.Body).Decode(&user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Failed to hash password", http.StatusInternalServerError)
		return
	}

	err = db.QueryRow("INSERT INTO users (username, email, password_hash) VALUES ($1, $2, $3) RETURNING user_id", user.Username, user.Email, string(hashedPassword)).Scan(&user.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	err = setUserSession(user.ID)
	if err != nil {
		http.Error(w, "Failed to cache user session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func getUser(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	id := params["id"]

	userSession, err := getUserSession(id)
	if err != nil {
		http.Error(w, "Failed to get user session", http.StatusInternalServerError)
		return
	}
	if userSession != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(*userSession)
		return
	}

	var user User
	err = db.QueryRow("SELECT user_id, username, email FROM users WHERE user_id = $1", id).Scan(&user.ID, &user.Username, &user.Email)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	err = setUserSession(user.ID)
	if err != nil {
		log.Println("Failed to cache user session:", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(user)
}

func sendMessage(w http.ResponseWriter, r *http.Request) {
	var message Message
	err := json.NewDecoder(r.Body).Decode(&message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_, err = db.Exec("INSERT INTO messages (sender_id, receiver_id, text) VALUES ($1, $2, $3)",
		message.SenderID, message.RecipientID, message.Text)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := cacheRecentMessage(message); err != nil {
		log.Println("Failed to cache recent message:", err)
	}

	w.WriteHeader(http.StatusCreated)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	userID := mux.Vars(r)["userID"]

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

func setUserSession(userID int) error {
	ctx := context.Background()
	key := fmt.Sprintf("user:%d:session", userID)
	return redisCli.Set(ctx, key, "active", time.Hour).Err()
}

func getUserSession(userID string) (*User, error) {
	ctx := context.Background()
	key := fmt.Sprintf("user:%s:session", userID)
	_, err := redisCli.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	var user User
	err = db.QueryRow("SELECT user_id, username, email FROM users WHERE user_id = $1", userID).Scan(&user.ID, &user.Username, &user.Email)
	if err != nil {
		return nil, err
	}

	return &user, nil
}

func cacheRecentMessage(msg Message) error {
	ctx := context.Background()
	key := "recent_messages"
	if err := redisCli.LPush(ctx, key, fmt.Sprintf("%v", msg)).Err(); err != nil {
		return err
	}
	return nil
}
