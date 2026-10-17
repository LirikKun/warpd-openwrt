package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// mihomoReload PUTs the config file path over the external-controller API.
func mihomoReload() error {
	body, _ := json.Marshal(map[string]string{"path": mihomoConf})
	req, err := http.NewRequest(http.MethodPut, "http://"+mihomoAPI+"/configs?force=true", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("mihomo api status %d", resp.StatusCode)
	}
	return nil
}

// mihomoAlive checks the API is answering.
func mihomoAlive() bool {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + mihomoAPI + "/version")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// loadAccount reads the WARP account JSON into the fields the config needs.
type warpAccount struct {
	ID            string `json:"id"`
	PrivateKey    string `json:"private_key"`
	PeerPub       string `json:"peer_public_key"`
	IPv4          string `json:"ipv4"`
	IPv6          string `json:"ipv6"`
	GenI1         string `json:"gen_i1"`
	MasquePriv    string `json:"-"`
	MasquePeerPub string `json:"-"`
}

func loadAccount(path string) (warpAccount, error) {
	var a warpAccount
	raw, err := os.ReadFile(path)
	if err != nil {
		return a, err
	}
	var m struct {
		ID            string `json:"id"`
		PrivateKey    string `json:"private_key"`
		PeerPublicKey string `json:"peer_public_key"`
		IPv4          string `json:"ipv4"`
		IPv6          string `json:"ipv6"`
		GenI1         string `json:"gen_i1"`
		Masque        struct {
			PrivateKey    string `json:"private_key"`
			PeerPublicKey string `json:"peer_public_key"`
		} `json:"masque"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return a, err
	}
	a = warpAccount{
		ID:         m.ID,
		PrivateKey: m.PrivateKey,
		PeerPub:    m.PeerPublicKey,
		IPv4:       m.IPv4,
		IPv6:       m.IPv6,
		GenI1:      m.GenI1,
		MasquePriv: m.Masque.PrivateKey,
	}
	// masque peer public key is PEM; mihomo wants the base64 body
	a.MasquePeerPub = pemBody(m.Masque.PeerPublicKey)
	return a, nil
}

func pemBody(pem string) string {
	const begin = "-----BEGIN PUBLIC KEY-----"
	const end = "-----END PUBLIC KEY-----"
	s := pem
	if i := index(s, begin); i >= 0 {
		s = s[i+len(begin):]
	}
	if j := index(s, end); j >= 0 {
		s = s[:j]
	}
	out := make([]byte, 0, len(s))
	for _, c := range s {
		if c == '\n' || c == '\r' || c == ' ' {
			continue
		}
		out = append(out, byte(c))
	}
	return string(out)
}

func index(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
