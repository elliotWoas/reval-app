// Package config loads defaults from YAML, overrides them from env and validates the result.
package config

import (
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/go-playground/validator/v10"
	"gopkg.in/yaml.v3"
)

// Config is the full runtime configuration.
type Config struct {
	App          App          `yaml:"app"`
	Telegram     Telegram     `yaml:"telegram"`
	Mongo        Mongo        `yaml:"mongo"`
	TeamDefaults TeamDefaults `yaml:"team_defaults"`
	Options      Options      `yaml:"options"`
	Limits       Limits       `yaml:"limits"`
	Outbox       Outbox       `yaml:"outbox"`
	Scheduler    Scheduler    `yaml:"scheduler"`
}

// App holds process-level settings.
type App struct {
	LogLevel       string        `yaml:"log_level" env:"LOG_LEVEL" validate:"oneof=debug info warn error"`
	LocalePath     string        `yaml:"locale_path" env:"LOCALE_PATH" validate:"required"`
	Workers        int           `yaml:"workers" env:"WORKERS" validate:"min=1,max=1024"`
	WorkerQueue    int           `yaml:"worker_queue" validate:"min=1"`
	HandlerTimeout time.Duration `yaml:"handler_timeout" validate:"min=1s"`
	HealthAddr     string        `yaml:"health_addr" env:"HEALTH_ADDR"`
	AdminChatID    int64         `yaml:"admin_chat_id" env:"ADMIN_CHAT_ID"`
	InstanceID     string        `yaml:"instance_id" env:"INSTANCE_ID"`
}

// Telegram holds bot connection settings. Token only comes from env.
type Telegram struct {
	Token       string        `yaml:"-" env:"TELEGRAM_BOT_TOKEN" validate:"required"`
	Mode        string        `yaml:"mode" env:"TELEGRAM_MODE" validate:"oneof=polling"`
	PollTimeout time.Duration `yaml:"poll_timeout" validate:"min=1s"`
	APITimeout  time.Duration `yaml:"api_timeout" validate:"min=1s"`
}

// Mongo holds database settings. URI only comes from env.
type Mongo struct {
	URI            string        `yaml:"-" env:"MONGO_URI" validate:"required"`
	Database       string        `yaml:"database" env:"MONGO_DB" validate:"required"`
	ConnectTimeout time.Duration `yaml:"connect_timeout" validate:"min=1s"`
	OpTimeout      time.Duration `yaml:"op_timeout" validate:"min=100ms"`
	MaxPool        uint64        `yaml:"max_pool" validate:"min=1"`
}

// TeamDefaults are copied into every new team; each team then keeps its own values.
type TeamDefaults struct {
	Timezone           string   `yaml:"timezone" validate:"required"`
	WorkDays           []int    `yaml:"work_days" validate:"min=1,dive,min=0,max=6"`
	Morning            string   `yaml:"morning" validate:"required"`
	Deadline           string   `yaml:"deadline" validate:"required"`
	Checkins           []string `yaml:"checkins"`
	Evening            string   `yaml:"evening" validate:"required"`
	Summary            string   `yaml:"summary" validate:"required"`
	Weekly             string   `yaml:"weekly" validate:"required"`
	ReminderBeforeMin  int      `yaml:"reminder_before_min" validate:"min=5,max=180"`
	ShowMissingInGroup bool     `yaml:"show_missing_in_group"`
	ManualApproval     bool     `yaml:"manual_approval"`
	LeaveApproval      bool     `yaml:"leave_approval"`
}

// WorkDayPreset is one button of the work-days keyboard.
type WorkDayPreset struct {
	Key  string `yaml:"key" validate:"required"`
	Days []int  `yaml:"days" validate:"min=1,dive,min=0,max=6"`
}

// CheckinPreset is one button of the check-in keyboard.
// Either Times is fixed, or EveryHours generates times between deadline and evening.
type CheckinPreset struct {
	Key        string   `yaml:"key" validate:"required"`
	Times      []string `yaml:"times"`
	EveryHours int      `yaml:"every_hours"`
}

// Options are the suggested choices shown on keyboards.
type Options struct {
	WorkDayPresets []WorkDayPreset `yaml:"work_day_presets" validate:"min=1,dive"`
	Morning        []string        `yaml:"morning" validate:"min=1"`
	Deadline       []string        `yaml:"deadline" validate:"min=1"`
	Checkins       []CheckinPreset `yaml:"checkins" validate:"min=1,dive"`
	Evening        []string        `yaml:"evening" validate:"min=1"`
	Summary        []string        `yaml:"summary" validate:"min=1"`
	Weekly         []string        `yaml:"weekly" validate:"min=1"`
	FailReasons    []string        `yaml:"fail_reasons" validate:"min=1"`
	Progress       []int           `yaml:"progress" validate:"min=1"`
}

// Limits are business limits.
type Limits struct {
	TeamNameMin         int           `yaml:"team_name_min" validate:"min=1"`
	TeamNameMax         int           `yaml:"team_name_max" validate:"gtfield=TeamNameMin"`
	DisplayNameMax      int           `yaml:"display_name_max" validate:"min=1"`
	InviteKeyGroups     int           `yaml:"invite_key_groups" validate:"min=1,max=4"`
	InviteKeyGroupLen   int           `yaml:"invite_key_group_len" validate:"min=3,max=8"`
	JoinAttemptsPerHour int           `yaml:"join_attempts_per_hour" validate:"min=1"`
	MaxTasks            int           `yaml:"max_tasks" validate:"min=1,max=50"`
	TaskMaxLen          int           `yaml:"task_max_len" validate:"min=10"`
	LeaveDaysAhead      int           `yaml:"leave_days_ahead" validate:"min=1,max=60"`
	MinGapMinutes       int           `yaml:"min_gap_minutes" validate:"min=0"`
	GroupReminderMax    int           `yaml:"group_reminder_max" validate:"min=0"`
	LargeTeam           int           `yaml:"large_team" validate:"min=2"`
	MessageMaxLen       int           `yaml:"message_max_len" validate:"min=500,max=4096"`
	SessionTTL          time.Duration `yaml:"session_ttl" validate:"min=1m"`
	StuckCarryDays      int           `yaml:"stuck_carry_days" validate:"min=1"`
	NoteMaxLen          int           `yaml:"note_max_len" validate:"min=10"`
}

// Outbox holds send-rate limits.
type Outbox struct {
	GlobalPerSec  float64       `yaml:"global_per_sec" env:"OUTBOX_GLOBAL_PER_SEC" validate:"gt=0"`
	ChatPerSec    float64       `yaml:"chat_per_sec" validate:"gt=0"`
	GroupPerMin   float64       `yaml:"group_per_min" validate:"gt=0"`
	Batch         int           `yaml:"batch" validate:"min=1"`
	MaxAttempts   int           `yaml:"max_attempts" validate:"min=1"`
	PollInterval  time.Duration `yaml:"poll_interval" validate:"min=10ms"`
	RetryBackoff  time.Duration `yaml:"retry_backoff" validate:"min=1s"`
	SentRetention time.Duration `yaml:"sent_retention" validate:"min=1h"`
}

// Scheduler holds the ticker settings.
type Scheduler struct {
	Tick       time.Duration `yaml:"tick" validate:"min=1s"`
	LockTTL    time.Duration `yaml:"lock_ttl" validate:"min=5s"`
	JobRunsTTL time.Duration `yaml:"job_runs_ttl" validate:"min=24h"`
	BatchTeams int           `yaml:"batch_teams" validate:"min=1"`
}

// Load reads YAML from path, then applies env overrides and validates.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw, true)
}

// Parse builds a config from YAML bytes. withEnv applies environment overrides.
func Parse(raw []byte, withEnv bool) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if withEnv {
		if err := env.Parse(&c); err != nil {
			return nil, fmt.Errorf("env config: %w", err)
		}
	}
	if c.App.InstanceID == "" {
		host, _ := os.Hostname()
		c.App.InstanceID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	return &c, nil
}

// Validate checks all fields. Call it once at boot.
func (c *Config) Validate() error {
	if err := validator.New().Struct(c); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	if _, err := time.LoadLocation(c.TeamDefaults.Timezone); err != nil {
		return fmt.Errorf("invalid config: timezone %q: %w", c.TeamDefaults.Timezone, err)
	}
	return nil
}
