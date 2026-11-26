package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TGUser is the user object inside verified Telegram Mini App init data.
type TGUser struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Username     string `json:"username"`
	LanguageCode string `json:"language_code"`
	PhotoURL     string `json:"photo_url"`
}

// Init data validation errors.
var (
	ErrInitMissing   = errors.New("init data missing")
	ErrInitBadHash   = errors.New("init data signature mismatch")
	ErrInitExpired   = errors.New("init data expired")
	ErrInitMalformed = errors.New("init data malformed")
)

// clockSkew tolerates small clock differences between Telegram and this server.
const clockSkew = 5 * time.Minute

// ValidateInitData verifies the raw Telegram.WebApp.initData string with the bot token
// (https://core.telegram.org/bots/webapps#validating-data-received-via-the-mini-app) and returns the user.
//
// secret = HMAC_SHA256(key="WebAppData", msg=botToken)
// hash   = hex(HMAC_SHA256(key=secret, msg=data_check_string))
// where data_check_string is every field except "hash", sorted by key, as "key=value" joined by "\n".
func ValidateInitData(raw, botToken string, maxAge time.Duration, now time.Time) (TGUser, error) {
	var u TGUser
	if raw == "" {
		return u, ErrInitMissing
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return u, ErrInitMalformed
	}
	hash := vals.Get("hash")
	if hash == "" {
		return u, ErrInitBadHash
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + vals.Get(k)
	}
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	want := hmacSHA256(secret, []byte(strings.Join(lines, "\n")))
	got, err := hex.DecodeString(hash)
	if err != nil || !hmac.Equal(want, got) {
		return u, ErrInitBadHash
	}
	authUnix, err := strconv.ParseInt(vals.Get("auth_date"), 10, 64)
	if err != nil || authUnix <= 0 {
		return u, ErrInitMalformed
	}
	authAt := time.Unix(authUnix, 0)
	if now.Sub(authAt) > maxAge || authAt.Sub(now) > clockSkew {
		return u, ErrInitExpired
	}
	if err := json.Unmarshal([]byte(vals.Get("user")), &u); err != nil || u.ID == 0 {
		return TGUser{}, ErrInitMalformed
	}
	return u, nil
}

// SignInitData builds a valid init data string. It exists for tests and local tooling only.
func SignInitData(botToken string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	q := url.Values{}
	for i, k := range keys {
		lines[i] = k + "=" + fields[k]
		q.Set(k, fields[k])
	}
	secret := hmacSHA256([]byte("WebAppData"), []byte(botToken))
	q.Set("hash", hex.EncodeToString(hmacSHA256(secret, []byte(strings.Join(lines, "\n")))))
	return q.Encode()
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}
