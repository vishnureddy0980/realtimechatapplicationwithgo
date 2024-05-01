package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

func TestCreateUser(t *testing.T) {
	initDB()
	defer db.Close()

	userData := User{
		Username: "vishnu",
		Email:    "vishnu@gmail.com",
		Password: "password",
	}
	jsonData, err := json.Marshal(userData)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("POST", "/users", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()

	CreateUser(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "handler returned wrong status code")

	var user User
	if err := json.NewDecoder(rr.Body).Decode(&user); err != nil {
		t.Errorf("failed to decode response body: %v", err)
		return
	}

	assert.Equal(t, userData.Username, user.Username, "username mismatch")
	assert.Equal(t, userData.Email, user.Email, "email mismatch")
}

func TestGetUser(t *testing.T) {
	initDB()
	defer db.Close()

	_, err := db.Exec("INSERT INTO users (username, email, password_hash) VALUES ($1, $2, $3)", "vishnu", "vishnu@gmail.com", "password")
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("GET", "/users/1", nil)
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()

	getUser(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code, "handler returned wrong status code")

	var user User
	if err := json.NewDecoder(rr.Body).Decode(&user); err != nil {
		t.Errorf("failed to decode response body: %v", err)
		return
	}

	expectedUser := User{ID: 1, Username: "vishnu", Email: "vishnu@gmail.com"}
	assert.Equal(t, expectedUser, user, "user mismatch")
}

func TestSendMessage(t *testing.T) {
	initDB()
	defer db.Close()

	message := Message{SenderID: 1, RecipientID: 2, Text: "Hello"}

	jsonData, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest("POST", "/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()

	sendMessage(rr, req)

	assert.Equal(t, http.StatusCreated, rr.Code, "handler returned wrong status code")
}

func TestHandleWebSocket(t *testing.T) {
	initDB()
	defer db.Close()

	req := httptest.NewRequest("GET", "/ws/1", nil)
	w := httptest.NewRecorder()

	mockConn, _, _ := websocket.DefaultDialer.DialContext(context.Background(), "ws://localhost/ws/1", nil)
	defer mockConn.Close()

	ws, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		t.Fatal(err)
	}

	go handleWebSocket(w, req)

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
