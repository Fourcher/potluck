package main

// Phone alerts: when a task needs an answer, Potluck tells the paired iPhones through Apple's
// push service, straight from this computer (no server of anyone else's in between). The
// alert carries Yes / No / Reply buttons, so the person can answer without opening the app.
//
// Apple only delivers pushes signed with a key from the developer account the app was built
// with. The person makes one "Apple Push Notifications service" key in their developer account
// (Certificates, Identifiers & Profiles, Keys), downloads the .p8 file, and gives Potluck its
// path, the key's ID and their team ID in the control panel. Without it, the app still shows
// waiting questions whenever it is open.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// PushConfig is the developer account's push key, kept in the config.
type PushConfig struct {
	KeyPath string `json:"keyPath"`
	KeyID   string `json:"keyID"`
	TeamID  string `json:"teamID"`
	Topic   string `json:"topic"` // the app's bundle id
}

var apnsMu sync.Mutex
var apnsJWT struct {
	token string
	at    time.Time
	key   string
}

// apnsToken signs the short-lived token Apple asks for (ES256, renewed every 40 minutes).
func apnsToken(pc PushConfig) (string, error) {
	apnsMu.Lock()
	defer apnsMu.Unlock()
	if apnsJWT.token != "" && apnsJWT.key == pc.KeyID && time.Since(apnsJWT.at) < 40*time.Minute {
		return apnsJWT.token, nil
	}
	raw, err := os.ReadFile(pc.KeyPath)
	if err != nil {
		return "", fmt.Errorf("cannot read the push key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("the push key is not a .p8 key file")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("the push key could not be read: %w", err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return "", errors.New("the push key is not an Apple push key")
	}
	enc := base64.RawURLEncoding
	hdr, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": pc.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": pc.TeamID, "iat": time.Now().Unix()})
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	apnsJWT.token, apnsJWT.at, apnsJWT.key = signing+"."+enc.EncodeToString(sig), time.Now(), pc.KeyID
	return apnsJWT.token, nil
}

var apnsClient = &http.Client{Timeout: 20 * time.Second}

// pushQuestion alerts every paired phone that allowed notifications.
func (h *Host) pushQuestion(q *Question, s *Session) {
	c := h.cfg.Get()
	if c.Push == nil || c.Push.KeyPath == "" {
		return
	}
	s.mu.Lock()
	task := s.Title
	s.mu.Unlock()
	h.pushAlert(q, task)
}

// testSession marks the alert sent by "Send a test alert"; its answer is kept for the panel.
const testSession = "potluck-test"

// PushTest sends a Yes/No/Reply alert to every phone, to check alerts work end to end.
func (h *Host) PushTest() error {
	c := h.cfg.Get()
	if c.Push == nil || c.Push.KeyPath == "" {
		return errors.New("add your push key first")
	}
	n := 0
	for _, p := range c.Phones {
		if p.PushToken != "" {
			n++
		}
	}
	if n == 0 {
		return errors.New("no paired phone has allowed alerts yet: open Potluck on the phone and allow notifications")
	}
	h.mu.Lock()
	h.pushErr, h.pushTestAnswer = "", ""
	h.mu.Unlock()
	q := &Question{ID: fmt.Sprintf("test-%d", time.Now().UnixNano()), Session: testSession,
		Title: "Test alert: did this reach your phone?", Detail: "Answer with Yes, No or Reply. Your answer shows up in Potluck on your computer.", Kind: "ask"}
	h.pushAlert(q, "Potluck test")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pushErr != "" {
		return errors.New(h.pushErr)
	}
	return nil
}

// pushAlert sends one question to every phone that allowed alerts.
func (h *Host) pushAlert(q *Question, task string) {
	c := h.cfg.Get()
	if c.Push == nil || c.Push.KeyPath == "" {
		return
	}
	body := q.Title
	if q.Detail != "" && q.Kind != "plan" {
		body += "\n" + trimTo(strings.TrimSpace(q.Detail), 180)
	}
	payload, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":              map[string]string{"title": "Potluck needs you", "subtitle": task, "body": body},
			"category":           "POTLUCK_QUESTION",
			"sound":              "default",
			"interruption-level": "time-sensitive",
			"thread-id":          q.Session,
		},
		"session": q.Session, "question": q.ID,
	})
	tok, err := apnsToken(*c.Push)
	if err != nil {
		h.pushProblem(err)
		return
	}
	for id, p := range c.Phones {
		if p.PushToken == "" {
			continue
		}
		env := p.PushEnv
		res, b, err := h.apnsSend(env, p.PushToken, tok, c.Push.Topic, q.ID, payload)
		if err == nil && res == 400 && strings.Contains(string(b), "BadDeviceToken") {
			// A token from the other push server (a test build talks to Apple's sandbox, an App
			// Store build to production). Try the other one and remember which worked.
			other := "development"
			if env == "development" {
				other = "production"
			}
			if r2, b2, err2 := h.apnsSend(other, p.PushToken, tok, c.Push.Topic, q.ID, payload); err2 == nil && r2 == 200 {
				h.cfg.Update(func(c *Config) { pp := c.Phones[id]; pp.PushEnv = other; c.Phones[id] = pp })
				continue
			} else {
				res, b, err = r2, b2, err2
			}
		}
		if err != nil {
			h.pushProblem(err)
			continue
		}
		if res == 410 || strings.Contains(string(b), "BadDeviceToken") || strings.Contains(string(b), "Unregistered") {
			h.cfg.Update(func(c *Config) { // the app was removed or the token changed
				pp := c.Phones[id]
				pp.PushToken = ""
				c.Phones[id] = pp
			})
		} else if res != 200 {
			h.pushProblem(fmt.Errorf("Apple refused the alert (%d): %s", res, strings.TrimSpace(string(b))))
		}
	}
}

// apnsSend posts one alert to Apple's push server for env ("development" = sandbox).
func (h *Host) apnsSend(env, token, jwt, topic, collapse string, payload []byte) (int, []byte, error) {
	host := "https://api.push.apple.com"
	if env == "development" {
		host = "https://api.sandbox.push.apple.com"
	}
	req, err := http.NewRequest("POST", host+"/3/device/"+url.PathEscape(token), bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-collapse-id", collapse)
	res, err := apnsClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
	res.Body.Close()
	return res.StatusCode, b, nil
}

func (h *Host) pushProblem(err error) {
	h.mu.Lock()
	h.pushErr = err.Error()
	h.mu.Unlock()
}
