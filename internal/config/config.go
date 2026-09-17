// Package config carrega e valida a configuracao do backend a partir do
// ambiente (e, opcionalmente, de um YAML). Nenhum outro pacote deve ler
// os.Getenv diretamente.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Ambientes suportados.
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// DefaultPort e a porta publica do backend (ver docker-compose.yml).
const DefaultPort = "8082"

// Config agrega todas as configuracoes de runtime do servico.
type Config struct {
	// Campos do scaffold (T1) — mantidos para compatibilidade.
	Env             string
	Port            string
	DBPath          string
	LogLevel        string
	CORSOrigins     []string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	ShutdownTimeout time.Duration

	// Autenticacao JWT.
	Auth AuthConfig
	// Banco (parametros avancados).
	Database DatabaseConfig
	// Integracoes com o Hermes CLI.
	Hermes HermesConfig

	// EphemeralSecret indica que o segredo JWT foi gerado em runtime.
	EphemeralSecret bool
}

// AuthConfig descreve o login admin e o JWT.
type AuthConfig struct {
	JWTSecret         string        `yaml:"jwt_secret"`
	Issuer            string        `yaml:"issuer"`
	TokenTTL          time.Duration `yaml:"-"`
	TokenTTLHours     int           `yaml:"token_ttl_hours"`
	AdminUser         string        `yaml:"admin_user"`
	AdminPassword     string        `yaml:"admin_password"`
	AdminPasswordHash string        `yaml:"admin_password_hash"`
}

// DatabaseConfig parametriza a conexao SQLite.
type DatabaseConfig struct {
	Path         string        `yaml:"path"`
	BusyTimeout  time.Duration `yaml:"-"`
	MaxOpenConns int           `yaml:"max_open_conns"`
}

// HermesConfig descreve como o backend chama a CLI do Hermes.
type HermesConfig struct {
	Bin          string `yaml:"bin"`
	CronJobsPath string `yaml:"cron_jobs_path"`
	KanbanBoard  string `yaml:"kanban_board"`
	TimeoutSecs  int    `yaml:"timeout_seconds"`
	CacheTTLSecs int    `yaml:"cache_ttl_seconds"`
	WorkDir      string `yaml:"workdir"`
}

// fileConfig espelha o YAML opcional (as env vars sempre vencem).
type fileConfig struct {
	Server struct {
		Host     string `yaml:"host"`
		Port     int    `yaml:"port"`
		Mode     string `yaml:"mode"`
		LogLevel string `yaml:"log_level"`
	} `yaml:"server"`
	Database struct {
		Path          string `yaml:"path"`
		BusyTimeoutMS int    `yaml:"busy_timeout_ms"`
		MaxOpenConns  int    `yaml:"max_open_conns"`
	} `yaml:"database"`
	Auth   AuthConfig   `yaml:"auth"`
	Hermes HermesConfig `yaml:"hermes"`
	CORS   struct {
		AllowedOrigins []string `yaml:"allowed_origins"`
	} `yaml:"cors"`
}

// Load le path (YAML opcional), aplica as env vars e valida o resultado.
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		raw, err := os.ReadFile(path)
		switch {
		case err == nil:
			var file fileConfig
			if err := yaml.Unmarshal(raw, &file); err != nil {
				return nil, fmt.Errorf("config: parse %s: %w", path, err)
			}
			applyFile(cfg, &file)
		case os.IsNotExist(err):
			// deploy 100% por env: arquivo ausente e valido.
		default:
			return nil, fmt.Errorf("config: ler %s: %w", path, err)
		}
	}

	applyEnv(cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Auth.JWTSecret == "" {
		if cfg.IsProduction() {
			return nil, fmt.Errorf("config: JWT_SECRET e obrigatorio em ENVIRONMENT=%s", EnvProduction)
		}
		secret, err := randomSecret()
		if err != nil {
			return nil, err
		}
		cfg.Auth.JWTSecret = secret
		cfg.EphemeralSecret = true
	}
	if len(cfg.Auth.JWTSecret) < 16 {
		return nil, fmt.Errorf("config: JWT_SECRET precisa de pelo menos 16 caracteres")
	}
	if cfg.Auth.AdminPasswordHash == "" && cfg.Auth.AdminPassword == "" {
		return nil, fmt.Errorf("config: defina ADMIN_PASSWORD ou ADMIN_PASSWORD_HASH")
	}
	cfg.Auth.TokenTTL = time.Duration(cfg.Auth.TokenTTLHours) * time.Hour
	if cfg.Database.BusyTimeout <= 0 {
		cfg.Database.BusyTimeout = 5 * time.Second
	}
	if cfg.Database.MaxOpenConns <= 0 {
		cfg.Database.MaxOpenConns = 1
	}
	return cfg, nil
}

// Validate garante que a configuracao e coerente antes do servidor subir.
func (c *Config) Validate() error {
	if c.Port == "" {
		return fmt.Errorf("config: PORT nao pode ser vazio")
	}
	if _, err := strconv.Atoi(c.Port); err != nil {
		return fmt.Errorf("config: PORT %q invalida: %w", c.Port, err)
	}
	if c.DBPath == "" {
		return fmt.Errorf("config: DB_PATH nao pode ser vazio")
	}
	switch c.Env {
	case EnvDevelopment, EnvProduction:
	default:
		return fmt.Errorf("config: ENVIRONMENT %q invalida (use %q ou %q)", c.Env, EnvDevelopment, EnvProduction)
	}
	return nil
}

// IsProduction informa se o servico roda em modo de producao.
func (c *Config) IsProduction() bool { return c.Env == EnvProduction }

// IsDevelopment informa se o servico roda em modo de desenvolvimento.
func (c *Config) IsDevelopment() bool { return c.Env == EnvDevelopment }

// Addr devolve o endereco de escuta no formato esperado por net/http.
func (c *Config) Addr() string { return ":" + c.Port }

// GinMode traduz ENVIRONMENT para o modo do gin.
func (c *Config) GinMode() string {
	if c.IsProduction() {
		return "release"
	}
	return "debug"
}

func defaults() *Config {
	return &Config{
		Env:             EnvDevelopment,
		Port:            DefaultPort,
		DBPath:          "data/itscwf_fabrica.db",
		LogLevel:        "info",
		CORSOrigins:     []string{"*"},
		ReadTimeout:     15 * time.Second,
		WriteTimeout:    30 * time.Second,
		ShutdownTimeout: 10 * time.Second,
		Auth: AuthConfig{
			Issuer:        "itscwf-fabrica",
			TokenTTLHours: 12,
			AdminUser:     "admin",
			AdminPassword: "fabrica",
		},
		Database: DatabaseConfig{Path: "data/itscwf_fabrica.db", BusyTimeout: 5 * time.Second, MaxOpenConns: 1},
		Hermes: HermesConfig{
			Bin:          "hermes",
			CronJobsPath: "/root/.hermes/cron/jobs.json",
			KanbanBoard:  "itscwf_fabrica",
			TimeoutSecs:  30,
			CacheTTLSecs: 15,
			WorkDir:      "/root",
		},
	}
}

func applyFile(cfg *Config, file *fileConfig) {
	if file.Server.Port > 0 {
		cfg.Port = strconv.Itoa(file.Server.Port)
	}
	if file.Server.Mode != "" {
		cfg.Env = strings.ToLower(file.Server.Mode)
		if cfg.Env == "release" || cfg.Env == "debug" || cfg.Env == "test" {
			// "release"/"debug" sao vocabulario do gin; traduz para o ambiente.
			if file.Server.Mode == "release" {
				cfg.Env = EnvProduction
			} else {
				cfg.Env = EnvDevelopment
			}
		}
	}
	if file.Server.LogLevel != "" {
		cfg.LogLevel = file.Server.LogLevel
	}
	if file.Database.Path != "" {
		cfg.DBPath = file.Database.Path
		cfg.Database.Path = file.Database.Path
	}
	if file.Database.MaxOpenConns > 0 {
		cfg.Database.MaxOpenConns = file.Database.MaxOpenConns
	}
	if file.Auth.JWTSecret != "" {
		cfg.Auth.JWTSecret = file.Auth.JWTSecret
	}
	if file.Auth.Issuer != "" {
		cfg.Auth.Issuer = file.Auth.Issuer
	}
	if file.Auth.TokenTTLHours > 0 {
		cfg.Auth.TokenTTLHours = file.Auth.TokenTTLHours
	}
	if file.Auth.AdminUser != "" {
		cfg.Auth.AdminUser = file.Auth.AdminUser
	}
	cfg.Auth.AdminPassword = pickNonEmpty(file.Auth.AdminPassword, cfg.Auth.AdminPassword)
	cfg.Auth.AdminPasswordHash = file.Auth.AdminPasswordHash
	if file.Hermes.Bin != "" {
		cfg.Hermes.Bin = file.Hermes.Bin
	}
	if file.Hermes.CronJobsPath != "" {
		cfg.Hermes.CronJobsPath = file.Hermes.CronJobsPath
	}
	if file.Hermes.KanbanBoard != "" {
		cfg.Hermes.KanbanBoard = file.Hermes.KanbanBoard
	}
	if file.Hermes.TimeoutSecs > 0 {
		cfg.Hermes.TimeoutSecs = file.Hermes.TimeoutSecs
	}
	if file.Hermes.CacheTTLSecs >= 0 {
		cfg.Hermes.CacheTTLSecs = file.Hermes.CacheTTLSecs
	}
	if file.Hermes.WorkDir != "" {
		cfg.Hermes.WorkDir = file.Hermes.WorkDir
	}
	if len(file.CORS.AllowedOrigins) > 0 {
		cfg.CORSOrigins = file.CORS.AllowedOrigins
	}
}

func applyEnv(cfg *Config) {
	if value := getEnv("ENVIRONMENT", ""); value != "" {
		cfg.Env = strings.ToLower(value)
	}
	if value := firstEnv("FABRICA_SERVER_ENV", "FABRICA_MODE"); value != "" {
		cfg.Env = strings.ToLower(value)
	}
	if value := firstEnv("FABRICA_SERVER_PORT", "PORT", "FABRICA_PORT"); value != "" {
		cfg.Port = value
	}
	if value := firstEnv("FABRICA_DB_PATH", "DB_PATH", "FABRICA_DATABASE_PATH"); value != "" {
		cfg.DBPath = value
		cfg.Database.Path = value
	}
	if value := firstEnv("FABRICA_LOG_LEVEL", "LOG_LEVEL"); value != "" {
		cfg.LogLevel = value
	}
	if value := firstEnv("FABRICA_CORS_ORIGINS", "CORS_ORIGINS"); value != "" {
		cfg.CORSOrigins = splitAndTrim(value)
	}
	if value := firstEnv("FABRICA_HTTP_READ_TIMEOUT", "HTTP_READ_TIMEOUT"); value != "" {
		cfg.ReadTimeout = parseDuration(value, cfg.ReadTimeout)
	}
	if value := firstEnv("FABRICA_HTTP_WRITE_TIMEOUT", "HTTP_WRITE_TIMEOUT"); value != "" {
		cfg.WriteTimeout = parseDuration(value, cfg.WriteTimeout)
	}
	if value := firstEnv("FABRICA_HTTP_SHUTDOWN_TIMEOUT", "HTTP_SHUTDOWN_TIMEOUT"); value != "" {
		cfg.ShutdownTimeout = parseDuration(value, cfg.ShutdownTimeout)
	}
	if value := firstEnv("FABRICA_JWT_SECRET", "JWT_SECRET"); value != "" {
		cfg.Auth.JWTSecret = value
		cfg.EphemeralSecret = false
	}
	if value := firstEnv("FABRICA_JWT_ISSUER", "JWT_ISSUER"); value != "" {
		cfg.Auth.Issuer = value
	}
	if value := firstEnv("FABRICA_TOKEN_TTL_HOURS", "TOKEN_TTL_HOURS"); value != "" {
		cfg.Auth.TokenTTLHours = atoi(value, cfg.Auth.TokenTTLHours)
	}
	if value := firstEnv("FABRICA_ADMIN_USER", "ADMIN_USER"); value != "" {
		cfg.Auth.AdminUser = value
	}
	if value := firstEnv("FABRICA_ADMIN_PASSWORD", "ADMIN_PASSWORD"); value != "" {
		cfg.Auth.AdminPassword = value
	}
	if value := firstEnv("FABRICA_ADMIN_PASSWORD_HASH", "ADMIN_PASSWORD_HASH"); value != "" {
		cfg.Auth.AdminPasswordHash = value
	}
	if value := firstEnv("FABRICA_DB_BUSY_TIMEOUT_MS", "DB_BUSY_TIMEOUT_MS"); value != "" {
		cfg.Database.BusyTimeout = time.Duration(atoi(value, 5000)) * time.Millisecond
	}
	if value := firstEnv("FABRICA_DB_MAX_OPEN_CONNS", "DB_MAX_OPEN_CONNS"); value != "" {
		cfg.Database.MaxOpenConns = atoi(value, cfg.Database.MaxOpenConns)
	}
	if value := firstEnv("FABRICA_HERMES_BIN", "HERMES_BIN"); value != "" {
		cfg.Hermes.Bin = value
	}
	if value := firstEnv("FABRICA_HERMES_CRON_JOBS", "HERMES_CRON_JOBS", "HERMES_CRON_JOBS_PATH"); value != "" {
		cfg.Hermes.CronJobsPath = value
	}
	if value := firstEnv("FABRICA_HERMES_BOARD", "HERMES_KANBAN_BOARD"); value != "" {
		cfg.Hermes.KanbanBoard = value
	}
	if value := firstEnv("FABRICA_HERMES_TIMEOUT_SECONDS", "HERMES_TIMEOUT_SECONDS"); value != "" {
		cfg.Hermes.TimeoutSecs = atoi(value, cfg.Hermes.TimeoutSecs)
	}
	if value := firstEnv("FABRICA_HERMES_CACHE_TTL_SECONDS", "HERMES_CACHE_TTL_SECONDS"); value != "" {
		cfg.Hermes.CacheTTLSecs = atoi(value, cfg.Hermes.CacheTTLSecs)
	}
	if value := firstEnv("FABRICA_HERMES_WORKDIR", "HERMES_WORKDIR"); value != "" {
		cfg.Hermes.WorkDir = value
	}
}

func getEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func pickNonEmpty(candidate, fallback string) string {
	if strings.TrimSpace(candidate) != "" {
		return candidate
	}
	return fallback
}

func atoi(raw string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return parsed
}

func parseDuration(raw string, fallback time.Duration) time.Duration {
	if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
		return parsed
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func splitAndTrim(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("config: gerar segredo efemero: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
