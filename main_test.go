package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

func TestCreateUser(t *testing.T) {
	initDB()
	defer db.Close()

	// Create a request body with user data
	userData := User{
		Username: "vishnu",
		Email:    "vishnu@gmail.com",
		Password: "password",
	}
	jsonData, err := json.Marshal(userData)
	if err != nil {
		t.Fatal(err)
	}

	// Create a request to create a new user
	req, err := http.NewRequest("POST", "/users", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Create a response recorder to record the response
	rr := httptest.NewRecorder()

	// Call the handler function (CreateUser) with the request and response recorder
	CreateUser(rr, req)

	// Check the status code of the response
	assert.Equal(t, http.StatusOK, rr.Code, "handler returned wrong status code")

	// Decode the response body
	var user User
	if err := json.NewDecoder(rr.Body).Decode(&user); err != nil {
		t.Errorf("failed to decode response body: %v", err)
		return
	}

	// Check if the returned user matches the created user
	assert.Equal(t, userData.Username, user.Username, "username mismatch")
	assert.Equal(t, userData.Email, user.Email, "email mismatch")
}

func TestGetUser(t *testing.T) {
	initDB()
	defer db.Close()

	// Insert a user into the database for testing
	_, err := db.Exec("INSERT INTO users (username, email, password_hash) VALUES ($1, $2, $3)", "vishnu", "vishnu@gmail.com", "password")
	if err != nil {
		t.Fatal(err)
	}

	// Create a request to get the user
	req, err := http.NewRequest("GET", "/users/1", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create a response recorder to record the response
	rr := httptest.NewRecorder()

	// Call the handler function (getUser) with the request and response recorder
	getUser(rr, req)

	// Check the status code of the response
	assert.Equal(t, http.StatusOK, rr.Code, "handler returned wrong status code")

	// Decode the response body
	var user User
	if err := json.NewDecoder(rr.Body).Decode(&user); err != nil {
		t.Errorf("failed to decode response body: %v", err)
		return
	}

	// Check the response body to verify the user
	expectedUser := User{ID: 1, Username: "vishnu", Email: "vishnu@gmail.com"}
	assert.Equal(t, expectedUser, user, "user mismatch")
}

func TestSendMessage(t *testing.T) {
	initDB()
	defer db.Close()

	// Create a message
	message := Message{SenderID: 1, RecipientID: 2, Text: "Hello"}

	// Marshal the message to JSON
	jsonData, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	// Create a request to send the message
	req, err := http.NewRequest("POST", "/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	// Create a response recorder to record the response
	rr := httptest.NewRecorder()

	// Call the handler function (sendMessage) with the request and response recorder
	sendMessage(rr, req)

	// Check the status code of the response
	assert.Equal(t, http.StatusCreated, rr.Code, "handler returned wrong status code")
}

func TestHandleWebSocket(t *testing.T) {
	initDB()
	defer db.Close()

	// Create a WebSocket request
	req := httptest.NewRequest("GET", "/ws/1", nil)
	w := httptest.NewRecorder()

	// Mock WebSocket connection
	mockConn, _, _ := websocket.DefaultDialer.DialContext(context.Background(), "ws://localhost/ws/1", nil)
	defer mockConn.Close()

	// Upgrade the connection
	ws, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Call the WebSocket handler
	go handleWebSocket(w, req)

	// Send a message through the WebSocket connection
	message := Message{SenderID: 1, RecipientID: 2, Text: "Hello"}
	if err := ws.WriteJSON(message); err != nil {
		t.Fatal(err)
	}
}

func initDB() {
	dbinfo := "user=postgres password=postgres dbname=chatdb sslmode=disable"
	db, _ = sql.Open("postgres", dbinfo)
	err := db.Ping()
	if err != nil {
		log.Fatal(err)
	}
}



func redisKey(userID int) string {
	return "user:" + strconv.Itoa(userID) + ":session"
}
