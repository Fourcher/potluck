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
			"thread-id":          s.ID,
		},
		"session": s.ID, "question": q.ID,
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
		host := "https://api.push.apple.com"
		if p.PushEnv == "development" {
			host = "https://api.sandbox.push.apple.com"
		}
		req, _ := http.NewRequest("POST", host+"/3/device/"+p.PushToken, bytes.NewReader(payload))
		req.Header.Set("authorization", "bearer "+tok)
		req.Header.Set("apns-topic", c.Push.Topic)
		req.Header.Set("apns-push-type", "alert")
		req.Header.Set("apns-priority", "10")
		req.Header.Set("apns-collapse-id", q.ID)
		res, err := apnsClient.Do(req)
		if err != nil {
			h.pushProblem(err)
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		res.Body.Close()
		if res.StatusCode == 410 || strings.Contains(string(b), "BadDeviceToken") || strings.Contains(string(b), "Unregistered") {
			h.cfg.Update(func(c *Config) { // the app was removed or the token changed
				pp := c.Phones[id]
				pp.PushToken = ""
				c.Phones[id] = pp
			})
		} else if res.StatusCode != 200 {
			h.pushProblem(fmt.Errorf("Apple refused the alert (%s): %s", res.Status, strings.TrimSpace(string(b))))
		}
	}
}

func (h *Host) pushProblem(err error) {
	h.mu.Lock()
	h.pushErr = err.Error()
	h.mu.Unlock()
}
