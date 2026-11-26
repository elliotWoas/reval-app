package httpapi

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestValidateInitData(t *testing.T) {
	const token = "123456:TEST-token"
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	fields := func(at time.Time) map[string]string {
		return map[string]string{
			"auth_date": strconv.FormatInt(at.Unix(), 10),
			"query_id":  "AAHdF6IQAAAAAN0XohDhrOrc",
			"user":      `{"id":42,"first_name":"Sara","username":"sara","language_code":"fa"}`,
			"signature": "ignored-but-signed",
		}
	}
	valid := SignInitData(token, fields(now.Add(-time.Minute)))

	u, err := ValidateInitData(valid, token, time.Hour, now)
	if err != nil || u.ID != 42 || u.FirstName != "Sara" || u.LanguageCode != "fa" {
		t.Fatalf("valid data rejected: %+v %v", u, err)
	}

	cases := []struct {
		name string
		raw  string
		tok  string
		want error
	}{
		{"missing", "", token, ErrInitMissing},
		{"wrong token", valid, "999:other", ErrInitBadHash},
		{"tampered user", strings.Replace(valid, "Sara", "Eve", 1), token, ErrInitBadHash},
		{"no hash", "auth_date=1&user=%7B%7D", token, ErrInitBadHash},
		{"garbage hash", valid[:strings.Index(valid, "hash=")] + "hash=zz", token, ErrInitBadHash},
		{"expired", SignInitData(token, fields(now.Add(-2*time.Hour))), token, ErrInitExpired},
		{"from the future", SignInitData(token, fields(now.Add(time.Hour))), token, ErrInitExpired},
		{"no user", SignInitData(token, map[string]string{"auth_date": strconv.FormatInt(now.Unix(), 10)}), token, ErrInitMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ValidateInitData(c.raw, c.tok, time.Hour, now); !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
}

func TestUserLimiter(t *testing.T) {
	l := newUserLimiter(2, time.Minute)
	now := time.Now()
	if !l.allow(1, now) || !l.allow(1, now) || l.allow(1, now) {
		t.Fatal("third request in the window must be refused")
	}
	if !l.allow(2, now) {
		t.Fatal("limits are per user")
	}
	if !l.allow(1, now.Add(time.Minute)) {
		t.Fatal("a new window must reset the count")
	}
}
